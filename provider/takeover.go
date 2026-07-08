package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

// takeover.go implements cc-switch's Local Routing takeover lifecycle:
// enabling routes a tool's live config at the local proxy (with a backup of
// the original), switching while enabled is a DB-only hot switch, and
// disabling restores the backup (or rebuilds live from the active provider).

// takeoverTools are the canonical tools local routing can take over.
var takeoverTools = map[string]bool{
	"claudecode":     true,
	"codex":          true,
	"claude-desktop": true,
}

// liveBackupBlob is the JSON stored in proxy_live_backup: file path -> content
// (null = file did not exist).
type liveBackupBlob struct {
	Files map[string]*string `json:"files"`
}

// takeoverLiveFiles lists the live files a tool's takeover touches.
func takeoverLiveFiles(home, tool string, p *core.Provider) ([]string, error) {
	switch tool {
	case "claudecode":
		return []string{filepath.Join(claudeConfigDir(home, p), "settings.json")}, nil
	case "codex":
		dir := codexConfigDir(home, p)
		return []string{filepath.Join(dir, "config.toml")}, nil
	case "claude-desktop":
		paths, err := claudeDesktopPathsFor(home, p)
		if err != nil {
			return nil, err
		}
		return []string{paths.normalConfigPath, paths.threepConfigPath, paths.profilePath, paths.metaPath}, nil
	default:
		return nil, fmt.Errorf("tool %q does not support local routing", tool)
	}
}

func snapshotLiveFiles(files []string) (string, error) {
	blob := liveBackupBlob{Files: map[string]*string{}}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			blob.Files[f] = nil
			continue
		}
		if err != nil {
			return "", err
		}
		content := string(data)
		blob.Files[f] = &content
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func restoreLiveFiles(blobRaw string) error {
	var blob liveBackupBlob
	if err := json.Unmarshal([]byte(blobRaw), &blob); err != nil {
		return fmt.Errorf("corrupt live backup: %w", err)
	}
	for path, content := range blob.Files {
		if content == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := store.AtomicWrite(path, []byte(*content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// liveAlreadyProxied detects a live config that already points at a local
// proxy (placeholder token or loopback URL), so enable never backs up a
// proxied state as if it were the user's original (cc-switch's strict-backup
// guard).
func liveAlreadyProxied(files []string) bool {
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		content := string(data)
		if strings.Contains(content, ProxyManagedToken) {
			return true
		}
	}
	return false
}

// writeClaudeTakeoverConfig rewrites ~/.claude/settings.json to point at the
// proxy: base URL -> proxy, credential -> placeholder, model overrides
// cleared (the proxy maps tiers per provider).
func writeClaudeTakeoverConfig(home string, p *core.Provider, proxyBaseURL string) error {
	path := filepath.Join(claudeConfigDir(home, p), "settings.json")
	existing := readJSONObject(path)
	env, _ := existing["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	authKey := claudeTakeoverAuthKey(home, p, env)
	for _, k := range managedClaudeEnvKeys {
		delete(env, k)
	}
	env["ANTHROPIC_BASE_URL"] = proxyBaseURL
	env[authKey] = ProxyManagedToken
	existing["env"] = env
	return writeJSONObject(path, existing)
}

func claudeTakeoverAuthKey(home string, p *core.Provider, env map[string]any) string {
	switch p.Meta.ClaudeAuthScheme {
	case "api_key":
		return "ANTHROPIC_API_KEY"
	case "auth_token":
		return "ANTHROPIC_AUTH_TOKEN"
	}
	if envHasValue(env, "ANTHROPIC_API_KEY") || claudePrimaryAPIKeyConfigured(home, p) {
		return "ANTHROPIC_API_KEY"
	}
	return "ANTHROPIC_AUTH_TOKEN"
}

func envHasValue(env map[string]any, key string) bool {
	if env == nil {
		return false
	}
	return strings.TrimSpace(stringValue(env[key])) != ""
}

func claudePrimaryAPIKeyConfigured(home string, p *core.Provider) bool {
	path := filepath.Join(claudeConfigDir(home, p), "config.json")
	config := readJSONObject(path)
	return strings.TrimSpace(stringValue(config["primaryApiKey"])) != ""
}

// writeCodexTakeoverConfig rewrites ~/.codex/config.toml so the agentnexus
// provider block targets the proxy with a placeholder bearer token. wire_api
// mirrors the active provider so the client calls the endpoint the proxy can
// pass through. auth.json is untouched.
func writeCodexTakeoverConfig(home string, p *core.Provider, proxyBaseURL string) error {
	dir := codexConfigDir(home, p)
	path := filepath.Join(dir, "config.toml")
	doc := readTOMLObject(path)
	doc["model_provider"] = codexModelProviderID
	if p.Model != "" {
		doc["model"] = p.Model
	}
	providers := ensureMap(doc, "model_providers")
	providers[codexModelProviderID] = map[string]any{
		"name":                      "AgentNexus Local Routing",
		"base_url":                  proxyBaseURL + "/v1",
		"wire_api":                  codexWireAPI(p),
		"experimental_bearer_token": ProxyManagedToken,
	}
	if err := syncCodexModelCatalog(dir, doc, codexCatalogModels(p)); err != nil {
		return err
	}
	return writeTOMLObject(path, doc)
}

// Service wraps Manager with the local routing proxy + takeover lifecycle.
// It implements core.ProviderManager; Switch hot-switches when the target
// tool is under takeover.
type Service struct {
	*Manager
	st    *store.Store
	proxy *ProxyServer
}

// NewService builds the provider service with a local routing proxy bound to
// addr (empty = DefaultProxyAddr).
func NewService(log *slog.Logger, st *store.Store, addr string) *Service {
	return &Service{
		Manager: NewManager(st),
		st:      st,
		proxy:   NewProxyServer(log, st, addr),
	}
}

// NewServiceWithProxy wires an explicit proxy (used by tests).
func NewServiceWithProxy(st *store.Store, proxy *ProxyServer) *Service {
	return &Service{Manager: NewManager(st), st: st, proxy: proxy}
}

var _ core.ProviderManager = (*Service)(nil)

// Proxy exposes the local routing server.
func (s *Service) Proxy() *ProxyServer { return s.proxy }

// Switch routes through the takeover-aware path: tools under takeover only
// flip the DB route (hot switch, live config keeps pointing at the proxy);
// everything else writes live config as usual.
func (s *Service) Switch(ctx context.Context, id, tool string) error {
	return s.switchRoute(ctx, core.ProviderRoute{Tool: tool, ProviderID: id}, false)
}

func (s *Service) SwitchRoute(ctx context.Context, route core.ProviderRoute) error {
	return s.switchRoute(ctx, route, true)
}

// SwitchRouteWithLocalTakeover applies a route with an explicit local-routing
// choice. When takeover is requested, the route is stored first so protocol
// converting providers do not have to pass direct live-config validation.
func (s *Service) SwitchRouteWithLocalTakeover(ctx context.Context, route core.ProviderRoute, enabled bool) error {
	canonical := liveConfigTool(route.Tool)
	if !takeoverTools[canonical] {
		return s.SwitchRoute(ctx, route)
	}
	if enabled {
		return s.switchRouteWithTakeover(ctx, route)
	}
	p, err := s.st.GetProvider(ctx, route.ProviderID)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("provider %q not found", route.ProviderID)
	}
	if err := validateDirectSwitch(core.ProviderWithRouteMeta(p, route.Meta), route.Tool); err != nil {
		return err
	}
	cfg, err := s.st.GetProxyToolConfig(ctx, canonical)
	if err != nil {
		return err
	}
	if cfg.Enabled {
		if err := s.DisableTakeover(ctx, canonical); err != nil {
			return err
		}
	}
	return s.Manager.SwitchRoute(ctx, route)
}

func (s *Service) switchRouteWithTakeover(ctx context.Context, route core.ProviderRoute) error {
	id, tool := route.ProviderID, route.Tool
	canonical := liveConfigTool(tool)
	if !takeoverTools[canonical] {
		return s.Manager.SwitchRoute(ctx, route)
	}
	p, err := s.st.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("provider %q not found", id)
	}
	effective := core.ProviderWithRouteMeta(p, route.Meta)
	cfg, err := s.st.GetProxyToolConfig(ctx, canonical)
	if err != nil {
		return err
	}
	if cfg.Enabled {
		if canonical == "claude-desktop" {
			token, err := s.st.GetOrCreateGatewayToken(ctx)
			if err != nil {
				return err
			}
			if err := WriteClaudeDesktopProxyProfile(effective, s.proxy.BaseURL()+"/claude-desktop", token); err != nil {
				return err
			}
		}
		return s.st.SetActiveProviderRoute(ctx, route)
	}
	if err := s.st.SetActiveProviderRoute(ctx, route); err != nil {
		return err
	}
	return s.EnableTakeover(ctx, canonical)
}

func (s *Service) switchRoute(ctx context.Context, route core.ProviderRoute, writeRouteMeta bool) error {
	id, tool := route.ProviderID, route.Tool
	canonical := liveConfigTool(tool)
	cfg, err := s.st.GetProxyToolConfig(ctx, canonical)
	if err == nil && cfg.Enabled && takeoverTools[canonical] {
		p, err := s.st.GetProvider(ctx, id)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("provider %q not found", id)
		}
		effective := core.ProviderWithRouteMeta(p, route.Meta)
		if canonical == "claude-desktop" {
			// The Desktop profile lists per-provider model routes, so a hot
			// switch still rewrites the profile (gateway URL/token stable).
			token, err := s.st.GetOrCreateGatewayToken(ctx)
			if err != nil {
				return err
			}
			if err := WriteClaudeDesktopProxyProfile(effective, s.proxy.BaseURL()+"/claude-desktop", token); err != nil {
				return err
			}
		}
		if writeRouteMeta {
			return s.st.SetActiveProviderRoute(ctx, route)
		}
		return s.st.SetActiveProvider(ctx, tool, id)
	}
	if writeRouteMeta && canonical == "claude-desktop" && route.Meta.ClaudeDesktopMode == "proxy" {
		if err := s.st.SetActiveProviderRoute(ctx, route); err != nil {
			return err
		}
		return s.EnableTakeover(ctx, canonical)
	}
	if writeRouteMeta {
		return s.Manager.SwitchRoute(ctx, route)
	}
	return s.Manager.Switch(ctx, id, tool)
}

// TakeoverStatus is the read model for /api/v1/proxy/status.
type TakeoverStatus struct {
	Running bool                    `json:"running"`
	BaseURL string                  `json:"base_url"`
	Tools   []store.ProxyToolConfig `json:"tools"`
}

// Status reports proxy + per-tool takeover state.
func (s *Service) Status(ctx context.Context) (TakeoverStatus, error) {
	persisted, err := s.st.ListProxyToolConfigs(ctx)
	if err != nil {
		return TakeoverStatus{}, err
	}
	byTool := map[string]store.ProxyToolConfig{}
	for _, cfg := range persisted {
		byTool[cfg.Tool] = cfg
	}
	tools := make([]store.ProxyToolConfig, 0, 3)
	for _, tool := range []string{"claudecode", "claude-desktop", "codex"} {
		if cfg, ok := byTool[tool]; ok {
			tools = append(tools, cfg)
		} else {
			cfg, err := s.st.GetProxyToolConfig(ctx, tool)
			if err != nil {
				return TakeoverStatus{}, err
			}
			tools = append(tools, cfg)
		}
	}
	return TakeoverStatus{
		Running: s.proxy.Running(),
		BaseURL: s.proxy.BaseURL(),
		Tools:   tools,
	}, nil
}

// SetToolConfig updates a tool's failover knobs without touching takeover.
func (s *Service) SetToolConfig(ctx context.Context, cfg store.ProxyToolConfig) error {
	cfg.Tool = liveConfigTool(cfg.Tool)
	if !takeoverTools[cfg.Tool] {
		return fmt.Errorf("tool %q does not support local routing", cfg.Tool)
	}
	current, err := s.st.GetProxyToolConfig(ctx, cfg.Tool)
	if err != nil {
		return err
	}
	cfg.Enabled = current.Enabled // takeover is only flipped via EnableTakeover/DisableTakeover
	return s.st.SetProxyToolConfig(ctx, cfg)
}

// EnableTakeover backs up the tool's live config and rewrites it to point at
// the local proxy, then marks the tool enabled and starts the proxy.
func (s *Service) EnableTakeover(ctx context.Context, tool string) error {
	canonical := liveConfigTool(tool)
	if !takeoverTools[canonical] {
		return fmt.Errorf("tool %q does not support local routing", tool)
	}
	active, err := s.activeProviderFor(ctx, canonical)
	if err != nil {
		return err
	}
	if active == nil {
		return fmt.Errorf("no active provider for %s: switch a provider first", canonical)
	}
	if err := s.proxy.Start(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	files, err := takeoverLiveFiles(home, canonical, active)
	if err != nil {
		return err
	}
	if _, exists, err := s.st.GetLiveBackup(ctx, canonical); err != nil {
		return err
	} else if !exists && !liveAlreadyProxied(files) {
		blob, err := snapshotLiveFiles(files)
		if err != nil {
			return err
		}
		if err := s.st.SaveLiveBackup(ctx, canonical, blob); err != nil {
			return err
		}
	}
	proxyURL := s.proxy.BaseURL()
	switch canonical {
	case "claudecode":
		if err := writeClaudeTakeoverConfig(home, active, proxyURL); err != nil {
			return err
		}
	case "codex":
		if err := writeCodexTakeoverConfig(home, active, proxyURL); err != nil {
			return err
		}
	case "claude-desktop":
		token, err := s.st.GetOrCreateGatewayToken(ctx)
		if err != nil {
			return err
		}
		if err := WriteClaudeDesktopProxyProfile(active, proxyURL+"/claude-desktop", token); err != nil {
			return err
		}
	}
	cfg, err := s.st.GetProxyToolConfig(ctx, canonical)
	if err != nil {
		return err
	}
	cfg.Enabled = true
	return s.st.SetProxyToolConfig(ctx, cfg)
}

// DisableTakeover restores the tool's original live config (backup first,
// provider rebuild as fallback) and stops the proxy when no tool needs it.
func (s *Service) DisableTakeover(ctx context.Context, tool string) error {
	canonical := liveConfigTool(tool)
	if !takeoverTools[canonical] {
		return fmt.Errorf("tool %q does not support local routing", tool)
	}
	blob, hasBackup, err := s.st.GetLiveBackup(ctx, canonical)
	if err != nil {
		return err
	}
	restored := false
	if hasBackup {
		if err := restoreLiveFiles(blob); err != nil {
			return err
		}
		if err := s.st.DeleteLiveBackup(ctx, canonical); err != nil {
			return err
		}
		restored = true
	}
	if !restored {
		// No backup: rebuild live from the active provider (SSOT), mirroring
		// cc-switch's restore_live_config_for_app_with_fallback.
		active, err := s.activeProviderFor(ctx, canonical)
		if err != nil {
			return err
		}
		if active != nil {
			if err := WriteLiveConfigForSwitch(canonical, active, nil); err != nil {
				return err
			}
		} else if canonical == "claude-desktop" {
			if err := RestoreClaudeDesktopOfficial(nil); err != nil {
				return err
			}
		}
	}
	cfg, err := s.st.GetProxyToolConfig(ctx, canonical)
	if err != nil {
		return err
	}
	cfg.Enabled = false
	if err := s.st.SetProxyToolConfig(ctx, cfg); err != nil {
		return err
	}
	return s.stopProxyIfIdle(ctx)
}

func (s *Service) stopProxyIfIdle(ctx context.Context) error {
	cfgs, err := s.st.ListProxyToolConfigs(ctx)
	if err != nil {
		return err
	}
	for _, cfg := range cfgs {
		if cfg.Enabled {
			return nil
		}
	}
	return s.proxy.Stop()
}

// RestoreProxyState starts the proxy at daemon boot when any tool is still
// marked as taken over (cc-switch restores takeover across restarts).
func (s *Service) RestoreProxyState(ctx context.Context) error {
	cfgs, err := s.st.ListProxyToolConfigs(ctx)
	if err != nil {
		return err
	}
	for _, cfg := range cfgs {
		if cfg.Enabled {
			return s.proxy.Start()
		}
	}
	return nil
}

// SetFailoverQueue toggles a provider's failover queue membership.
func (s *Service) SetFailoverQueue(ctx context.Context, id string, inQueue bool, sortIndex int) error {
	p, err := s.st.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("provider %q not found", id)
	}
	return s.st.SetFailoverQueue(ctx, id, inQueue, sortIndex)
}

func (s *Service) activeProviderFor(ctx context.Context, canonical string) (*core.Provider, error) {
	for _, key := range activeRouteKeys(canonical) {
		route, ok, err := s.st.ActiveProviderRoute(ctx, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		p, err := s.st.GetProvider(ctx, route.ProviderID)
		if err != nil {
			return nil, err
		}
		if p != nil {
			return core.ProviderWithRouteMeta(p, route.Meta), nil
		}
	}
	return nil, nil
}
