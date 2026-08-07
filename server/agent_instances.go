package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

func (s *Server) handleAgentInstancesList(w http.ResponseWriter, r *http.Request) {
	items, err := s.agentInstances(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleAgentInstanceUpsert(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	a, ok := decodeJSON[core.AgentInstance](w, r)
	if !ok {
		return
	}
	if err := s.normalizeAgentInstance(r.Context(), &a); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.UpsertAgentInstance(r.Context(), &a); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a.ProviderID != "" && a.ProviderTool != "" && s.provider != nil {
		if err := s.provider.Switch(r.Context(), a.ProviderID, a.ProviderTool); err != nil {
			writeErr(w, http.StatusInternalServerError, "agent saved, but provider route failed: " + err.Error())
			return
		}
	}
	if s.connect != nil {
		if err := s.connect.RestartChannelsForAgent(r.Context(), a.ID); err != nil {
			writeErr(w, http.StatusInternalServerError, "agent saved, but bound channels failed to restart: " + err.Error())
			return
		}
	}
	items := []core.AgentInstance{a}
	s.enrichAgentProviders(r.Context(), items)
	writeJSON(w, http.StatusOK, &items[0])
}

func (s *Server) handleAgentInstanceInitialize(w http.ResponseWriter, r *http.Request) {
	if s.workspace == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace initializer unavailable")
		return
	}
	var opts core.WorkspaceInitOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	opts.RuntimeID = strings.TrimSpace(opts.RuntimeID)
	opts.WorkDir = strings.TrimSpace(opts.WorkDir)
	if opts.AgentID == "" {
		opts.AgentID = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if opts.AgentID != "" {
		inst, ok, err := s.findAgentInstance(r.Context(), opts.AgentID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "agent not found")
			return
		}
		if opts.RuntimeID == "" {
			opts.RuntimeID = inst.RuntimeID
		}
		if opts.WorkDir == "" {
			opts.WorkDir = inst.WorkDir
		}
		if len(opts.Skills) == 0 {
			opts.Skills = append([]string(nil), inst.Skills...)
		}
		if len(opts.MCPServers) == 0 {
			opts.MCPServers = append([]string(nil), inst.MCPServers...)
		}
	}
	res, err := s.workspace.InitializeWorkspace(r.Context(), opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAgentInstanceDelete(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if strings.HasPrefix(id, "config:") {
		writeErr(w, http.StatusBadRequest, "config-managed agents must be edited in config.toml")
		return
	}
	if err := s.st.DeleteAgentInstance(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.connect != nil {
		if err := s.connect.RestartChannelsForAgent(r.Context(), id); err != nil {
			writeErr(w, http.StatusInternalServerError, "agent deleted, but bound channels failed to restart: " + err.Error())
			return
		}
	}
	writeOK(w)
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

func (s *Server) findAgentInstance(ctx context.Context, id string) (core.AgentInstance, bool, error) {
	items, err := s.agentInstances(ctx)
	if err != nil {
		return core.AgentInstance{}, false, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return core.AgentInstance{}, false, nil
}

func (s *Server) normalizeAgentInstance(ctx context.Context, a *core.AgentInstance) error {
	a.Name = strings.TrimSpace(a.Name)
	a.RuntimeID = strings.TrimSpace(a.RuntimeID)
	a.WorkDir = strings.TrimSpace(a.WorkDir)
	a.ProviderTool = strings.TrimSpace(a.ProviderTool)
	a.ProviderID = strings.TrimSpace(a.ProviderID)
	a.DefaultModel = strings.TrimSpace(a.DefaultModel)
	a.DefaultReasoningEffort = strings.TrimSpace(a.DefaultReasoningEffort)
	a.DefaultServiceTier = strings.TrimSpace(a.DefaultServiceTier)
	a.DefaultApprovalMode = strings.TrimSpace(a.DefaultApprovalMode)
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
	creatingRecord := newRecord
	if !newRecord && s.st != nil {
		existing, err := s.st.GetAgentInstance(ctx, a.ID)
		if err != nil {
			return fmt.Errorf("load existing agent: %w", err)
		}
		creatingRecord = existing == nil
	}
	if creatingRecord && !agentRuntimeAvailable(a.RuntimeID) {
		return fmt.Errorf("agent runtime %q is not installed on this machine", a.RuntimeID)
	}
	if a.DefaultApprovalMode != "" && !core.ApprovalModeSupported(a.RuntimeID, a.DefaultApprovalMode) {
		return fmt.Errorf("approval mode %q is not supported by %s", a.DefaultApprovalMode, a.RuntimeID)
	}
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
	if err := s.validateAgentDefaultRuntimeSettings(ctx, a); err != nil {
		return err
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
			DefaultModel:    p.DefaultModel,
			MemoryScope:     "project:" + p.Name,
			Env:             redactStringMap(p.Env),
			ChannelBindings: channels,
			Enabled:         true,
			Source:          "config.toml",
		})
	}
	return out
}

func (s *Server) validateAgentDefaultRuntimeSettings(ctx context.Context, a *core.AgentInstance) error {
	if a.DefaultModel == "" && a.DefaultReasoningEffort == "" && a.DefaultServiceTier == "" && a.DefaultApprovalMode == "" {
		return nil
	}
	p, err := s.agentProvider(ctx, a)
	if err != nil {
		return err
	}
	if p == nil {
		// Local Codex login discovers its catalog only after app-server starts.
		// Let that runtime validate the values instead of rejecting the Agent
		// record before it can reach the signed-in account.
		return nil
	}
	if a.DefaultModel != "" {
		models := core.ProviderModelOptions(p)
		if len(models) == 0 {
			return fmt.Errorf("provider %q has no selectable models", p.ID)
		}
		if !containsRuntimeValue(models, a.DefaultModel) {
			return fmt.Errorf("default model %q is not supported by provider %q", a.DefaultModel, p.ID)
		}
	}
	if a.DefaultReasoningEffort != "" && len(p.Meta.SupportedReasoningEfforts) > 0 &&
		!containsRuntimeValue(p.Meta.SupportedReasoningEfforts, a.DefaultReasoningEffort) {
		return fmt.Errorf("default reasoning effort %q is not supported by provider %q", a.DefaultReasoningEffort, p.ID)
	}
	if a.DefaultServiceTier != "" && len(p.Meta.SupportedServiceTiers) > 0 &&
		!containsRuntimeValue(p.Meta.SupportedServiceTiers, a.DefaultServiceTier) {
		return fmt.Errorf("default service tier %q is not supported by provider %q", a.DefaultServiceTier, p.ID)
	}
	return nil
}

func containsRuntimeValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Server) agentProvider(ctx context.Context, a *core.AgentInstance) (*core.Provider, error) {
	if s.provider == nil {
		return nil, nil
	}
	if a.ProviderID != "" {
		return s.provider.Get(ctx, a.ProviderID)
	}
	tool := a.ProviderTool
	if tool == "" {
		tool = a.RuntimeID
	}
	routes, err := s.provider.ActiveRoutes(ctx)
	if err != nil {
		return nil, err
	}
	want := core.NormalizeProviderTool(tool)
	for _, route := range routes {
		if route.Tool == tool || core.NormalizeProviderTool(route.Tool) == want {
			return s.provider.Get(ctx, route.ProviderID)
		}
	}
	return nil, nil
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

func availableAgentRuntimes() []string {
	registered := core.RegisteredAgents()
	available := make([]string, 0, len(registered))
	for _, id := range registered {
		if agentRuntimeAvailable(id) {
			available = append(available, id)
		}
	}
	return available
}

func agentRuntimeAvailable(id string) bool {
	if _, catalogued := framework.Lookup(id); catalogued {
		return framework.IsInstalled(id)
	}
	// Third-party adapters do not have a built-in framework specification. A
	// successful registration is their declaration that they are runnable.
	return core.HasAgent(id)
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
