package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

func (s *Server) handleAgentInstancesList(w http.ResponseWriter, r *http.Request) {
	items, err := s.agentInstances(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleAgentInstanceUpsert(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "store unavailable"})
		return
	}
	var a core.AgentInstance
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.normalizeAgentInstance(r.Context(), &a); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.st.UpsertAgentInstance(r.Context(), &a); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if a.ProviderID != "" && a.ProviderTool != "" && s.provider != nil {
		if err := s.provider.Switch(r.Context(), a.ProviderID, a.ProviderTool); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "agent saved, but provider route failed: " + err.Error()})
			return
		}
	}
	items := []core.AgentInstance{a}
	s.enrichAgentProviders(r.Context(), items)
	writeJSON(w, http.StatusOK, &items[0])
}

func (s *Server) handleAgentInstanceDelete(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "store unavailable"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if strings.HasPrefix(id, "config:") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "config-managed agents must be edited in config.toml"})
		return
	}
	if err := s.st.DeleteAgentInstance(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) agentInstances(ctx context.Context) ([]core.AgentInstance, error) {
	var items []core.AgentInstance
	if s.st != nil {
		stored, err := s.st.ListAgentInstances(ctx)
		if err != nil {
			return nil, err
		}
		items = append(items, stored...)
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.ID] = true
	}
	for _, item := range s.configAgentInstances() {
		if !seen[item.ID] {
			items = append(items, item)
		}
	}
	s.enrichAgentProviders(ctx, items)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Enabled != items[j].Enabled {
			return items[i].Enabled
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

func (s *Server) normalizeAgentInstance(ctx context.Context, a *core.AgentInstance) error {
	a.Name = strings.TrimSpace(a.Name)
	a.RuntimeID = strings.TrimSpace(a.RuntimeID)
	a.WorkDir = strings.TrimSpace(a.WorkDir)
	a.ProviderTool = strings.TrimSpace(a.ProviderTool)
	a.ProviderID = strings.TrimSpace(a.ProviderID)
	a.MemoryScope = strings.TrimSpace(a.MemoryScope)
	if a.Name == "" {
		return fmt.Errorf("agent name is required")
	}
	if a.RuntimeID == "" {
		return fmt.Errorf("runtime is required")
	}
	if !knownAgentRuntime(a.RuntimeID) {
		return fmt.Errorf("unknown agent runtime %q", a.RuntimeID)
	}
	newRecord := strings.TrimSpace(a.ID) == ""
	if newRecord {
		a.ID = "agent-" + randHex(6)
	}
	if strings.HasPrefix(a.ID, "config:") {
		return fmt.Errorf("config-managed agent ids are read-only")
	}
	now := time.Now()
	if !newRecord && a.CreatedAt.IsZero() && s.st != nil {
		if existing, err := s.st.GetAgentInstance(ctx, a.ID); err == nil && existing != nil {
			a.CreatedAt = existing.CreatedAt
		}
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	if a.ProviderTool == "" {
		a.ProviderTool = a.RuntimeID
	}
	if a.MemoryScope == "" {
		a.MemoryScope = "agent:" + a.ID
	}
	if a.Source == "config.toml" {
		a.Source = "manual"
	}
	if a.Source == "" {
		if newRecord {
			a.Source = "manual"
		} else {
			a.Source = "console"
		}
	}
	if newRecord && len(a.ChannelBindings) == 0 {
		a.ChannelBindings = []core.AgentChannelBinding{}
	}
	return nil
}

func (s *Server) configAgentInstances() []core.AgentInstance {
	if s.cfg == nil {
		return nil
	}
	out := make([]core.AgentInstance, 0, len(s.cfg.Projects))
	for projectIndex, p := range s.cfg.Projects {
		id := "config:" + slugID(p.Name) + "-" + strconv.Itoa(projectIndex+1)
		channels := make([]core.AgentChannelBinding, 0, len(p.Platforms))
		for i, raw := range p.Platforms {
			typ, _ := raw["type"].(string)
			if typ == "" {
				typ = "platform"
			}
			channels = append(channels, core.AgentChannelBinding{
				ID:     id + ":channel:" + strconv.Itoa(i),
				Type:   typ,
				Name:   typ,
				Status: "configured",
				Config: redactAnyMap(raw),
			})
		}
		out = append(out, core.AgentInstance{
			ID:              id,
			Name:            p.Name,
			RuntimeID:       p.Agent,
			WorkDir:         p.WorkDir,
			SystemPrompt:    p.SystemPrompt,
			ProviderTool:    p.Agent,
			MemoryScope:     "project:" + p.Name,
			Env:             redactStringMap(p.Env),
			ChannelBindings: channels,
			Enabled:         true,
			Source:          "config.toml",
		})
	}
	return out
}

func (s *Server) enrichAgentProviders(ctx context.Context, items []core.AgentInstance) {
	if s.provider == nil {
		return
	}
	providers, err := s.provider.List(ctx)
	if err != nil {
		return
	}
	names := map[string]string{}
	for _, p := range providers {
		names[p.ID] = p.Name
	}
	for i := range items {
		if items[i].ProviderID != "" {
			items[i].ProviderName = names[items[i].ProviderID]
		}
	}
}

func knownAgentRuntime(id string) bool {
	for _, name := range core.RegisteredAgents() {
		if name == id {
			return true
		}
	}
	return false
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func slugID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return out
}

func redactStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		if isSecretish(k) {
			out[k] = "<redacted>"
		} else {
			out[k] = v
		}
	}
	return out
}

func redactAnyMap(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		if isSecretish(k) {
			out[k] = "<redacted>"
			continue
		}
		switch x := v.(type) {
		case string:
			out[k] = x
		case fmt.Stringer:
			out[k] = x.String()
		default:
			b, err := json.Marshal(x)
			if err == nil {
				out[k] = string(b)
			}
		}
	}
	return out
}

func isSecretish(key string) bool {
	key = strings.ToLower(key)
	for _, needle := range []string{"secret", "token", "password", "api_key", "apikey", "app_secret", "key"} {
		if strings.Contains(key, needle) {
			return true
		}
	}
	return false
}
