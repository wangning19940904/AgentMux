// Package framework defines the catalog of agent frameworks AgentNexus can run,
// plus detection and installation of the SDK-based ones.
//
// A framework is either:
//
//   - a "cli" framework: an external coding CLI (claude, codex, gemini, ...)
//     driven as a subprocess. Detection is exec.LookPath on its binary; these
//     are always compiled into the binary as agent adapters.
//   - a "sdk" framework: a library (claude-agent-sdk, openai-agents) hosted by
//     the Node sidecar worker. Detection reads the sidecar's node_modules;
//     installation runs `npm install <pkg>` into the sidecar dir.
//
// The catalog is the single source of truth shared by the HTTP layer
// (server/frameworks.go) and the dynamic SDK agent registration
// (agent/sdkagent).
package framework

// KindType classifies how a framework is hosted.
type KindType string

const (
	// KindCLI is a subprocess CLI framework (always available if its binary is
	// on PATH).
	KindCLI KindType = "cli"
	// KindSDK is a library framework hosted by the Node sidecar worker.
	KindSDK KindType = "sdk"
)

// Spec is one catalog entry describing a framework.
type Spec struct {
	// Kind is the stable framework identifier, also the agent registry name
	// (e.g. "claudecode", "claude-agent-sdk").
	Kind string `json:"kind"`
	// Display is the human-facing label.
	Display string `json:"display"`
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
	// UpdateCommand is the catalog-owned command used to update an installed CLI.
	UpdateCommand []string `json:"-"`
	// UpdateSupported reports whether this framework can be checked and updated
	// from the Console.
	UpdateSupported bool `json:"update_supported"`
	// EnvRequired lists environment variables the framework needs at runtime.
	EnvRequired []string `json:"env_required,omitempty"`
	// Supported is false for frameworks that are catalogued but not yet
	// runnable (shown as "coming soon" and not installable).
	Supported bool `json:"supported"`
	// Note is an optional short description shown in the UI.
	Note string `json:"note,omitempty"`
}

// catalog is the built-in framework registry.
var catalog = []Spec{
	{
		Kind: "claudecode", Display: "Claude Code", KindType: KindCLI,
		Bin: "claude", VersionArgs: []string{"--version"},
		NPMPackage: "@anthropic-ai/claude-code", UpdateCommand: []string{"claude", "update"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true,
		EnvRequired: []string{"ANTHROPIC_API_KEY"}, Supported: true,
		Note: "Anthropic Claude Code CLI",
	},
	{
		Kind: "codex", Display: "Codex", KindType: KindCLI,
		Bin: "codex", VersionArgs: []string{"--version"},
		NPMPackage: "@openai/codex", UpdateCommand: []string{"codex", "update"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true,
		EnvRequired: []string{"OPENAI_API_KEY"}, Supported: true,
		Note: "OpenAI Codex CLI",
	},
	{
		Kind: "cursor", Display: "Cursor Agent", KindType: KindCLI,
		Bin: "cursor-agent", VersionArgs: []string{"--version"},
		InstallCommand:   []string{"bash", "-c", "curl https://cursor.com/install -fsS | bash"},
		InstallSupported: true,
		LatestURL:        "https://cursor.com/install", UpdateCommand: []string{"cursor-agent", "update"},
		UpdateSupported: true, Supported: true, Note: "Cursor Agent CLI",
	},
	{
		Kind: "gemini", Display: "Gemini CLI", KindType: KindCLI,
		Bin: "gemini", VersionArgs: []string{"--version"},
		NPMPackage: "@google/gemini-cli", UpdateCommand: []string{"npm", "install", "-g", "@google/gemini-cli@latest"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true,
		EnvRequired: []string{"GEMINI_API_KEY"}, Supported: true,
		Note: "Google Gemini CLI",
	},
	{
		Kind: "qoder", Display: "Qoder", KindType: KindCLI,
		Bin: "qodercli", VersionArgs: []string{"--version"},
		NPMPackage: "@qoder-ai/qodercli", UpdateCommand: []string{"qodercli", "update"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, Supported: true, Note: "Qoder CLI",
	},
	{
		Kind: "opencode", Display: "OpenCode", KindType: KindCLI,
		Bin: "opencode", VersionArgs: []string{"--version"},
		NPMPackage: "opencode-ai", UpdateCommand: []string{"opencode", "upgrade"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, Supported: true, Note: "OpenCode CLI",
	},
	{
		Kind: "iflow", Display: "iFlow", KindType: KindCLI,
		Bin: "iflow", VersionArgs: []string{"--version"},
		NPMPackage: "@iflow-ai/iflow-cli", UpdateCommand: []string{"npm", "install", "-g", "@iflow-ai/iflow-cli@latest"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, Supported: true, Note: "iFlow CLI",
	},
	{
		Kind: "kimi", Display: "Kimi", KindType: KindCLI,
		Bin: "kimi", VersionArgs: []string{"--version"},
		NPMPackage: "@moonshot-ai/kimi-code", UpdateCommand: []string{"npm", "install", "-g", "@moonshot-ai/kimi-code@latest"},
		InstallSupported: true, InstallRequiresNPM: true,
		UpdateSupported: true, Supported: true, Note: "Kimi Code CLI",
	},
	{
		Kind: "claude-agent-sdk", Display: "Claude Agent SDK", KindType: KindSDK,
		Language: "node", Packages: []string{"@anthropic-ai/claude-agent-sdk"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true,
		EnvRequired: []string{"ANTHROPIC_API_KEY"}, Supported: true,
		Note: "Anthropic Agent SDK (Node) hosted by the sidecar worker",
	},
	{
		Kind: "openai-agents", Display: "OpenAI Agents SDK", KindType: KindSDK,
		Language: "node", Packages: []string{"@openai/agents", "zod"},
		InstallSupported: true, InstallRequiresNPM: true, UpdateSupported: true,
		EnvRequired: []string{"OPENAI_API_KEY"}, Supported: true,
		Note: "OpenAI Agents SDK (Node) hosted by the sidecar worker",
	},
	{
		Kind: "deepagents", Display: "DeepAgents", KindType: KindSDK,
		Language: "python", Packages: []string{"deepagents", "langgraph"},
		EnvRequired: []string{"OPENAI_API_KEY"}, Supported: false,
		Note: "LangGraph DeepAgents (Python) — requires a Python runtime; experimental",
	},
}

// Catalog returns a copy of the framework catalog.
func Catalog() []Spec {
	out := make([]Spec, len(catalog))
	copy(out, catalog)
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
