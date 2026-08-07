package native

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	integrationassets "github.com/wangning19940904/AgentMux/integrations"
)

type Options struct {
	HomeDir      string
	AssetsDir    string
	HelperSource string
	Runner       Runner
	Now          func() time.Time
	Random       io.Reader
}

type Manager struct {
	home         string
	assets       string
	helperSource string
	runner       Runner
	now          func() time.Time
	random       io.Reader
}

type hostSpec struct {
	host            Host
	binary          string
	root            string
	pluginRoot      string
	manifestPath    string
	hooksPath       string
	marketplacePath string
}

type assetInfo struct {
	Version            string
	PluginSHA          string
	MarketplaceSHA     string
	HandlerFingerprint string
}

// NewManager creates a manager rooted at HomeDir. AssetsDir is the directory
// containing the repo-local codex/ and claude/ marketplace roots.
func NewManager(options Options) (*Manager, error) {
	home := strings.TrimSpace(options.HomeDir)
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	assets := strings.TrimSpace(options.AssetsDir)
	if assets == "" {
		assets = DefaultAssetsDir()
	}
	if assets == "" {
		var err error
		assets, err = integrationassets.MaterializeMarketplaces(home, PluginVersion)
		if err != nil {
			return nil, fmt.Errorf("materialize bundled native integration assets: %w", err)
		}
	}
	runner := options.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	return &Manager{
		home:         filepath.Clean(home),
		assets:       filepath.Clean(assets),
		helperSource: strings.TrimSpace(options.HelperSource),
		runner:       runner,
		now:          now,
		random:       random,
	}, nil
}

// DefaultAssetsDir locates source-tree or packaged marketplace assets without
// reading or changing any user configuration.
func DefaultAssetsDir() string {
	if configured := strings.TrimSpace(os.Getenv("AGENTMUX_INTEGRATION_ASSETS")); configured != "" {
		return configured
	}
	var candidates []string
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(dir, "integrations", "marketplaces"),
			filepath.Join(dir, "..", "share", "agentmux", "integrations", "marketplaces"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "integrations", "marketplaces"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return filepath.Clean(candidate)
		}
	}
	return ""
}

func (m *Manager) spec(host Host) (hostSpec, error) {
	if !host.Valid() {
		return hostSpec{}, fmt.Errorf("unsupported native integration host %q", host)
	}
	root := filepath.Join(m.assets, string(host))
	pluginRoot := filepath.Join(root, "plugins", PluginID)
	spec := hostSpec{
		host:       host,
		root:       root,
		pluginRoot: pluginRoot,
		hooksPath:  filepath.Join(pluginRoot, "hooks", "hooks.json"),
	}
	if host == HostCodex {
		spec.binary = "codex"
		spec.manifestPath = filepath.Join(pluginRoot, ".codex-plugin", "plugin.json")
		spec.marketplacePath = filepath.Join(root, ".agents", "plugins", "marketplace.json")
	} else {
		spec.binary = "claude"
		spec.manifestPath = filepath.Join(pluginRoot, ".claude-plugin", "plugin.json")
		spec.marketplacePath = filepath.Join(root, ".claude-plugin", "marketplace.json")
	}
	return spec, nil
}

func (m *Manager) inspectAssets(spec hostSpec) (assetInfo, error) {
	manifest, err := os.ReadFile(spec.manifestPath)
	if err != nil {
		return assetInfo{}, fmt.Errorf("read %s manifest: %w", spec.host, err)
	}
	var parsed struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &parsed); err != nil {
		return assetInfo{}, fmt.Errorf("parse %s manifest: %w", spec.host, err)
	}
	if parsed.Name != PluginID || strings.TrimSpace(parsed.Version) == "" {
		return assetInfo{}, fmt.Errorf("invalid %s plugin identity %q version %q", spec.host, parsed.Name, parsed.Version)
	}
	pluginSHA, err := hashTree(spec.pluginRoot)
	if err != nil {
		return assetInfo{}, err
	}
	marketplaceSHA, err := fileHash(spec.marketplacePath)
	if err != nil || marketplaceSHA == "" {
		if err == nil {
			err = fmt.Errorf("marketplace manifest is missing: %s", spec.marketplacePath)
		}
		return assetInfo{}, err
	}
	fingerprint, err := handlerFingerprint(spec.hooksPath)
	if err != nil {
		return assetInfo{}, err
	}
	return assetInfo{
		Version:            parsed.Version,
		PluginSHA:          pluginSHA,
		MarketplaceSHA:     marketplaceSHA,
		HandlerFingerprint: fingerprint,
	}, nil
}

func (m *Manager) helperTarget() string {
	return filepath.Join(m.home, ".agentmux", "bin", "agentmux-hook")
}

func (m *Manager) resolvedHelperSource() string {
	if m.helperSource != "" {
		return filepath.Clean(m.helperSource)
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "agentmux-hook")
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func (m *Manager) statePath(host Host) string {
	return filepath.Join(m.home, ".agentmux", "integrations", string(host)+".json")
}

func (m *Manager) lockPath() string {
	return filepath.Join(m.home, ".agentmux", "integrations", ".native-integrations.lock")
}

func (m *Manager) env() []string { return commandEnv(m.home) }
