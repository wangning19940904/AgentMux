package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// SetConnect attaches the channels/triggers runtime. Nil keeps the CRUD
// endpoints working in persist-only mode (no live attach/scheduling).
func (s *Server) SetConnect(svc *core.ConnectService) { s.connect = svc }

// apiChannel is a channel plus live status and display enrichment.
type apiChannel struct {
	core.Channel
	AgentName string `json:"agent_name,omitempty"`
	State     string `json:"state,omitempty"`
	Error     string `json:"error,omitempty"`
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
	channels, err := s.st.ListChannels(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	statuses := map[string]core.ChannelStatus{}
	if s.connect != nil {
		for _, st := range s.connect.ChannelStatuses() {
			statuses[st.ChannelID] = st
		}
	}
	agentNames := s.agentNames(r.Context())
	out := make([]apiChannel, 0, len(channels))
	for _, ch := range channels {
		ch.Config = redactStringMap(ch.Config)
		item := apiChannel{Channel: ch, AgentName: agentNames[ch.AgentID]}
		if st, ok := statuses[ch.ID]; ok {
			item.State = st.State
			item.Error = st.Error
		} else if ch.Enabled {
			item.State = "pending"
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChannelUpsert(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "store unavailable"})
		return
	}
	var ch core.Channel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.normalizeChannel(r.Context(), &ch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.st.UpsertChannel(r.Context(), &ch); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.reloadChannels(r.Context())
	ch.Config = redactStringMap(ch.Config)
	writeJSON(w, http.StatusOK, &ch)
}

func (s *Server) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "store unavailable"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if err := s.st.DeleteChannel(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.reloadChannels(r.Context())
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChannelRestart(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if s.connect == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "connect runtime not running (start the daemon)"})
		return
	}
	if err := s.connect.RestartChannel(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTriggersList(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []apiTrigger{})
		return
	}
	triggers, err := s.st.ListTriggers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "store unavailable"})
		return
	}
	var tr core.Trigger
	if err := json.NewDecoder(r.Body).Decode(&tr); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.normalizeTrigger(r.Context(), &tr); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.st.UpsertTrigger(r.Context(), &tr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.reloadTriggers(r.Context())
	writeJSON(w, http.StatusOK, &tr)
}

func (s *Server) handleTriggerDelete(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "store unavailable"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if err := s.st.DeleteTrigger(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.reloadTriggers(r.Context())
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTriggerRun(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if s.connect == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "connect runtime not running (start the daemon)"})
		return
	}
	if s.st != nil {
		tr, err := s.st.GetTrigger(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if tr == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "trigger not found"})
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
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = now
	}
	ch.UpdatedAt = now
	return nil
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
