package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	ttspkg "github.com/wangning19940904/AgentMux/tts"
)

// SetConnect attaches the channels/triggers runtime. Nil keeps the CRUD
// endpoints working in persist-only mode (no live attach/scheduling).
func (s *Server) SetConnect(svc *core.ConnectService) { s.connect = svc }

// apiChannel is a channel plus live status and display enrichment.
type apiChannel struct {
	core.Channel
	AgentName            string                       `json:"agent_name,omitempty"`
	DefaultMessagePrompt string                       `json:"default_message_prompt,omitempty"`
	BotName              string                       `json:"bot_name,omitempty"`
	BotAvatarURL         string                       `json:"bot_avatar_url,omitempty"`
	BotAvatarProxyURL    string                       `json:"bot_avatar_proxy_url,omitempty"`
	BotOpenID            string                       `json:"bot_open_id,omitempty"`
	State                string                       `json:"state,omitempty"`
	Connected            bool                         `json:"connected"`
	Error                string                       `json:"error,omitempty"`
	StartedAt            *time.Time                   `json:"started_at,omitempty"`
	ConnectedAt          *time.Time                   `json:"connected_at,omitempty"`
	LastCheckedAt        *time.Time                   `json:"last_checked_at,omitempty"`
	LastHeartbeatAt      *time.Time                   `json:"last_heartbeat_at,omitempty"`
	LastEventAt          *time.Time                   `json:"last_event_at,omitempty"`
	LastInboundAt        *time.Time                   `json:"last_inbound_at,omitempty"`
	CodexCapability      *core.CodexControlCapability `json:"codex_control_capability,omitempty"`
}

// apiTrigger is a trigger plus display enrichment.
type apiTrigger struct {
	core.Trigger
	AgentName   string `json:"agent_name,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	HookPath    string `json:"hook_path,omitempty"`
}

func (s *Server) handleChannelsList(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []apiChannel{})
		return
	}
	principal := requestPrincipal(r)
	var channels []core.Channel
	var err error
	if principal.IsTenant() {
		channels, err = s.st.ListChannelsForTenant(r.Context(), principal.TenantID)
	} else {
		channels, err = s.st.ListChannels(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.labelChannelOwners(r.Context(), channels)
	statuses := map[string]core.ChannelStatus{}
	if s.connect != nil {
		for _, st := range s.connect.ChannelStatuses() {
			statuses[st.ChannelID] = st
		}
	}
	agentNames := s.agentNames(r.Context())
	out := make([]apiChannel, 0, len(channels))
	for _, ch := range channels {
		botInfo := s.lookupChannelBotInfo(r.Context(), ch)
		ch.Config = redactStringMap(ch.Config)
		item := apiChannel{
			Channel:              ch,
			AgentName:            agentNames[ch.AgentID],
			DefaultMessagePrompt: core.ChannelDefaultMessagePrompt(ch),
		}
		if s.connect != nil {
			if capability, ok := s.connect.ChannelCodexControlCapability(ch.ID); ok {
				item.CodexCapability = &capability
			}
		}
		if botInfo != nil {
			item.BotName = botInfo.Name
			item.BotAvatarURL = botInfo.AvatarURL
			if botInfo.AvatarURL != "" {
				item.BotAvatarProxyURL = channelAvatarProxyURL(r, ch.ID)
			}
			item.BotOpenID = botInfo.OpenID
		}
		if st, ok := statuses[ch.ID]; ok {
			item.State = st.State
			item.Connected = st.Connected
			item.Error = st.Error
			item.StartedAt = nonZeroTime(st.StartedAt)
			item.ConnectedAt = nonZeroTime(st.ConnectedAt)
			item.LastCheckedAt = nonZeroTime(st.LastCheckedAt)
			item.LastHeartbeatAt = nonZeroTime(st.LastHeartbeatAt)
			item.LastEventAt = nonZeroTime(st.LastEventAt)
			item.LastInboundAt = nonZeroTime(st.LastInboundAt)
		} else if ch.Enabled {
			item.State = "pending"
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func nonZeroTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func channelAvatarProxyURL(r *http.Request, channelID string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1:8765"
	}
	return scheme + "://" + host + "/channel-avatar?id=" + url.QueryEscape(channelID)
}

func (s *Server) handleChannelAvatar(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	ch, err := s.st.GetChannel(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ch == nil {
		http.NotFound(w, r)
		return
	}
	info := s.lookupChannelBotInfo(r.Context(), *ch)
	if info == nil || info.AvatarURL == "" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.AvatarURL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, fmt.Sprintf("avatar request failed: HTTP %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
}

type channelBotInfo struct {
	Name      string
	AvatarURL string
	OpenID    string
}

var channelBotOpenAPIBase = map[string]string{
	"feishu": "https://open.feishu.cn",
	"lark":   "https://open.larksuite.com",
}

func (s *Server) lookupChannelBotInfo(ctx context.Context, ch core.Channel) *channelBotInfo {
	if ch.Type != "feishu" && ch.Type != "lark" {
		return nil
	}
	appID := strings.TrimSpace(ch.Config["app_id"])
	appSecret := strings.TrimSpace(ch.Config["app_secret"])
	if appID == "" || appSecret == "" || appSecret == "<redacted>" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := fetchChannelBotInfo(ctx, &http.Client{Timeout: 3 * time.Second}, ch.Type, appID, appSecret)
	if err != nil {
		if s.log != nil {
			s.log.Warn("lookup channel bot info", "channel", ch.Name, "type", ch.Type, "err", err)
		}
		return nil
	}
	return info
}

func fetchChannelBotInfo(ctx context.Context, client *http.Client, platform, appID, appSecret string) (*channelBotInfo, error) {
	base := strings.TrimRight(channelBotOpenAPIBase[platform], "/")
	if base == "" {
		return nil, fmt.Errorf("unsupported bot info platform %q", platform)
	}
	payload, _ := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/open-apis/auth/v3/app_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token request failed: HTTP %d", resp.StatusCode)
	}
	var tokenResp struct {
		Code           int    `json:"code"`
		Msg            string `json:"msg"`
		AppAccessToken string `json:"app_access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokenResp); err != nil {
		return nil, err
	}
	if tokenResp.Code != 0 {
		return nil, fmt.Errorf("token request failed: %s", tokenResp.Msg)
	}
	if tokenResp.AppAccessToken == "" {
		return nil, fmt.Errorf("token request returned empty token")
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/open-apis/bot/v3/info", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tokenResp.AppAccessToken)
	resp, err = client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bot info request failed: HTTP %d", resp.StatusCode)
	}
	var infoResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			AppName   string `json:"app_name"`
			AvatarURL string `json:"avatar_url"`
			OpenID    string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&infoResp); err != nil {
		return nil, err
	}
	if infoResp.Code != 0 {
		return nil, fmt.Errorf("bot info request failed: %s", infoResp.Msg)
	}
	return &channelBotInfo{
		Name:      strings.TrimSpace(infoResp.Bot.AppName),
		AvatarURL: strings.TrimSpace(infoResp.Bot.AvatarURL),
		OpenID:    strings.TrimSpace(infoResp.Bot.OpenID),
	}, nil
}

func (s *Server) handleChannelUpsert(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	ch, ok := decodeJSON[core.Channel](w, r)
	if !ok {
		return
	}
	// Editing an existing channel requires manage access. An unknown ID is a
	// create with a caller-chosen ID and needs no prior authorization.
	var existing *core.Channel
	if id := strings.TrimSpace(ch.ID); id != "" {
		found, err := s.st.GetChannel(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if found != nil {
			if _, authorized := s.authorizeChannel(w, r, id, core.GrantLevelManage); !authorized {
				return
			}
			existing = found
		}
	}
	if err := s.normalizeChannel(r.Context(), &ch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.stampChannelOwnership(requestPrincipal(r), &ch, existing)
	s.channelClaimMu.Lock()
	defer s.channelClaimMu.Unlock()
	if isExclusiveLongConnection(ch) {
		channels, err := s.st.ListChannels(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		conflicts := collectChannelClaimConflicts(nil, "", "local machine", "", ch, channels)
		if err := s.disableChannelClaimConflicts(r.Context(), conflicts); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.st.UpsertChannel(r.Context(), &ch); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadChannels(r.Context())
	ch.Config = redactStringMap(ch.Config)
	writeJSON(w, http.StatusOK, &ch)
}

func (s *Server) handleChannelValidate(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	ch, ok := decodeJSON[core.Channel](w, r)
	if !ok {
		return
	}
	if err := s.normalizeChannel(r.Context(), &ch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ch.Config = redactStringMap(ch.Config)
	writeJSON(w, http.StatusOK, &ch)
}

func (s *Server) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if _, authorized := s.authorizeChannel(w, r, id, core.GrantLevelManage); !authorized {
		return
	}
	if err := s.st.DeleteChannel(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadChannels(r.Context())
	writeOK(w)
}

func (s *Server) handleChannelRestart(w http.ResponseWriter, r *http.Request) {
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if s.st != nil {
		if _, authorized := s.authorizeChannel(w, r, id, core.GrantLevelManage); !authorized {
			return
		}
	}
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime not running (start the daemon)")
		return
	}
	if err := s.connect.RestartChannel(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleTriggersList(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []apiTrigger{})
		return
	}
	principal := requestPrincipal(r)
	var triggers []core.Trigger
	var err error
	if principal.IsTenant() {
		triggers, err = s.st.ListTriggersForTenant(r.Context(), principal.TenantID)
	} else {
		triggers, err = s.st.ListTriggers(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	agentNames := s.agentNames(r.Context())
	channelNames := map[string]string{}
	if channels, err := s.st.ListChannels(r.Context()); err == nil {
		for _, ch := range channels {
			channelNames[ch.ID] = ch.Name
		}
	}
	out := make([]apiTrigger, 0, len(triggers))
	for _, tr := range triggers {
		item := apiTrigger{
			Trigger:     tr,
			AgentName:   agentNames[tr.AgentID],
			ChannelName: channelNames[tr.ChannelID],
		}
		if tr.Kind == core.TriggerWebhook {
			item.HookPath = "/hook/" + tr.ID
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTriggerUpsert(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	tr, ok := decodeJSON[core.Trigger](w, r)
	if !ok {
		return
	}
	principal := requestPrincipal(r)
	if id := strings.TrimSpace(tr.ID); id != "" {
		found, err := s.st.GetTrigger(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if found != nil {
			if _, authorized := s.authorizeTrigger(w, r, id, core.GrantLevelManage); !authorized {
				return
			}
			tr.OwnerTenantID = found.OwnerTenantID
		}
	}
	if err := s.normalizeTrigger(r.Context(), &tr); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A trigger fires an agent, so a tenant may only point one at an agent it
	// is allowed to run.
	if principal.IsTenant() {
		tr.OwnerTenantID = principal.TenantID
		if agentID := strings.TrimSpace(tr.AgentID); agentID != "" {
			if _, authorized := s.authorizeAgent(w, r, agentID, core.GrantLevelUse); !authorized {
				return
			}
		}
	}
	if err := s.st.UpsertTrigger(r.Context(), &tr); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadTriggers(r.Context())
	writeJSON(w, http.StatusOK, &tr)
}

func (s *Server) handleTriggerDelete(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if _, authorized := s.authorizeTrigger(w, r, id, core.GrantLevelManage); !authorized {
		return
	}
	if err := s.st.DeleteTrigger(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadTriggers(r.Context())
	writeOK(w)
}

func (s *Server) handleTriggerRun(w http.ResponseWriter, r *http.Request) {
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime not running (start the daemon)")
		return
	}
	if s.st != nil {
		if _, authorized := s.authorizeTrigger(w, r, id, core.GrantLevelUse); !authorized {
			return
		}
	}
	s.connect.RunTriggerNow(id, "")
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// handleInboundHook is the public webhook endpoint: POST /hook/{id}. It is
// outside /api/ so the bridge bearer token does not gate it; each trigger
// carries its own token instead.
func (s *Server) handleInboundHook(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	tr, err := s.st.GetTrigger(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tr == nil || tr.Kind != core.TriggerWebhook {
		http.Error(w, "webhook trigger not found", http.StatusNotFound)
		return
	}
	if !tr.Enabled {
		http.Error(w, "trigger disabled", http.StatusForbidden)
		return
	}
	if !hookTokenOK(r, tr.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.connect == nil {
		http.Error(w, "connect runtime not running", http.StatusServiceUnavailable)
		return
	}

	input := parseHookInput(r)
	s.connect.RunTriggerNow(id, input)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// parseHookInput extracts the prompt input from a webhook request body:
// JSON {"prompt": "...", "payload": ...} or a raw text body.
func parseHookInput(r *http.Request) string {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		return ""
	}
	var in struct {
		Prompt  string          `json:"prompt"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return strings.TrimSpace(string(body))
	}
	parts := []string{}
	if p := strings.TrimSpace(in.Prompt); p != "" {
		parts = append(parts, p)
	}
	if len(in.Payload) > 0 && string(in.Payload) != "null" {
		parts = append(parts, "Payload:\n"+string(in.Payload))
	}
	if len(parts) == 0 {
		return strings.TrimSpace(string(body))
	}
	return strings.Join(parts, "\n\n")
}

func hookTokenOK(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	candidates := []string{
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		r.Header.Get("X-Hook-Token"),
		r.URL.Query().Get("token"),
	}
	for _, c := range candidates {
		if c != "" && subtle.ConstantTimeCompare([]byte(c), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) normalizeChannel(ctx context.Context, ch *core.Channel) error {
	ch.Name = strings.TrimSpace(ch.Name)
	ch.Type = strings.TrimSpace(ch.Type)
	ch.AgentID = strings.TrimSpace(ch.AgentID)
	if ch.Config == nil {
		ch.Config = map[string]string{}
	}
	if ch.Name == "" {
		return fmt.Errorf("channel name is required")
	}
	if !knownPlatformType(ch.Type) {
		return fmt.Errorf("unknown platform type %q", ch.Type)
	}
	if strings.HasPrefix(ch.AgentID, "config:") {
		return fmt.Errorf("channels can only bind console-managed agents")
	}
	newRecord := strings.TrimSpace(ch.ID) == ""
	if newRecord {
		ch.ID = "channel-" + randHex(6)
	}
	now := time.Now()
	if !newRecord {
		if existing, err := s.st.GetChannel(ctx, ch.ID); err == nil && existing != nil {
			ch.CreatedAt = existing.CreatedAt
			// The console round-trips redacted secrets; restore originals.
			for k, v := range ch.Config {
				if v == "<redacted>" {
					if orig, ok := existing.Config[k]; ok {
						ch.Config[k] = orig
					}
				}
			}
		}
	}
	if err := normalizeChannelConfig(ch); err != nil {
		return err
	}
	if ch.Config[core.ChannelConfigMeetingVoice] == "true" && ch.Config[core.ChannelConfigMeetingTTSMode] == core.MeetingTTSModeLocal {
		modelID := ch.Config[core.ChannelConfigMeetingLocalModel]
		if s.ttsModels == nil || !s.ttsModels.IsInstalled(modelID) {
			return fmt.Errorf("local TTS model %q is not downloaded", modelID)
		}
	}
	if ch.AgentID != "" {
		agent, err := s.st.GetAgentInstance(ctx, ch.AgentID)
		if err != nil {
			return err
		}
		if agent == nil {
			return fmt.Errorf("bound Agent %q was not found", ch.AgentID)
		}
	}
	if core.CodexRemoteControlEnabled(*ch) {
		if ch.AgentID == "" {
			return fmt.Errorf("Codex remote control requires a bound Agent")
		}
		agent, err := s.st.GetAgentInstance(ctx, ch.AgentID)
		if err != nil {
			return err
		}
		if agent == nil || (agent.RuntimeID != "codex" && agent.RuntimeID != "codex-app") {
			return fmt.Errorf("Codex remote control requires a Codex Agent")
		}
		allowed := cleanIDList(ch.Config[core.ChannelConfigAllowedUserIDs])
		admins := cleanIDList(ch.Config[core.ChannelConfigAdminUserIDs])
		if allowed == "" && admins == "" {
			return fmt.Errorf("Codex remote control requires at least one allowed or admin user ID")
		}
		ch.Config[core.ChannelConfigAllowedUserIDs] = allowed
		ch.Config[core.ChannelConfigAdminUserIDs] = admins
		maxQueue, err := boundedChannelInt(ch.Config[core.ChannelConfigCodexMaxQueue], core.DefaultCodexMaxQueue, 1, 100)
		if err != nil {
			return fmt.Errorf("invalid codex_max_queue: %w", err)
		}
		timeout, err := boundedChannelInt(ch.Config[core.ChannelConfigCodexTurnTimeout], core.DefaultCodexTurnTimeoutMinutes, 1, 240)
		if err != nil {
			return fmt.Errorf("invalid codex_turn_timeout_minutes: %w", err)
		}
		ch.Config[core.ChannelConfigCodexMaxQueue] = strconv.Itoa(maxQueue)
		ch.Config[core.ChannelConfigCodexTurnTimeout] = strconv.Itoa(timeout)
	}
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = now
	}
	ch.UpdatedAt = now
	return nil
}

func normalizeChannelConfig(ch *core.Channel) error {
	// Approval defaults belong to the bound Agent. Strip the retired channel
	// key whenever a channel is created or saved so old records migrate lazily.
	delete(ch.Config, "approval_mode")
	turnTimeoutRaw := strings.TrimSpace(ch.Config[core.ChannelConfigTurnTimeout])
	if turnTimeoutRaw == "" {
		turnTimeoutRaw = strings.TrimSpace(ch.Config[core.ChannelConfigCodexTurnTimeout])
	}
	turnTimeout, err := boundedChannelInt(turnTimeoutRaw, core.DefaultChannelTurnTimeoutMinutes, 1, 240)
	if err != nil {
		return fmt.Errorf("invalid turn_timeout_minutes: %w", err)
	}
	ch.Config[core.ChannelConfigTurnTimeout] = strconv.Itoa(turnTimeout)
	if ch.Type != "feishu" && ch.Type != "lark" {
		return nil
	}
	scope := strings.TrimSpace(ch.Config[core.ChannelConfigReplyScope])
	if scope == "" {
		scope = core.ReplyScopeDMAndMentions
	}
	switch scope {
	case core.ReplyScopeDMAndMentions, core.ReplyScopeAll, core.ReplyScopeMentionsOnly:
		ch.Config[core.ChannelConfigReplyScope] = scope
	default:
		return fmt.Errorf("invalid reply_scope %q (want dm_and_mentions, all or mentions_only)", scope)
	}

	mode := strings.TrimSpace(ch.Config[core.ChannelConfigReplyMode])
	if mode == "" {
		mode = core.ReplyModeStreamMessage
	}
	switch mode {
	case core.ReplyModeStreamMessage, core.ReplyModeStreamCard:
		ch.Config[core.ChannelConfigReplyMode] = mode
	default:
		return fmt.Errorf("invalid reply_mode %q (want stream_message or stream_card)", mode)
	}

	ack := strings.ToLower(strings.TrimSpace(ch.Config[core.ChannelConfigAckReaction]))
	if ack == "" {
		ack = core.DefaultAckReactionEnabled
	}
	switch ack {
	case "true", "1", "yes", "on":
		ch.Config[core.ChannelConfigAckReaction] = "true"
	case "false", "0", "no", "off":
		ch.Config[core.ChannelConfigAckReaction] = "false"
	default:
		return fmt.Errorf("invalid ack_reaction_enabled %q (want true or false)", ack)
	}

	emojis := cleanCommaList(ch.Config[core.ChannelConfigAckReactionEmojis])
	if emojis == "" {
		emojis = core.DefaultAckReactionEmojis
	}
	ch.Config[core.ChannelConfigAckReactionEmojis] = emojis

	wakeWords, err := core.NormalizeMeetingVoiceWakeWords(ch.Config[core.ChannelConfigMeetingWakeWords])
	if err != nil {
		return fmt.Errorf("invalid meeting_voice_wake_words: %w", err)
	}
	ch.Config[core.ChannelConfigMeetingWakeWords] = wakeWords

	legacyMeetingReply := strings.ToLower(strings.TrimSpace(ch.Config[core.ChannelConfigMeetingReplyMode]))
	if legacyMeetingReply != "" && legacyMeetingReply != core.MeetingReplyModeStream && legacyMeetingReply != core.MeetingReplyModeFinal {
		return fmt.Errorf("invalid meeting_reply_mode %q (want stream or final)", legacyMeetingReply)
	}
	legacyMeetingVoice := strings.ToLower(strings.TrimSpace(ch.Config[core.ChannelConfigMeetingVoice]))
	switch legacyMeetingVoice {
	case "", "true", "1", "yes", "on", "false", "0", "no", "off":
	default:
		return fmt.Errorf("invalid meeting_voice_enabled %q (want true or false)", legacyMeetingVoice)
	}

	responseMode := strings.TrimSpace(ch.Config[core.ChannelConfigMeetingResponseMode])
	if normalized := core.NormalizeMeetingResponseMode(responseMode); normalized != "" {
		expected := core.Channel{Config: map[string]string{}}
		_ = core.ApplyMeetingResponseMode(&expected, normalized)
		legacyReply := strings.ToLower(strings.TrimSpace(ch.Config[core.ChannelConfigMeetingReplyMode]))
		legacyVoice := strings.ToLower(strings.TrimSpace(ch.Config[core.ChannelConfigMeetingVoice]))
		legacyChanged := legacyReply != "" && legacyReply != expected.Config[core.ChannelConfigMeetingReplyMode]
		if legacyVoice != "" {
			legacyVoiceEnabled := legacyVoice == "true" || legacyVoice == "1" || legacyVoice == "yes" || legacyVoice == "on"
			legacyChanged = legacyChanged || strconv.FormatBool(legacyVoiceEnabled) != expected.Config[core.ChannelConfigMeetingVoice]
		}
		if legacyChanged {
			legacy := *ch
			legacy.Config = make(map[string]string, len(ch.Config))
			for key, value := range ch.Config {
				legacy.Config[key] = value
			}
			delete(legacy.Config, core.ChannelConfigMeetingResponseMode)
			responseMode = core.ChannelMeetingResponseMode(legacy)
		}
	}
	if responseMode == "" {
		responseMode = core.ChannelMeetingResponseMode(*ch)
	}
	if err := core.ApplyMeetingResponseMode(ch, responseMode); err != nil {
		return fmt.Errorf("invalid meeting_response_mode %q (want stream_text, final_text, text_voice or voice)", responseMode)
	}

	if ch.Config[core.ChannelConfigMeetingVoice] == "true" {
		ttsMode := strings.ToLower(strings.TrimSpace(ch.Config[core.ChannelConfigMeetingTTSMode]))
		if ttsMode == "" {
			ttsMode = core.DefaultMeetingTTSMode
		}
		if ttsMode != core.MeetingTTSModeAPI && ttsMode != core.MeetingTTSModeLocal {
			return fmt.Errorf("invalid meeting_voice_tts_mode %q (want api or local)", ttsMode)
		}
		ch.Config[core.ChannelConfigMeetingTTSMode] = ttsMode
		if ttsMode == core.MeetingTTSModeLocal {
			modelID := strings.TrimSpace(ch.Config[core.ChannelConfigMeetingLocalModel])
			if modelID == "" {
				modelID = core.DefaultMeetingLocalModel
			}
			model, ok := ttspkg.Lookup(modelID)
			if !ok {
				return fmt.Errorf("unknown local TTS model %q", modelID)
			}
			voiceID := strings.TrimSpace(ch.Config[core.ChannelConfigMeetingLocalVoice])
			if voiceID == "" {
				voiceID = model.Voices[0].ID
			}
			validVoice := false
			for _, voice := range model.Voices {
				if voice.ID == voiceID {
					validVoice = true
					break
				}
			}
			if !validVoice {
				return fmt.Errorf("invalid local TTS voice %q for model %q", voiceID, modelID)
			}
			ch.Config[core.ChannelConfigMeetingLocalModel] = modelID
			ch.Config[core.ChannelConfigMeetingLocalVoice] = voiceID
			return nil
		}
		baseURL := strings.TrimRight(strings.TrimSpace(ch.Config[core.ChannelConfigMeetingTTSBaseURL]), "/")
		if baseURL == "" {
			baseURL = core.DefaultMeetingTTSBaseURL
		}
		parsed, err := url.Parse(baseURL)
		if err != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" ||
			parsed.User != nil ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return fmt.Errorf("invalid meeting_voice_tts_base_url %q", baseURL)
		}
		apiKey := strings.TrimSpace(ch.Config[core.ChannelConfigMeetingTTSAPIKey])
		if apiKey == "" {
			return fmt.Errorf("meeting_voice_tts_api_key is required when meeting voice is enabled")
		}
		model := strings.TrimSpace(ch.Config[core.ChannelConfigMeetingTTSModel])
		if model == "" {
			model = core.DefaultMeetingTTSModel
		}
		voiceName := strings.TrimSpace(ch.Config[core.ChannelConfigMeetingTTSVoice])
		if voiceName == "" {
			voiceName = core.DefaultMeetingTTSVoice
		}
		ch.Config[core.ChannelConfigMeetingTTSBaseURL] = baseURL
		ch.Config[core.ChannelConfigMeetingTTSAPIKey] = apiKey
		ch.Config[core.ChannelConfigMeetingTTSModel] = model
		ch.Config[core.ChannelConfigMeetingTTSVoice] = voiceName
	}
	return nil
}

func cleanCommaList(raw string) string {
	parts := strings.Split(raw, ",")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			cleaned = append(cleaned, item)
		}
	}
	return strings.Join(cleaned, ",")
}

func cleanIDList(raw string) string {
	values := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	seen := map[string]bool{}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			cleaned = append(cleaned, value)
		}
	}
	return strings.Join(cleaned, ",")
}

func boundedChannelInt(raw string, fallback, min, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("must be an integer between %d and %d", min, max)
	}
	return value, nil
}

func (s *Server) normalizeTrigger(ctx context.Context, tr *core.Trigger) error {
	tr.Name = strings.TrimSpace(tr.Name)
	tr.Kind = strings.TrimSpace(tr.Kind)
	tr.CronExpr = strings.TrimSpace(tr.CronExpr)
	tr.Event = strings.TrimSpace(tr.Event)
	tr.SessionMode = strings.TrimSpace(tr.SessionMode)
	if tr.Name == "" {
		return fmt.Errorf("trigger name is required")
	}
	switch tr.Kind {
	case core.TriggerCron:
		if tr.CronExpr == "" {
			return fmt.Errorf("cron expression is required")
		}
		if err := core.ValidateCronExpr(tr.CronExpr); err != nil {
			return fmt.Errorf("invalid cron expression %q: %v", tr.CronExpr, err)
		}
		if strings.TrimSpace(tr.Prompt) == "" {
			return fmt.Errorf("prompt is required for cron triggers")
		}
	case core.TriggerWebhook:
		if tr.Token == "" {
			tr.Token = randHex(16)
		}
	case core.TriggerEvent:
		if tr.Event == "" {
			return fmt.Errorf("event is required for event triggers")
		}
		if tr.ActionType != core.ActionShell && tr.ActionType != core.ActionHTTP {
			return fmt.Errorf("action_type must be shell or http")
		}
		if strings.TrimSpace(tr.ActionTarget) == "" {
			return fmt.Errorf("action_target is required for event triggers")
		}
	default:
		return fmt.Errorf("unknown trigger kind %q (want cron, webhook or event)", tr.Kind)
	}
	switch tr.SessionMode {
	case "", core.SessionModeReuse, core.SessionModeNewPerRun:
	default:
		return fmt.Errorf("invalid session_mode %q (want reuse or new_per_run)", tr.SessionMode)
	}
	newRecord := strings.TrimSpace(tr.ID) == ""
	if newRecord {
		tr.ID = "trigger-" + randHex(6)
	}
	now := time.Now()
	if !newRecord {
		if existing, err := s.st.GetTrigger(ctx, tr.ID); err == nil && existing != nil {
			tr.CreatedAt = existing.CreatedAt
			tr.LastRun = existing.LastRun
			tr.LastStatus = existing.LastStatus
			tr.LastError = existing.LastError
		}
	}
	if tr.CreatedAt.IsZero() {
		tr.CreatedAt = now
	}
	tr.UpdatedAt = now
	return nil
}

func (s *Server) reloadChannels(ctx context.Context) {
	if s.connect == nil {
		return
	}
	if err := s.connect.ReloadChannels(ctx); err != nil {
		s.log.Error("reload channels", "err", err)
	}
}

func (s *Server) reloadTriggers(ctx context.Context) {
	if s.connect == nil {
		return
	}
	if err := s.connect.ReloadTriggers(ctx); err != nil {
		s.log.Error("reload triggers", "err", err)
	}
}

func (s *Server) agentNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	items, err := s.agentInstances(ctx)
	if err != nil {
		return names
	}
	for _, item := range items {
		names[item.ID] = item.Name
	}
	return names
}

func knownPlatformType(typ string) bool {
	for _, name := range core.RegisteredPlatforms() {
		if name == typ {
			return true
		}
	}
	return false
}
