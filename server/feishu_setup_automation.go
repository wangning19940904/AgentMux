package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	feishuAutomationTTL      = 10 * time.Minute
	feishuLongConnectionMode = 4
)

var (
	feishuAutomationAccountsOrigin = "https://accounts.feishu.cn"
	feishuAutomationAskOrigin      = "https://ask.feishu.cn"
	feishuAutomationOpenOrigin     = "https://open.feishu.cn"
)

var (
	feishuAppIDPattern = regexp.MustCompile(`^cli_[A-Za-z0-9]+$`)
	csrfPatterns       = []*regexp.Regexp{
		regexp.MustCompile(`window\.csrfToken\s*=\s*["']([^"']+)["']`),
		regexp.MustCompile(`csrfToken\s*:\s*["']([^"']+)["']`),
	}
)

var agentMuxFeishuScopes = []string{
	"contact:user.base:readonly",
	"im:message", "im:message:readonly", "im:message:send_as_bot",
	"im:message.group_at_msg:readonly", "im:message.p2p_msg:readonly", "im:resource",
	"im:message.reactions:write_only", "cardkit:card:write",
	"vc:meeting.bot.join:write", "vc:meeting.bot.realtime:write", "vc:meeting.meetingevent:read", "vc:meeting.message:write",
}

var agentMuxFeishuEvents = []string{
	"im.message.receive_v1",
	"vc.bot.meeting_invited_v1", "vc.bot.meeting_activity_v1", "vc.bot.meeting_ended_v1",
}

type feishuAutomationSession struct {
	mu        sync.Mutex
	client    *http.Client
	flowKey   string
	token     string
	ready     bool
	createdAt time.Time
}

func (s *Server) handleFeishuAutomationBegin(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "Feishu Open Platform automation is available only from the local console")
		return
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	client := &http.Client{Timeout: 20 * time.Second, Jar: jar}
	endpoint := fmt.Sprintf("%s/accounts/qrlogin/init?_r%d=%d", feishuAutomationAccountsOrigin, 10000+time.Now().Nanosecond()%80000, time.Now().UnixMilli())
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader([]byte(`{"biz_type":null,"redirect_uri":"https://ask.feishu.cn/"}`)))
	applyFeishuAutomationHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Feishu QR init: "+err.Error())
		return
	}
	defer resp.Body.Close()
	payload, err := decodeLimitedJSON(resp.Body)
	if err != nil || resp.StatusCode >= 300 {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("Feishu QR init returned %s", resp.Status))
		return
	}
	flowKey := strings.TrimSpace(resp.Header.Get("X-Flow-Key"))
	token := nestedString(payload, "data", "step_info", "token")
	if flowKey == "" || token == "" {
		writeErr(w, http.StatusBadGateway, "Feishu QR init returned incomplete session data")
		return
	}
	sessionID := "feishu-setup-" + randHex(12)
	session := &feishuAutomationSession{client: client, flowKey: flowKey, token: token, createdAt: time.Now().UTC()}
	s.feishuAutomationMu.Lock()
	s.pruneFeishuAutomationSessionsLocked()
	s.feishuAutomations[sessionID] = session
	s.feishuAutomationMu.Unlock()
	qrPayload, _ := json.Marshal(map[string]any{"qrlogin": map[string]string{"token": token}})
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "qr_payload": string(qrPayload), "expires_in": int(feishuAutomationTTL.Seconds())})
}

func (s *Server) handleFeishuAutomationPoll(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "Feishu Open Platform automation is available only from the local console")
		return
	}
	var input struct {
		SessionID string `json:"session_id"`
	}
	if !decodeJSONInto(w, r, &input) {
		return
	}
	session := s.feishuAutomationSession(strings.TrimSpace(input.SessionID))
	if session == nil {
		writeErr(w, http.StatusNotFound, "Feishu automation session expired")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.ready {
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed"})
		return
	}
	endpoint := fmt.Sprintf("%s/accounts/qrlogin/polling?_r%d=%d", feishuAutomationAccountsOrigin, 10000+time.Now().Nanosecond()%80000, time.Now().UnixMilli())
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader([]byte(`{"biz_type":null}`)))
	applyFeishuAutomationHeaders(req)
	req.Header.Set("X-Flow-Key", session.flowKey)
	resp, err := session.client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Feishu QR poll: "+err.Error())
		return
	}
	payload, decodeErr := decodeLimitedJSON(resp.Body)
	resp.Body.Close()
	if decodeErr != nil || resp.StatusCode >= 300 {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("Feishu QR poll returned %s", resp.Status))
		return
	}
	status := nestedInt(payload, "data", "step_info", "status")
	nextStep := nestedString(payload, "data", "next_step")
	if nextStep == "enter_app" {
		if crossLogin := nestedString(payload, "data", "step_info", "cross_login_uri"); crossLogin != "" {
			if _, err := feishuAutomationGet(r.Context(), session.client, crossLogin); err != nil {
				writeErr(w, http.StatusBadGateway, "Feishu cross login: "+err.Error())
				return
			}
		}
		if _, err := feishuAutomationGet(r.Context(), session.client, feishuAutomationAskOrigin+"/"); err != nil {
			writeErr(w, http.StatusBadGateway, "Feishu session validation: "+err.Error())
			return
		}
		session.ready = true
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed"})
		return
	}
	switch status {
	case 2:
		writeJSON(w, http.StatusOK, map[string]any{"status": "scanned"})
	case 5:
		writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	}
}

func (s *Server) handleFeishuAutomationConfigure(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "Feishu Open Platform automation is available only from the local console")
		return
	}
	var input struct {
		SessionID  string `json:"session_id"`
		AppID      string `json:"app_id"`
		Publish    bool   `json:"publish"`
		Visibility string `json:"visibility"`
	}
	if !decodeJSONInto(w, r, &input) {
		return
	}
	input.SessionID, input.AppID = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.AppID)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if !feishuAppIDPattern.MatchString(input.AppID) {
		writeErr(w, http.StatusBadRequest, "invalid Feishu app_id")
		return
	}
	if input.Publish && input.Visibility != "owner" && input.Visibility != "all" {
		writeErr(w, http.StatusBadRequest, "publishing requires visibility owner or all")
		return
	}
	session := s.feishuAutomationSession(input.SessionID)
	if session == nil {
		writeErr(w, http.StatusNotFound, "Feishu automation session expired")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.ready {
		writeErr(w, http.StatusConflict, "Feishu QR login is not complete")
		return
	}

	result, err := configureFeishuOpenPlatform(r.Context(), session.client, input.AppID, input.Publish, input.Visibility)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.feishuAutomationMu.Lock()
	delete(s.feishuAutomations, input.SessionID)
	s.feishuAutomationMu.Unlock()
	writeJSON(w, http.StatusOK, result)
}

func configureFeishuOpenPlatform(ctx context.Context, client *http.Client, appID string, publish bool, visibility string) (map[string]any, error) {
	html, finalURL, err := feishuAutomationGetWithURL(ctx, client, feishuAutomationOpenOrigin+"/app/"+appID+"/auth")
	if err != nil {
		return nil, fmt.Errorf("read Feishu Open Platform: %w", err)
	}
	csrf := extractFeishuCSRF(html)
	if csrf == "" {
		return nil, fmt.Errorf("Feishu Open Platform page did not expose a CSRF token; finish Open Platform login in the browser and retry")
	}
	originURL, _ := url.Parse(finalURL)
	origin := originURL.Scheme + "://" + originURL.Host
	referer := origin + "/app/" + appID
	post := func(path string, body any) (any, error) {
		return feishuOpenPlatformPost(ctx, client, origin, referer, csrf, path, body)
	}

	scopePayload, err := post("/developers/v1/scope/all/"+appID, nil)
	if err != nil {
		return nil, fmt.Errorf("read Feishu scope catalog: %w", err)
	}
	catalog := map[string]string{}
	collectFeishuScopeEntries(scopePayload, catalog)
	var scopeIDs, missingScopes []string
	for _, name := range agentMuxFeishuScopes {
		if id := catalog[name]; id != "" {
			scopeIDs = append(scopeIDs, id)
		} else {
			missingScopes = append(missingScopes, name)
		}
	}
	if len(scopeIDs) > 0 {
		if _, err := post("/developers/v1/scope/update/"+appID, map[string]any{
			"clientId": appID, "appScopeIDs": uniqueSetupStrings(scopeIDs), "userScopeIDs": []string{},
			"scopeIds": []string{}, "operation": "add", "isDeveloperPanel": true,
		}); err != nil {
			return nil, fmt.Errorf("configure Feishu scopes: %w", err)
		}
	}
	if _, err := post("/developers/v1/robot/switch/"+appID, map[string]any{"clientId": appID, "enable": true}); err != nil {
		return nil, fmt.Errorf("enable Feishu bot capability: %w", err)
	}
	if _, err := post("/developers/v1/event/switch/"+appID, map[string]any{"clientId": appID, "eventMode": feishuLongConnectionMode}); err != nil {
		return nil, fmt.Errorf("enable Feishu long connection events: %w", err)
	}
	if _, err := post("/developers/v1/event/update/"+appID, map[string]any{
		"clientId": appID, "operation": "add", "events": []string{}, "appEvents": agentMuxFeishuEvents,
		"userEvents": []string{}, "eventMode": feishuLongConnectionMode,
	}); err != nil {
		return nil, fmt.Errorf("subscribe Feishu events: %w", err)
	}
	_, _ = post("/developers/v1/callback/switch/"+appID, map[string]any{"clientId": appID, "callbackMode": feishuLongConnectionMode})
	if _, err := post("/developers/v1/callback/update/"+appID, map[string]any{
		"clientId": appID, "operation": "add", "callbacks": []string{"card.action.trigger"}, "callbackMode": feishuLongConnectionMode,
	}); err != nil {
		return nil, fmt.Errorf("subscribe Feishu card callback: %w", err)
	}

	eventState, err := post("/developers/v1/event/"+appID, map[string]any{"needEventDetail": true})
	if err != nil {
		return nil, fmt.Errorf("verify Feishu events: %w", err)
	}
	callbackState, err := post("/developers/v1/callback/"+appID, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("verify Feishu callbacks: %w", err)
	}
	eventIDs := collectFeishuEventIDs(eventState)
	callbackIDs := collectFeishuEventIDs(callbackState)
	if !containsSetupString(eventIDs, "im.message.receive_v1") || !containsSetupString(callbackIDs, "card.action.trigger") {
		return nil, fmt.Errorf("Feishu core event/callback verification failed after update")
	}

	versionID := ""
	if publish {
		userID := extractFeishuOpenPlatformUserID(html)
		if visibility == "owner" && userID == "" {
			return nil, fmt.Errorf("cannot publish owner-only version because Open Platform user identity was not found")
		}
		versions, err := post("/developers/v1/app_version/list/"+appID, map[string]any{})
		if err != nil {
			return nil, fmt.Errorf("list Feishu app versions: %w", err)
		}
		members := []string{}
		isAll := 1
		if visibility == "owner" {
			members, isAll = []string{userID}, 0
		}
		created, err := post("/developers/v1/app_version/create/"+appID, map[string]any{
			"appVersion": nextFeishuVersion(versions), "mobileDefaultAbility": "bot", "pcDefaultAbility": "bot",
			"changeLog":           "Enable AgentMux messaging, cards, and long-connection events.",
			"visibleSuggest":      map[string]any{"departments": []string{}, "members": members, "groups": []string{}, "isAll": isAll},
			"blackVisibleSuggest": map[string]any{"departments": []string{}, "members": []string{}, "groups": []string{}, "isAll": 0},
		})
		if err != nil {
			return nil, fmt.Errorf("create Feishu app version: %w", err)
		}
		versionID = findSetupStringByKey(created, map[string]bool{"versionId": true, "version_id": true})
		if versionID == "" {
			return nil, fmt.Errorf("Feishu app version creation returned no version id")
		}
		if _, err := post("/developers/v1/publish/commit/"+appID+"/"+versionID, map[string]any{"clientId": appID}); err != nil {
			return nil, fmt.Errorf("publish Feishu app version: %w", err)
		}
	}

	return map[string]any{
		"ok": true, "app_id": appID, "scope_count": len(scopeIDs), "missing_scopes": missingScopes,
		"events": eventIDs, "callbacks": callbackIDs, "published": publish, "version_id": versionID,
	}, nil
}

func applyFeishuAutomationHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Id", "12")
	req.Header.Set("X-Api-Version", "1.0.28")
	req.Header.Set("X-Locale", "zh-CN")
	req.Header.Set("X-Terminal-Type", "2")
	req.Header.Set("X-Device-Info", "device_id=0;device_name=Chrome;device_os=Mac;device_model=Chrome;channel=Release;package_name=feishu;tt_app_id=1658")
}

func (s *Server) feishuAutomationSession(id string) *feishuAutomationSession {
	s.feishuAutomationMu.Lock()
	defer s.feishuAutomationMu.Unlock()
	s.pruneFeishuAutomationSessionsLocked()
	return s.feishuAutomations[id]
}

func (s *Server) pruneFeishuAutomationSessionsLocked() {
	cutoff := time.Now().Add(-feishuAutomationTTL)
	for id, session := range s.feishuAutomations {
		if session.createdAt.Before(cutoff) {
			delete(s.feishuAutomations, id)
		}
	}
}

func feishuAutomationGet(ctx context.Context, client *http.Client, target string) (string, error) {
	body, _, err := feishuAutomationGetWithURL(ctx, client, target)
	return body, err
}

func feishuAutomationGetWithURL(ctx context.Context, client *http.Client, target string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode >= 300 {
		return "", resp.Request.URL.String(), fmt.Errorf("HTTP %s", resp.Status)
	}
	return string(body), resp.Request.URL.String(), nil
}

func feishuOpenPlatformPost(ctx context.Context, client *http.Client, origin, referer, csrf, path string, body any) (any, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", referer)
	req.Header.Set("X-CSRF-Token", csrf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, decodeErr := decodeLimitedJSON(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s for %s", resp.Status, path)
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	if record, ok := payload.(map[string]any); ok {
		if code, exists := record["code"].(float64); exists && code != 0 {
			return nil, fmt.Errorf("Feishu code=%d: %v", int(code), record["msg"])
		}
	}
	return payload, nil
}

func decodeLimitedJSON(reader io.Reader) (any, error) {
	var payload any
	err := json.NewDecoder(io.LimitReader(reader, 4<<20)).Decode(&payload)
	return payload, err
}

func nestedString(value any, path ...string) string {
	current := value
	for _, key := range path {
		record, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = record[key]
	}
	result, _ := current.(string)
	return strings.TrimSpace(result)
}

func nestedInt(value any, path ...string) int {
	current := value
	for _, key := range path {
		record, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = record[key]
	}
	result, _ := current.(float64)
	return int(result)
}

func extractFeishuCSRF(html string) string {
	for _, pattern := range csrfPatterns {
		if match := pattern.FindStringSubmatch(html); len(match) > 1 {
			return match[1]
		}
	}
	return ""
}

func extractFeishuOpenPlatformUserID(html string) string {
	marker := "window.user"
	start := strings.Index(html, marker)
	if start < 0 {
		return ""
	}
	brace := strings.Index(html[start:], "{")
	if brace < 0 {
		return ""
	}
	object := balancedJSONObject(html, start+brace)
	if object == "" {
		return ""
	}
	var user map[string]any
	if json.Unmarshal([]byte(object), &user) != nil {
		return ""
	}
	for _, key := range []string{"id", "userId", "user_id"} {
		if value, ok := user[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func balancedJSONObject(input string, start int) string {
	depth, inString, escaped := 0, false, false
	for index := start; index < len(input); index++ {
		char := input[index]
		if inString {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
		} else if char == '{' {
			depth++
		} else if char == '}' {
			depth--
			if depth == 0 {
				return input[start : index+1]
			}
		}
	}
	return ""
}

func collectFeishuScopeEntries(value any, out map[string]string) {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			collectFeishuScopeEntries(child, out)
		}
	case map[string]any:
		name := firstSetupString(typed, "scope_name", "scopeName", "name", "key", "scopeKey")
		id := firstSetupString(typed, "id", "scope_id", "scopeId", "scopeID")
		if name != "" && id != "" && out[name] == "" {
			out[name] = id
		}
		for _, child := range typed {
			collectFeishuScopeEntries(child, out)
		}
	}
}

func collectFeishuEventIDs(value any) []string {
	var out []string
	var walk func(any, string)
	walk = func(current any, parent string) {
		switch typed := current.(type) {
		case []any:
			for _, child := range typed {
				if text, ok := child.(string); ok && (strings.Contains(strings.ToLower(parent), "event") || strings.Contains(strings.ToLower(parent), "callback")) {
					out = append(out, text)
				} else {
					walk(child, parent)
				}
			}
		case map[string]any:
			if id := firstSetupString(typed, "id"); id != "" && (strings.Contains(strings.ToLower(parent), "event") || strings.Contains(strings.ToLower(parent), "callback") || strings.Contains(id, ".")) {
				out = append(out, id)
			}
			for key, child := range typed {
				walk(child, key)
			}
		}
	}
	walk(value, "")
	return uniqueSetupStrings(out)
}

func firstSetupString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func uniqueSetupStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func containsSetupString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func nextFeishuVersion(payload any) string {
	var versions [][3]int
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			if raw, ok := typed["appVersion"].(string); ok {
				parts := strings.Split(raw, ".")
				if len(parts) == 3 {
					var version [3]int
					valid := true
					for index, part := range parts {
						parsed, err := strconv.Atoi(part)
						if err != nil {
							valid = false
						}
						version[index] = parsed
					}
					if valid {
						versions = append(versions, version)
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(payload)
	if len(versions) == 0 {
		return "0.0.1"
	}
	sort.Slice(versions, func(i, j int) bool {
		for part := 0; part < 3; part++ {
			if versions[i][part] != versions[j][part] {
				return versions[i][part] > versions[j][part]
			}
		}
		return false
	})
	latest := versions[0]
	return fmt.Sprintf("%d.%d.%d", latest[0], latest[1], latest[2]+1)
}

func findSetupStringByKey(value any, keys map[string]bool) string {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if found := findSetupStringByKey(child, keys); found != "" {
				return found
			}
		}
	case map[string]any:
		for key, child := range typed {
			if keys[key] {
				if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
			if found := findSetupStringByKey(child, keys); found != "" {
				return found
			}
		}
	}
	return ""
}
