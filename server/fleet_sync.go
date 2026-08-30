package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	providerpkg "github.com/wangning19940904/AgentMux/provider"
	skillpkg "github.com/wangning19940904/AgentMux/skills"
	"github.com/wangning19940904/AgentMux/store"
)

const (
	fleetSyncPlanTTL        = 15 * time.Minute
	fleetSyncMaxBodyBytes   = 64 << 20
	fleetSyncMaxSkillBytes  = 16 << 20
	fleetSyncMaxBundleBytes = 48 << 20
)

type fleetSyncSkill struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	Files       map[string]string `json:"files"`
}

type fleetSyncManifest struct {
	Version            int                  `json:"version"`
	SourceHome         string               `json:"source_home,omitempty"`
	Agents             []core.AgentInstance `json:"agents"`
	Providers          []*core.Provider     `json:"providers"`
	ProviderKeys       map[string]string    `json:"provider_keys,omitempty"`
	Routes             []core.ProviderRoute `json:"routes"`
	Channels           []core.Channel       `json:"channels"`
	Triggers           []core.Trigger       `json:"triggers"`
	MCPServers         []core.MCPServer     `json:"mcp_servers"`
	Skills             []fleetSyncSkill     `json:"skills"`
	GuardPolicies      []core.GuardPolicy   `json:"guard_policies"`
	Memory             []*core.MemoryEntry  `json:"memory"`
	TenantNames        map[string]string    `json:"tenant_names,omitempty"`
	Grants             []core.ResourceGrant `json:"grants,omitempty"`
	CredentialsOmitted []string             `json:"credentials_omitted,omitempty"`
}

type fleetSyncPathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type fleetSyncResourceResult struct {
	Type               string `json:"type"`
	Key                string `json:"key"`
	Name               string `json:"name"`
	Action             string `json:"action"`
	Reason             string `json:"reason,omitempty"`
	CredentialsMissing bool   `json:"credentials_missing,omitempty"`
}

type fleetSyncInspection struct {
	HomeDir   string                    `json:"home_dir,omitempty"`
	Resources []fleetSyncResourceResult `json:"resources"`
	Warnings  []string                  `json:"warnings,omitempty"`
}

type fleetSyncInspectRequest struct {
	Manifest           fleetSyncManifest      `json:"manifest"`
	PathMappings       []fleetSyncPathMapping `json:"path_mappings,omitempty"`
	PreserveActivation bool                   `json:"preserve_activation,omitempty"`
}

type fleetSyncPreviewRequest struct {
	SourceTargetID     string                            `json:"source_target_id"`
	DestinationIDs     []string                          `json:"destination_target_ids"`
	Categories         []string                          `json:"categories,omitempty"`
	ProviderIDs        []string                          `json:"provider_ids,omitempty"`
	IncludeCredentials bool                              `json:"include_credentials,omitempty"`
	PreserveActivation bool                              `json:"preserve_activation,omitempty"`
	PathMappings       map[string][]fleetSyncPathMapping `json:"path_mappings,omitempty"`
}

type fleetSyncDestinationPreview struct {
	Target       machineTarget          `json:"target"`
	Inspection   fleetSyncInspection    `json:"inspection"`
	PathMappings []fleetSyncPathMapping `json:"path_mappings,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

type fleetSyncPreviewResponse struct {
	PlanID       string                        `json:"plan_id"`
	ExpiresAt    time.Time                     `json:"expires_at"`
	Source       machineTarget                 `json:"source"`
	Destinations []fleetSyncDestinationPreview `json:"destinations"`
}

type fleetSyncPlan struct {
	ID                 string
	ExpiresAt          time.Time
	Source             machineTarget
	Destinations       []machineTarget
	Categories         []string
	ProviderIDs        []string
	IncludeCredentials bool
	PreserveActivation bool
	Mappings           map[string][]fleetSyncPathMapping
	SourceDigest       string
}

type fleetSyncApplyRequest struct {
	PlanID string `json:"plan_id"`
}

type fleetSyncApplyTarget struct {
	Target     machineTarget       `json:"target"`
	Inspection fleetSyncInspection `json:"inspection"`
	Error      string              `json:"error,omitempty"`
}

type fleetSyncApplyResponse struct {
	PlanID  string                 `json:"plan_id"`
	Targets []fleetSyncApplyTarget `json:"targets"`
}

func (s *Server) handleFleetSyncCapabilities(w http.ResponseWriter, _ *http.Request) {
	home, _ := os.UserHomeDir()
	writeJSON(w, http.StatusOK, map[string]any{
		"feature": "fleet-sync-v1", "version": 1, "home_dir": home,
	})
}

func requireFleetPeer(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-AgentMux-Fleet-Peer") != "1" {
		writeErr(w, http.StatusForbidden, "fleet sync peer request required")
		return false
	}
	return true
}

func (s *Server) handleFleetSyncExport(w http.ResponseWriter, r *http.Request) {
	if !requireFleetPeer(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request struct {
		IncludeCredentials bool     `json:"include_credentials"`
		Categories         []string `json:"categories,omitempty"`
		ProviderIDs        []string `json:"provider_ids,omitempty"`
	}
	if !decodeJSONInto(w, r, &request) {
		return
	}
	manifest, err := s.exportFleetSyncManifest(r.Context(), request.IncludeCredentials, request.Categories, request.ProviderIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleFleetSyncInspect(w http.ResponseWriter, r *http.Request) {
	if !requireFleetPeer(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, fleetSyncMaxBodyBytes)
	var request fleetSyncInspectRequest
	if !decodeJSONInto(w, r, &request) {
		return
	}
	inspection, err := s.inspectFleetSync(r.Context(), request, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inspection)
}

func (s *Server) handleFleetSyncImport(w http.ResponseWriter, r *http.Request) {
	if !requireFleetPeer(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, fleetSyncMaxBodyBytes)
	var request fleetSyncInspectRequest
	if !decodeJSONInto(w, r, &request) {
		return
	}
	inspection, err := s.inspectFleetSync(r.Context(), request, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inspection)
}

func (s *Server) handleFleetSyncPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var request fleetSyncPreviewRequest
	if !decodeJSONInto(w, r, &request) {
		return
	}
	request.SourceTargetID = strings.TrimSpace(request.SourceTargetID)
	if request.SourceTargetID == "" || request.SourceTargetID == fleetAllTargetID {
		writeErr(w, http.StatusBadRequest, "source_target_id must name one machine")
		return
	}
	if len(request.DestinationIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "at least one destination target is required")
		return
	}
	sourceTargets, err := s.resolveFleetTargets([]string{request.SourceTargetID})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	destinations, err := s.resolveFleetTargets(request.DestinationIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, destination := range destinations {
		if destination.ID == sourceTargets[0].ID {
			writeErr(w, http.StatusBadRequest, "source machine cannot also be a destination")
			return
		}
	}

	request.ProviderIDs = normalizeFleetSyncIDs(request.ProviderIDs)
	redactedManifest, err := s.exportManifestFromTarget(r.Context(), sourceTargets[0], false, request.Categories, request.ProviderIDs)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	manifest := redactedManifest
	if request.IncludeCredentials {
		manifest, err = s.exportManifestFromTarget(r.Context(), sourceTargets[0], true, request.Categories, request.ProviderIDs)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	previews := make([]fleetSyncDestinationPreview, len(destinations))
	resolvedMappings := map[string][]fleetSyncPathMapping{}
	var wg sync.WaitGroup
	var mappingMu sync.Mutex
	sem := make(chan struct{}, 4)
	for index, destination := range destinations {
		index, destination := index, destination
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			mappings := append([]fleetSyncPathMapping(nil), request.PathMappings[destination.ID]...)
			if len(mappings) == 0 && manifest.SourceHome != "" {
				if home, capabilityErr := s.targetFleetSyncHome(r.Context(), destination); capabilityErr == nil && home != "" {
					mappings = []fleetSyncPathMapping{{From: manifest.SourceHome, To: home}}
				}
			}
			mappingMu.Lock()
			resolvedMappings[destination.ID] = mappings
			mappingMu.Unlock()
			inspection, inspectErr := s.inspectManifestOnTarget(r.Context(), destination, fleetSyncInspectRequest{
				Manifest: manifest, PathMappings: mappings, PreserveActivation: request.PreserveActivation,
			})
			previews[index] = fleetSyncDestinationPreview{Target: destination, Inspection: inspection, PathMappings: mappings}
			if inspectErr != nil {
				previews[index].Error = inspectErr.Error()
			}
		}()
	}
	wg.Wait()

	planID := newFleetSyncID()
	expiresAt := time.Now().Add(fleetSyncPlanTTL)
	plan := &fleetSyncPlan{
		ID: planID, ExpiresAt: expiresAt, Source: sourceTargets[0], Destinations: destinations,
		Categories: append([]string(nil), request.Categories...), IncludeCredentials: request.IncludeCredentials,
		ProviderIDs:        append([]string(nil), request.ProviderIDs...),
		PreserveActivation: request.PreserveActivation, Mappings: resolvedMappings,
		SourceDigest: semanticFingerprint(redactedManifest),
	}
	s.fleetSyncMu.Lock()
	for id, existing := range s.fleetSyncPlans {
		if time.Now().After(existing.ExpiresAt) {
			delete(s.fleetSyncPlans, id)
		}
	}
	s.fleetSyncPlans[planID] = plan
	s.fleetSyncMu.Unlock()
	writeJSON(w, http.StatusOK, fleetSyncPreviewResponse{
		PlanID: planID, ExpiresAt: expiresAt, Source: sourceTargets[0], Destinations: previews,
	})
}

func (s *Server) handleFleetSyncApply(w http.ResponseWriter, r *http.Request) {
	var request fleetSyncApplyRequest
	if !decodeJSONInto(w, r, &request) {
		return
	}
	s.fleetSyncMu.Lock()
	plan := s.fleetSyncPlans[strings.TrimSpace(request.PlanID)]
	if plan != nil && time.Now().After(plan.ExpiresAt) {
		delete(s.fleetSyncPlans, plan.ID)
		plan = nil
	}
	s.fleetSyncMu.Unlock()
	if plan == nil {
		writeErr(w, http.StatusConflict, "sync plan is missing or expired; preview again")
		return
	}
	currentManifest, err := s.exportManifestFromTarget(r.Context(), plan.Source, false, plan.Categories, plan.ProviderIDs)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if semanticFingerprint(currentManifest) != plan.SourceDigest {
		writeErr(w, http.StatusConflict, "source configuration changed after preview; preview again")
		return
	}
	manifest := currentManifest
	if plan.IncludeCredentials {
		manifest, err = s.exportManifestFromTarget(r.Context(), plan.Source, true, plan.Categories, plan.ProviderIDs)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	results := make([]fleetSyncApplyTarget, len(plan.Destinations))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for index, destination := range plan.Destinations {
		index, destination := index, destination
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			inspection, applyErr := s.importManifestOnTarget(r.Context(), destination, fleetSyncInspectRequest{
				Manifest: manifest, PathMappings: plan.Mappings[destination.ID], PreserveActivation: plan.PreserveActivation,
			})
			results[index] = fleetSyncApplyTarget{Target: destination, Inspection: inspection}
			if applyErr != nil {
				results[index].Error = applyErr.Error()
			}
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, fleetSyncApplyResponse{PlanID: plan.ID, Targets: results})
}

func (s *Server) handleFleetSyncApplyStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	var request fleetSyncApplyRequest
	if !decodeJSONInto(w, r, &request) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	writeFleetSyncEvent(w, flusher, "progress", map[string]any{"phase": "preparing", "percent": 5})
	body := mustJSON(request)
	internal := httptest.NewRequestWithContext(r.Context(), http.MethodPost, "/api/v1/remote/sync/apply", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	s.handleFleetSyncApply(recorder, internal)
	response := recorder.Result()
	payload := recorder.Body.Bytes()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeFleetSyncEvent(w, flusher, "error", map[string]any{"error": fleetErrorMessage(payload, response.StatusCode)})
		return
	}
	var result fleetSyncApplyResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		writeFleetSyncEvent(w, flusher, "error", map[string]any{"error": err.Error()})
		return
	}
	writeFleetSyncEvent(w, flusher, "progress", map[string]any{"phase": "complete", "percent": 100})
	writeFleetSyncEvent(w, flusher, "result", result)
}

func writeFleetSyncEvent(w http.ResponseWriter, flusher http.Flusher, event string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	flusher.Flush()
}

func (s *Server) exportManifestFromTarget(ctx context.Context, target machineTarget, includeCredentials bool, categories, providerIDs []string) (fleetSyncManifest, error) {
	if target.ID == fleetLocalTargetID {
		return s.exportFleetSyncManifest(ctx, includeCredentials, categories, providerIDs)
	}
	var manifest fleetSyncManifest
	err := s.syncPeerJSON(ctx, target, fleetOperation{
		Key: "export", Method: http.MethodPost, Path: "/api/v1/fleet-sync/export",
		Body: mustJSON(map[string]any{"include_credentials": includeCredentials, "categories": categories, "provider_ids": providerIDs}),
	}, &manifest)
	if err == nil {
		// Older peers ignore provider_ids. Filter again on the controller so a
		// single-provider transfer can never widen into an all-provider export.
		err = filterFleetSyncProviders(&manifest, providerIDs)
	}
	return manifest, err
}

func (s *Server) inspectManifestOnTarget(ctx context.Context, target machineTarget, request fleetSyncInspectRequest) (fleetSyncInspection, error) {
	if target.ID == fleetLocalTargetID {
		return s.inspectFleetSync(ctx, request, false)
	}
	var inspection fleetSyncInspection
	err := s.syncPeerJSON(ctx, target, fleetOperation{Key: "inspect", Method: http.MethodPost, Path: "/api/v1/fleet-sync/inspect", Body: mustJSON(request)}, &inspection)
	return inspection, err
}

func (s *Server) importManifestOnTarget(ctx context.Context, target machineTarget, request fleetSyncInspectRequest) (fleetSyncInspection, error) {
	if target.ID == fleetLocalTargetID {
		return s.inspectFleetSync(ctx, request, true)
	}
	var inspection fleetSyncInspection
	err := s.syncPeerJSON(ctx, target, fleetOperation{Key: "import", Method: http.MethodPost, Path: "/api/v1/fleet-sync/import", Body: mustJSON(request)}, &inspection)
	return inspection, err
}

func (s *Server) targetFleetSyncHome(ctx context.Context, target machineTarget) (string, error) {
	if target.ID == fleetLocalTargetID {
		return os.UserHomeDir()
	}
	var capability struct {
		HomeDir string `json:"home_dir"`
	}
	err := s.syncPeerJSON(ctx, target, fleetOperation{Key: "capabilities", Method: http.MethodGet, Path: "/api/v1/fleet-sync/capabilities"}, &capability)
	return capability.HomeDir, err
}

func (s *Server) syncPeerJSON(ctx context.Context, target machineTarget, operation fleetOperation, output any) error {
	status, payload, err := s.executeFleetOperation(ctx, target.ID, operation)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s: %s", target.Name, fleetErrorMessage(payload, status))
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return fmt.Errorf("decode %s fleet sync response: %w", target.Name, err)
	}
	return nil
}

func (s *Server) exportFleetSyncManifest(ctx context.Context, includeCredentials bool, categories, providerIDs []string) (fleetSyncManifest, error) {
	if s.st == nil {
		return fleetSyncManifest{}, errors.New("store unavailable")
	}
	selected := syncCategorySet(categories)
	home, _ := os.UserHomeDir()
	manifest := fleetSyncManifest{Version: 1, SourceHome: home, ProviderKeys: map[string]string{}, TenantNames: map[string]string{}}
	var err error
	if selected("agents") {
		manifest.Agents, err = s.st.ListAgentInstances(ctx)
		if err != nil {
			return manifest, err
		}
		manifest.Agents = filterSyncAgents(manifest.Agents, includeCredentials, &manifest.CredentialsOmitted)
	}
	if selected("providers") {
		manifest.Providers, err = s.st.ListProviders(ctx)
		if err != nil {
			return manifest, err
		}
		manifest.Routes, _ = s.st.ActiveProviderRoutes(ctx)
		for _, item := range manifest.Providers {
			item.APIKey = ""
			if item.APIKeyEnv == "" {
				continue
			}
			key, keyErr := providerpkg.LoadProviderAPIKey(item.APIKeyEnv)
			if keyErr == nil && key != "" {
				if includeCredentials {
					manifest.ProviderKeys[item.ID] = key
				} else {
					manifest.CredentialsOmitted = append(manifest.CredentialsOmitted, "provider:"+item.ID)
				}
			}
		}
	}
	if selected("channels") {
		manifest.Channels, err = s.st.ListChannels(ctx)
		if err != nil {
			return manifest, err
		}
		for index := range manifest.Channels {
			if !includeCredentials {
				stripSecretValues(manifest.Channels[index].Config, "channel:"+manifest.Channels[index].ID, &manifest.CredentialsOmitted)
			}
		}
	}
	if selected("triggers") {
		manifest.Triggers, err = s.st.ListTriggers(ctx)
		if err != nil {
			return manifest, err
		}
		for index := range manifest.Triggers {
			if !includeCredentials && manifest.Triggers[index].Token != "" {
				manifest.CredentialsOmitted = append(manifest.CredentialsOmitted, "trigger:"+manifest.Triggers[index].ID)
				manifest.Triggers[index].Token = ""
			}
		}
	}
	if selected("mcp") {
		manifest.MCPServers, err = s.st.ListMCPServers(ctx)
		if err != nil {
			return manifest, err
		}
		if !includeCredentials {
			for index := range manifest.MCPServers {
				if len(manifest.MCPServers[index].Env) > 0 {
					manifest.CredentialsOmitted = append(manifest.CredentialsOmitted, "mcp:"+manifest.MCPServers[index].Name)
				}
				manifest.MCPServers[index].Env = nil
			}
		}
	}
	if selected("guard") {
		manifest.GuardPolicies, err = s.st.ListGuardPolicies(ctx)
		if err != nil {
			return manifest, err
		}
	}
	if selected("memory") {
		manifest.Memory, err = s.st.SearchMemory(ctx, "", "", 100000)
		if err != nil {
			return manifest, err
		}
	}
	if selected("skills") {
		manifest.Skills, err = s.exportFleetSyncSkills(ctx)
		if err != nil {
			return manifest, err
		}
	}
	if tenants, tenantErr := s.st.ListTenants(ctx); tenantErr == nil {
		for _, tenant := range tenants {
			manifest.TenantNames[tenant.ID] = tenant.Name
		}
	}
	manifest.Grants, _ = s.st.ListResourceGrants(ctx, "")
	if err := filterFleetSyncProviders(&manifest, providerIDs); err != nil {
		return manifest, err
	}
	sort.Strings(manifest.CredentialsOmitted)
	return manifest, nil
}

func normalizeFleetSyncIDs(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func filterFleetSyncProviders(manifest *fleetSyncManifest, providerIDs []string) error {
	providerIDs = normalizeFleetSyncIDs(providerIDs)
	if len(providerIDs) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, id := range providerIDs {
		wanted[id] = true
	}
	selected := make([]*core.Provider, 0, len(providerIDs))
	found := map[string]bool{}
	for _, item := range manifest.Providers {
		if item != nil && wanted[item.ID] {
			selected = append(selected, item)
			found[item.ID] = true
		}
	}
	missing := make([]string, 0)
	for _, id := range providerIDs {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("providers not found on source machine: %s", strings.Join(missing, ", "))
	}
	manifest.Providers = selected
	// A card-level transfer moves only the selected Provider configuration.
	// Active routes are machine-local policy and remain untouched.
	manifest.Routes = nil
	keys := map[string]string{}
	for id, key := range manifest.ProviderKeys {
		if wanted[id] {
			keys[id] = key
		}
	}
	manifest.ProviderKeys = keys
	omitted := make([]string, 0, len(manifest.CredentialsOmitted))
	for _, value := range manifest.CredentialsOmitted {
		if !strings.HasPrefix(value, "provider:") || wanted[strings.TrimPrefix(value, "provider:")] {
			omitted = append(omitted, value)
		}
	}
	manifest.CredentialsOmitted = omitted
	grants := make([]core.ResourceGrant, 0, len(manifest.Grants))
	for _, grant := range manifest.Grants {
		if grant.ResourceType == core.ResourceTypeProvider && wanted[grant.ResourceID] {
			grants = append(grants, grant)
		}
	}
	manifest.Grants = grants
	return nil
}

func syncCategorySet(categories []string) func(string) bool {
	if len(categories) == 0 {
		return func(string) bool { return true }
	}
	set := map[string]bool{}
	for _, category := range categories {
		set[strings.ToLower(strings.TrimSpace(category))] = true
	}
	if set["agents"] {
		for _, dependency := range []string{"providers", "channels", "triggers", "mcp", "skills", "memory"} {
			set[dependency] = true
		}
	}
	return func(category string) bool { return set[category] }
}

func filterSyncAgents(items []core.AgentInstance, includeCredentials bool, omitted *[]string) []core.AgentInstance {
	out := make([]core.AgentInstance, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.ID, "config:") || item.Source == "config.toml" {
			continue
		}
		item.ChannelBindings = nil
		item.Schedules = nil
		if !includeCredentials && len(item.Env) > 0 {
			*omitted = append(*omitted, "agent:"+item.ID)
			item.Env = nil
		}
		out = append(out, item)
	}
	return out
}

func stripSecretValues(values map[string]string, resource string, omitted *[]string) {
	removed := false
	for key := range values {
		if isSecretish(key) {
			delete(values, key)
			removed = true
		}
	}
	if removed {
		*omitted = append(*omitted, resource)
	}
}

func (s *Server) exportFleetSyncSkills(ctx context.Context) ([]fleetSyncSkill, error) {
	if s.skills == nil {
		return nil, nil
	}
	items, err := s.skills.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]fleetSyncSkill, 0, len(items))
	total := int64(0)
	for _, item := range items {
		root := filepath.Dir(item.Path)
		bundle := fleetSyncSkill{Name: item.Name, Description: item.Description, Enabled: item.Enabled, Files: map[string]string{}}
		itemBytes := int64(0)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("skill %s contains a symbolic link", item.Name)
			}
			if entry.IsDir() {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("skill %s contains a non-regular file", item.Name)
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
				return fmt.Errorf("skill %s contains an unsafe path", item.Name)
			}
			itemBytes += info.Size()
			total += info.Size()
			if itemBytes > fleetSyncMaxSkillBytes || total > fleetSyncMaxBundleBytes {
				return fmt.Errorf("skill bundle exceeds the sync size limit")
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			bundle.Files[filepath.ToSlash(relative)] = base64.StdEncoding.EncodeToString(data)
			return nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, bundle)
	}
	return out, nil
}

func (s *Server) inspectFleetSync(ctx context.Context, request fleetSyncInspectRequest, apply bool) (fleetSyncInspection, error) {
	if s.st == nil {
		return fleetSyncInspection{}, errors.New("store unavailable")
	}
	home, _ := os.UserHomeDir()
	result := fleetSyncInspection{HomeDir: home, Resources: []fleetSyncResourceResult{}}
	omitted := map[string]bool{}
	for _, key := range request.Manifest.CredentialsOmitted {
		omitted[key] = true
	}

	destinationTenants, _ := s.st.ListTenants(ctx)
	tenantByName := map[string]string{}
	for _, tenant := range destinationTenants {
		tenantByName[strings.ToLower(tenant.Name)] = tenant.ID
	}

	providers, _ := s.st.ListProviders(ctx)
	providerIDs, providerNames := providerIndexes(providers)
	for _, source := range request.Manifest.Providers {
		item := *source
		if !request.PreserveActivation {
			item.Enabled = false
			item.InFailoverQueue = false
		}
		action := syncActionFor(item.ID, item.Name, providerIDs, providerNames, semanticFingerprint(&item))
		action.Type = "provider"
		action.CredentialsMissing = omitted["provider:"+item.ID]
		if apply && action.Action == "add" {
			if s.provider == nil {
				action.Action = "blocked"
				action.Reason = "provider manager unavailable"
			}
			if key := request.Manifest.ProviderKeys[item.ID]; key != "" {
				if err := providerpkg.SaveProviderAPIKey(item.APIKeyEnv, key); err != nil {
					action.Action = "blocked"
					action.Reason = err.Error()
				}
			}
			if action.Action == "add" {
				item.APIKey = ""
				if err := s.provider.Upsert(ctx, &item); err != nil {
					action.Action = "blocked"
					action.Reason = err.Error()
				} else {
					providerIDs[item.ID] = semanticFingerprint(&item)
					providerNames[strings.ToLower(item.Name)] = item.ID
				}
			}
		}
		if !apply && action.Action == "add" {
			providerIDs[item.ID] = semanticFingerprint(&item)
			providerNames[strings.ToLower(item.Name)] = item.ID
		}
		result.Resources = append(result.Resources, action)
	}
	destinationRoutes, _ := s.st.ActiveProviderRoutes(ctx)
	routesByTool := map[string]core.ProviderRoute{}
	for _, route := range destinationRoutes {
		routesByTool[route.Tool] = route
	}
	for _, route := range request.Manifest.Routes {
		action := fleetSyncResourceResult{Type: "route", Key: route.Tool, Name: route.Tool, Action: "add"}
		if !request.PreserveActivation {
			action.Action = "blocked"
			action.Reason = "active routes remain disabled by the sync plan"
		} else if existing, ok := routesByTool[route.Tool]; ok {
			if semanticFingerprint(existing) == semanticFingerprint(route) {
				action.Action = "exists"
			} else {
				action.Action = "conflict"
				action.Reason = "destination tool already has a different active route"
			}
		} else if providerIDs[route.ProviderID] == "" {
			action.Action = "blocked"
			action.Reason = "referenced provider is missing or conflicted"
		}
		if apply && action.Action == "add" {
			if s.provider == nil {
				action.Action = "blocked"
				action.Reason = "provider manager unavailable"
			} else if err := s.provider.SwitchRoute(ctx, route); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			} else {
				routesByTool[route.Tool] = route
			}
		}
		result.Resources = append(result.Resources, action)
	}

	mcpItems, _ := s.st.ListMCPServers(ctx)
	mcpByName := map[string]string{}
	for _, item := range mcpItems {
		mcpByName[strings.ToLower(item.Name)] = semanticFingerprint(item)
	}
	for _, source := range request.Manifest.MCPServers {
		item := source
		if !request.PreserveActivation {
			item.Enabled = false
		}
		action := fleetSyncResourceResult{Type: "mcp", Key: item.Name, Name: item.Name, Action: "add", CredentialsMissing: omitted["mcp:"+item.Name]}
		if existing, ok := mcpByName[strings.ToLower(item.Name)]; ok {
			if existing == semanticFingerprint(item) {
				action.Action = "exists"
			} else {
				action.Action = "conflict"
				action.Reason = "same name has different configuration"
			}
		}
		if blocked := validateSyncCommandPath(&item, request.PathMappings); blocked != "" {
			action.Action = "blocked"
			action.Reason = blocked
		}
		if apply && action.Action == "add" && s.mcp == nil {
			action.Action = "blocked"
			action.Reason = "MCP registry unavailable"
		}
		if apply && action.Action == "add" {
			if err := s.mcp.Upsert(ctx, &item); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			} else {
				mcpByName[strings.ToLower(item.Name)] = semanticFingerprint(item)
			}
		}
		if !apply && action.Action == "add" {
			mcpByName[strings.ToLower(item.Name)] = semanticFingerprint(item)
		}
		result.Resources = append(result.Resources, action)
	}

	guardItems, _ := s.st.ListGuardPolicies(ctx)
	guardIDs, guardNames := guardIndexes(guardItems)
	for _, item := range request.Manifest.GuardPolicies {
		action := syncActionFor(item.ID, item.Tool+"/"+item.Action, guardIDs, guardNames, semanticFingerprint(item))
		action.Type = "guard"
		if apply && action.Action == "add" {
			if err := s.st.UpsertGuardPolicy(ctx, &item); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			} else {
				guardIDs[item.ID] = semanticFingerprint(item)
			}
		}
		result.Resources = append(result.Resources, action)
	}

	memoryItems, _ := s.st.SearchMemory(ctx, "", "", 100000)
	memoryIDs := map[string]string{}
	for _, item := range memoryItems {
		memoryIDs[item.ID] = semanticFingerprint(item)
	}
	for _, item := range request.Manifest.Memory {
		action := fleetSyncResourceResult{Type: "memory", Key: item.ID, Name: item.Scope, Action: "add"}
		if existing, ok := memoryIDs[item.ID]; ok {
			if existing == semanticFingerprint(item) {
				action.Action = "exists"
			} else {
				action.Action = "conflict"
				action.Reason = "same id has different content"
			}
		}
		if apply && action.Action == "add" {
			if err := s.st.PutMemory(ctx, item); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			} else {
				memoryIDs[item.ID] = semanticFingerprint(item)
			}
		}
		result.Resources = append(result.Resources, action)
	}

	agents, _ := s.st.ListAgentInstances(ctx)
	agentIDs, agentNames := agentIndexes(agents)
	plannedSkillNames := map[string]bool{}
	if s.skills != nil {
		if installed, listErr := s.skills.List(ctx); listErr == nil {
			for _, skill := range installed {
				plannedSkillNames[strings.ToLower(skill.Name)] = true
			}
		}
	}
	for _, skill := range request.Manifest.Skills {
		plannedSkillNames[strings.ToLower(skill.Name)] = true
	}
	for _, source := range request.Manifest.Agents {
		item := source
		mapSyncOwner(&item.OwnerTenantID, &item.Visibility, request.Manifest.TenantNames, tenantByName, &result.Warnings, "agent "+item.Name)
		blocked := rewriteAndValidateAgentPath(&item, request.PathMappings)
		action := syncActionFor(item.ID, item.Name, agentIDs, agentNames, semanticFingerprint(item))
		action.Type = "agent"
		action.CredentialsMissing = omitted["agent:"+item.ID]
		if blocked != "" {
			action.Action = "blocked"
			action.Reason = blocked
		}
		if item.RuntimeID == "codex-app" && item.DesktopThreadID != "" {
			action.Action = "blocked"
			action.Reason = "Codex Desktop agents require a destination thread selection"
		}
		if item.ProviderID != "" && providerIDs[item.ProviderID] == "" {
			action.Action = "blocked"
			action.Reason = "referenced provider is missing or conflicted"
		}
		for _, name := range item.MCPServers {
			if mcpByName[strings.ToLower(name)] == "" {
				action.Action = "blocked"
				action.Reason = "referenced MCP server is missing or conflicted"
				break
			}
		}
		for _, name := range item.Skills {
			if !plannedSkillNames[strings.ToLower(name)] {
				action.Action = "blocked"
				action.Reason = "referenced skill is missing"
				break
			}
		}
		if apply && action.Action == "add" {
			if err := s.st.UpsertAgentInstance(ctx, &item); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			} else {
				agentIDs[item.ID] = semanticFingerprint(item)
				agentNames[strings.ToLower(item.Name)] = item.ID
			}
		}
		if !apply && action.Action == "add" {
			agentIDs[item.ID] = semanticFingerprint(item)
			agentNames[strings.ToLower(item.Name)] = item.ID
		}
		result.Resources = append(result.Resources, action)
	}

	channels, _ := s.st.ListChannels(ctx)
	channelIDs, channelNames := channelIndexes(channels)
	for _, source := range request.Manifest.Channels {
		item := source
		if item.Enabled && request.PreserveActivation {
			result.Warnings = append(result.Warnings, "channel "+item.Name+": imported disabled; claim it from the Channel page to move the exclusive connection safely")
		}
		item.Enabled = false
		mapSyncOwner(&item.OwnerTenantID, &item.Visibility, request.Manifest.TenantNames, tenantByName, &result.Warnings, "channel "+item.Name)
		action := syncActionFor(item.ID, item.Name, channelIDs, channelNames, semanticFingerprint(item))
		action.Type = "channel"
		action.CredentialsMissing = omitted["channel:"+item.ID]
		if item.AgentID != "" && agentIDs[item.AgentID] == "" {
			action.Action = "blocked"
			action.Reason = "referenced agent is missing or conflicted"
		}
		if apply && action.Action == "add" {
			if err := s.st.UpsertChannel(ctx, &item); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			} else {
				channelIDs[item.ID] = semanticFingerprint(item)
				channelNames[strings.ToLower(item.Name)] = item.ID
			}
		}
		if !apply && action.Action == "add" {
			channelIDs[item.ID] = semanticFingerprint(item)
			channelNames[strings.ToLower(item.Name)] = item.ID
		}
		result.Resources = append(result.Resources, action)
	}

	triggers, _ := s.st.ListTriggers(ctx)
	triggerIDs, triggerNames := triggerIndexes(triggers)
	for _, source := range request.Manifest.Triggers {
		item := source
		if !request.PreserveActivation {
			item.Enabled = false
		}
		visibility := core.VisibilityPrivate
		mapSyncOwner(&item.OwnerTenantID, &visibility, request.Manifest.TenantNames, tenantByName, &result.Warnings, "trigger "+item.Name)
		action := syncActionFor(item.ID, item.Name, triggerIDs, triggerNames, semanticFingerprint(item))
		action.Type = "trigger"
		action.CredentialsMissing = omitted["trigger:"+item.ID]
		if item.AgentID != "" && agentIDs[item.AgentID] == "" {
			action.Action = "blocked"
			action.Reason = "referenced agent is missing or conflicted"
		}
		if item.ChannelID != "" && channelIDs[item.ChannelID] == "" {
			action.Action = "blocked"
			action.Reason = "referenced channel is missing or conflicted"
		}
		if apply && action.Action == "add" {
			if item.Kind == core.TriggerWebhook && item.Token == "" {
				item.Token = newFleetSyncID()
			}
			if err := s.st.UpsertTrigger(ctx, &item); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			} else {
				triggerIDs[item.ID] = semanticFingerprint(item)
				triggerNames[strings.ToLower(item.Name)] = item.ID
			}
		}
		result.Resources = append(result.Resources, action)
	}

	installedSkills, _ := s.exportFleetSyncSkills(ctx)
	skillNames := map[string]string{}
	for _, item := range installedSkills {
		skillNames[strings.ToLower(item.Name)] = semanticFingerprint(item)
	}
	for _, item := range request.Manifest.Skills {
		action := fleetSyncResourceResult{Type: "skill", Key: item.Name, Name: item.Name, Action: "add"}
		if fingerprint, ok := skillNames[strings.ToLower(item.Name)]; ok {
			if fingerprint == semanticFingerprint(item) {
				action.Action = "exists"
			} else {
				action.Action = "conflict"
				action.Reason = "same skill name has different files"
			}
		}
		if apply && action.Action == "add" {
			if err := applyFleetSyncSkill(item); err != nil {
				action.Action = "blocked"
				action.Reason = err.Error()
			}
		}
		result.Resources = append(result.Resources, action)
	}

	if apply {
		s.reloadChannels(ctx)
		s.reloadTriggers(ctx)
		applySyncGrants(ctx, s.st, request.Manifest, tenantByName, &result)
	}
	sort.SliceStable(result.Resources, func(i, j int) bool {
		if result.Resources[i].Type == result.Resources[j].Type {
			return result.Resources[i].Name < result.Resources[j].Name
		}
		return result.Resources[i].Type < result.Resources[j].Type
	})
	return result, nil
}

func providerIndexes(items []*core.Provider) (map[string]string, map[string]string) {
	ids, names := map[string]string{}, map[string]string{}
	for _, item := range items {
		ids[item.ID] = semanticFingerprint(item)
		names[strings.ToLower(item.Name)] = item.ID
	}
	return ids, names
}
func agentIndexes(items []core.AgentInstance) (map[string]string, map[string]string) {
	ids, names := map[string]string{}, map[string]string{}
	for _, item := range items {
		ids[item.ID] = semanticFingerprint(item)
		names[strings.ToLower(item.Name)] = item.ID
	}
	return ids, names
}
func channelIndexes(items []core.Channel) (map[string]string, map[string]string) {
	ids, names := map[string]string{}, map[string]string{}
	for _, item := range items {
		ids[item.ID] = semanticFingerprint(item)
		names[strings.ToLower(item.Name)] = item.ID
	}
	return ids, names
}
func triggerIndexes(items []core.Trigger) (map[string]string, map[string]string) {
	ids, names := map[string]string{}, map[string]string{}
	for _, item := range items {
		ids[item.ID] = semanticFingerprint(item)
		names[strings.ToLower(item.Name)] = item.ID
	}
	return ids, names
}
func guardIndexes(items []core.GuardPolicy) (map[string]string, map[string]string) {
	ids, names := map[string]string{}, map[string]string{}
	for _, item := range items {
		ids[item.ID] = semanticFingerprint(item)
		names[strings.ToLower(item.Tool+"/"+item.Action)] = item.ID
	}
	return ids, names
}

func syncActionFor(id, name string, ids, names map[string]string, fingerprint string) fleetSyncResourceResult {
	action := fleetSyncResourceResult{Key: id, Name: name, Action: "add"}
	if existing, ok := ids[id]; ok {
		if existing == fingerprint {
			action.Action = "exists"
		} else {
			action.Action = "conflict"
			action.Reason = "same id has different configuration"
		}
		return action
	}
	if existingID, ok := names[strings.ToLower(name)]; ok {
		action.Action = "conflict"
		action.Reason = "same name already exists as " + existingID
	}
	return action
}

func semanticFingerprint(value any) string {
	switch item := value.(type) {
	case *core.Provider:
		copy := *item
		copy.APIKey = ""
		copy.APIKeyAvailable = false
		copy.APIKeyIssue = ""
		copy.Enabled = false
		copy.InFailoverQueue = false
		copy.CreatedAt = time.Time{}
		copy.UpdatedAt = time.Time{}
		value = copy
	case core.AgentInstance:
		item.ProviderName = ""
		item.OwnerTenantName = ""
		item.CreatedAt = time.Time{}
		item.UpdatedAt = time.Time{}
		value = item
	case core.Channel:
		item.Enabled = false
		item.CreatedAt = time.Time{}
		item.UpdatedAt = time.Time{}
		value = item
	case core.Trigger:
		item.Enabled = false
		item.LastRun = time.Time{}
		item.LastStatus = ""
		item.LastError = ""
		item.CreatedAt = time.Time{}
		item.UpdatedAt = time.Time{}
		value = item
	case core.MCPServer:
		item.Enabled = false
		value = item
	case *core.MemoryEntry:
		copy := *item
		copy.CreatedAt = time.Time{}
		copy.UpdatedAt = time.Time{}
		value = copy
	}
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func rewriteAndValidateAgentPath(item *core.AgentInstance, mappings []fleetSyncPathMapping) string {
	if item.WorkDir == "" {
		return ""
	}
	item.WorkDir = applySyncPath(item.WorkDir, mappings)
	if !filepath.IsAbs(item.WorkDir) {
		return "working directory is not absolute after mapping"
	}
	info, err := os.Stat(item.WorkDir)
	if err != nil || !info.IsDir() {
		return "mapped working directory does not exist"
	}
	return ""
}

func validateSyncCommandPath(item *core.MCPServer, mappings []fleetSyncPathMapping) string {
	if item.Command == "" || !filepath.IsAbs(item.Command) {
		return ""
	}
	item.Command = applySyncPath(item.Command, mappings)
	info, err := os.Stat(item.Command)
	if err != nil || info.IsDir() {
		return "mapped MCP command does not exist"
	}
	return ""
}

func applySyncPath(value string, mappings []fleetSyncPathMapping) string {
	for _, mapping := range mappings {
		from, to := filepath.Clean(mapping.From), filepath.Clean(mapping.To)
		if from != "." && to != "." && (value == from || strings.HasPrefix(value, from+string(os.PathSeparator))) {
			return filepath.Join(to, strings.TrimPrefix(value, from))
		}
	}
	return value
}

func mapSyncOwner(owner *string, visibility *string, sourceNames map[string]string, destinationByName map[string]string, warnings *[]string, label string) {
	if owner == nil || *owner == "" {
		return
	}
	name := sourceNames[*owner]
	if destination := destinationByName[strings.ToLower(name)]; destination != "" {
		*owner = destination
		return
	}
	*owner = ""
	if visibility != nil {
		*visibility = core.VisibilityPrivate
	}
	*warnings = append(*warnings, label+": owner tenant "+name+" is unavailable; imported as private and unassigned")
}

func applySyncGrants(ctx context.Context, st *store.Store, manifest fleetSyncManifest, tenantByName map[string]string, result *fleetSyncInspection) {
	existing, _ := st.ListResourceGrants(ctx, "")
	keys := map[string]bool{}
	for _, grant := range existing {
		keys[grant.TenantID+"\x00"+grant.ResourceType+"\x00"+grant.ResourceID] = true
	}
	for _, grant := range manifest.Grants {
		name := manifest.TenantNames[grant.TenantID]
		grant.TenantID = tenantByName[strings.ToLower(name)]
		if grant.TenantID == "" {
			continue
		}
		key := grant.TenantID + "\x00" + grant.ResourceType + "\x00" + grant.ResourceID
		if keys[key] {
			continue
		}
		if err := st.UpsertResourceGrant(ctx, &grant); err != nil {
			result.Warnings = append(result.Warnings, "grant "+name+": "+err.Error())
		} else {
			keys[key] = true
		}
	}
}

func applyFleetSyncSkill(item fleetSyncSkill) error {
	name := strings.TrimSpace(item.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return errors.New("invalid skill name")
	}
	root := skillpkg.DefaultRoots()[0]
	destination := filepath.Join(root, name)
	if _, err := os.Stat(destination); err == nil {
		return nil
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	for relative, encoded := range item.Files {
		clean := filepath.Clean(filepath.FromSlash(relative))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return errors.New("skill archive contains an unsafe path")
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		path := filepath.Join(destination, clean)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

func newFleetSyncID() string {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}
