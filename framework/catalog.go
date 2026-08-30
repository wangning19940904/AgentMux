// Package framework defines the catalog of agent frameworks AgentMux can run,
// plus their detection, installation, update, and authentication metadata.
//
// A framework is either:
//
//   - a "cli" framework: an external coding CLI (claude, codex, gemini, ...)
//     driven as a subprocess. Detection is exec.LookPath on its binary; these
//     are always compiled into the binary as agent adapters.
//   - an "sdk" framework: a catalogued SDK integration. SDK entries can remain
//     visible as coming-soon items without shipping a runtime host.
//
// The catalog is the single source of truth shared by the HTTP layer and the
// compiled CLI adapters.
package framework

// KindType classifies how a framework is hosted.
type KindType string

const (
	// KindCLI is a subprocess CLI framework (always available if its binary is
	// on PATH).
	KindCLI KindType = "cli"
	// KindSDK is an SDK framework. Current SDK entries are catalog-only.
	KindSDK KindType = "sdk"
)

// Spec is one catalog entry describing a framework.
type Spec struct {
	// Kind is the stable framework identifier, also the agent registry name
	// (e.g. "claudecode", "traecli").
	Kind string `json:"kind"`
	// Display is the human-facing label.
	Display string `json:"display"`
	// Company is the publisher shown in the compact framework catalogue.
	Company string `json:"company"`
	// KindType is "cli" or "sdk".
	KindType KindType `json:"kind_type"`
	// Language is the runtime language ("node", "python", or "" for CLIs).
	Language string `json:"language"`
	// Packages are the npm packages that back an SDK framework.
	Packages []string `json:"packages,omitempty"`
	// Bin is the executable name for a CLI framework.
	Bin string `json:"bin,omitempty"`
	// VersionArgs are passed to Bin to read an installed CLI version.
	VersionArgs []string `json:"-"`
	// NPMPackage is the registry package used to resolve the latest CLI version.
	NPMPackage string `json:"-"`
	// InstallCommand is the catalog-owned command used when a CLI is not
	// distributed through npm. Callers can select a framework, but cannot
	// provide or alter the command that is executed.
	InstallCommand []string `json:"-"`
	// InstallSupported reports whether this framework can be installed from the
	// Console. InstallRequiresNPM lets the UI disable that action when the host
	// prerequisites are missing.
	InstallSupported   bool `json:"install_supported"`
	InstallRequiresNPM bool `json:"install_requires_npm"`
	// LatestURL is an official text endpoint whose response contains the latest
	// CLI version. It is used for CLIs that are not distributed through npm.
	LatestURL string `json:"-"`
	// ExactLatest selects exact latest-version equality instead of ordered
	// semantic comparison. Native date+hash builds such as Cursor need this.
	ExactLatest bool `json:"-"`
	// UpdateCommand is the catalog-owned command used to update an installed CLI.
	UpdateCommand []string `json:"-"`
	// UpdateSupported reports whether this framework can be checked and updated
	// from the Console.
	UpdateSupported bool `json:"update_supported"`
	// UninstallCommand is the catalog-owned command used to remove a framework.
	// It deliberately preserves user configuration and session data when the
	// upstream CLI exposes that option.
	UninstallCommand []string `json:"-"`
	// UninstallSupported reports whether AgentMux can safely remove the CLI.
	UninstallSupported bool `json:"uninstall_supported"`
	// EnvRequired lists environment variables the framework needs at runtime.
	EnvRequired []string `json:"env_required,omitempty"`
	// Supported is false for frameworks that are catalogued but not yet
	// runnable (shown as "coming soon" and not installable).
	Supported bool `json:"supported"`
	// Note is an optional short description shown in the UI.
	Note string `json:"note,omitempty"`
	// InternalOnly marks frameworks that depend on ByteDance accounts, network,
	// or distribution endpoints. Installation requires explicit acknowledgement.
	InternalOnly bool `json:"internal_only,omitempty"`
	// InstallPlatforms limits automatic installation by GOOS. An installed CLI
	// remains detectable and routable on other platforms.
	InstallPlatforms []string `json:"install_platforms,omitempty"`
	// Hidden keeps compatibility for persisted framework references while
	// excluding the framework from user-facing catalogs and runtime selectors.
	Hidden bool `json:"-"`
}

// catalog is the built-in framework registry.
var catalog = []Spec{
	{
		Kind: "claudecode", Display: "Claude Code", Company: "Anthropic", KindType: KindCLI,
		Bin: "claude", VersionArgs: []string{"--version"},
		NPMPackage: "@anthropic-ai/claude-code", UpdateCommand: []string{"claude", "update"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true, UninstallSupported: true,
		EnvRequired: []string{"ANTHROPIC_API_KEY"}, Supported: true,
		Note: "Anthropic Claude Code CLI",
	},
	{
		Kind: "codex", Display: "Codex", Company: "OpenAI", KindType: KindCLI,
		Bin: "codex", VersionArgs: []string{"--version"},
		NPMPackage: "@openai/codex", UpdateCommand: []string{"codex", "update"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true, UninstallSupported: true,
		EnvRequired: []string{"OPENAI_API_KEY"}, Supported: true,
		Note: "OpenAI Codex CLI",
	},
	{
		Kind: "cursor", Display: "Cursor Agent", Company: "Cursor", KindType: KindCLI,
		Bin: "cursor-agent", VersionArgs: []string{"--version"},
		InstallCommand:   []string{"bash", "-c", "curl https://cursor.com/install -fsS | bash"},
		InstallSupported: true,
		// Cursor's native self-updater can reject a headless service with an
		// unauthenticated error even though the official installer is publicly
		// downloadable. Re-running the installer performs the same atomic version
		// switch without depending on an interactive Cursor login.
		LatestURL: "https://cursor.com/install", UpdateCommand: []string{"bash", "-c", "curl https://cursor.com/install -fsS | bash"},
		ExactLatest: true, UpdateSupported: true, Supported: true, Note: "Cursor Agent CLI",
	},
	{
		Kind: "gemini", Display: "Gemini CLI", Company: "Google", KindType: KindCLI,
		Bin: "gemini", VersionArgs: []string{"--version"},
		NPMPackage: "@google/gemini-cli", UpdateCommand: []string{"npm", "install", "-g", "@google/gemini-cli@latest"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true, UninstallSupported: true,
		EnvRequired: []string{"GEMINI_API_KEY"}, Supported: true,
		Note: "Google Gemini CLI",
	},
	{
		Kind: "qoder", Display: "Qoder", Company: "Alibaba", KindType: KindCLI,
		Bin: "qodercli", VersionArgs: []string{"--version"},
		NPMPackage: "@qoder-ai/qodercli", UpdateCommand: []string{"qodercli", "update"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, UninstallSupported: true, Supported: true, Note: "Qoder CLI",
	},
	{
		Kind: "opencode", Display: "OpenCode", Company: "Anomaly", KindType: KindCLI,
		Bin: "opencode", VersionArgs: []string{"--version"},
		NPMPackage: "opencode-ai", UpdateCommand: []string{"opencode", "upgrade"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, UninstallCommand: []string{"opencode", "uninstall", "--keep-config", "--keep-data", "--force"},
		UninstallSupported: true, Supported: true, Note: "OpenCode CLI",
	},
	{
		Kind: "traecli", Display: "TRAE CLI", Company: "ByteDance", KindType: KindCLI,
		Bin: "traecli", VersionArgs: []string{"--version"},
		InstallCommand: []string{
			"bash", "-c",
			"curl -fsSL https://code.byted.org/api/tos-proxy/download/traex_install.sh | TRAEX_INSTALL_ASSUME_YES=1 sh",
		},
		LatestURL:        "https://code.byted.org/api/tos-proxy/download/traex_latest__stable.json",
		UpdateCommand:    []string{"traecli", "update"},
		InstallSupported: true, UpdateSupported: true, Supported: true,
		InternalOnly: true, InstallPlatforms: []string{"darwin", "linux"},
		Note: "ByteDance internal TRAE coding-agent CLI",
	},
	{
		Kind: "iflow", Display: "iFlow", Company: "iFlow AI", KindType: KindCLI,
		Bin: "iflow", VersionArgs: []string{"--version"},
		NPMPackage: "@iflow-ai/iflow-cli", UpdateCommand: []string{"npm", "install", "-g", "@iflow-ai/iflow-cli@latest"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, UninstallSupported: true, Supported: true, Note: "iFlow CLI", Hidden: true,
	},
	{
		Kind: "kimi", Display: "Kimi", Company: "Moonshot AI", KindType: KindCLI,
		Bin: "kimi", VersionArgs: []string{"--version"},
		NPMPackage: "@moonshot-ai/kimi-code", UpdateCommand: []string{"npm", "install", "-g", "@moonshot-ai/kimi-code@latest"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, UninstallSupported: true, Supported: true, Note: "Kimi Code CLI", Hidden: true,
	},
	{
		Kind: "deepagents", Display: "DeepAgents", Company: "LangChain", KindType: KindSDK,
		Language: "python", Packages: []string{"deepagents", "langgraph"},
		EnvRequired: []string{"OPENAI_API_KEY"}, Supported: false,
		Note: "LangGraph DeepAgents (Python) — requires a Python runtime; experimental",
	},
}

// Catalog returns a copy of the user-visible framework catalog.
func Catalog() []Spec {
	out := make([]Spec, 0, len(catalog))
	for _, spec := range catalog {
		if !spec.Hidden {
			out = append(out, spec)
		}
	}
	return out
}

// Lookup returns the spec for a kind, or false if unknown.
func Lookup(kind string) (Spec, bool) {
	for _, s := range catalog {
		if s.Kind == kind {
			return s, true
		}
	}
	return Spec{}, false
}
