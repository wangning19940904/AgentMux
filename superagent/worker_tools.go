package superagent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

// Tools a worker can use besides reading and writing code. The router reads
// them from the roster so work that needs a browser or the desktop goes to a
// worker that has one.
const (
	WorkerToolBrowser     = "browser"
	WorkerToolComputerUse = "computer_use"
)

const workerToolsPath = "/api/v1/super-agents/workers/tools"

// WorkerTools is what a machine reports: for each installed framework, the
// tools its workers there can use.
type WorkerTools struct {
	Runtimes map[string][]string `json:"runtimes"`
	// Desktop says a person's desktop session is open on the machine, which
	// computer use needs.
	Desktop bool `json:"desktop"`
}

var (
	browserToolName  = regexp.MustCompile(`(?i)browser|playwright|puppeteer|chrom|devtools|selenium`)
	computerToolName = regexp.MustCompile(`(?i)computer|desktop|screen|peekaboo|macos-use|automation-mcp`)
	codexMCPHeader   = regexp.MustCompile(`(?m)^\s*\[mcp_servers\.("?)([^\]"]+)("?)\]`)
)

// browserCLIs drive a browser from a shell, which every framework's worker
// has, so finding one gives every framework the browser tool.
var browserCLIs = []string{"agent-browser", "playwright", "playwright-cli", "browser-use"}

type toolProbe struct {
	home     string
	goos     string
	lookPath func(string) (string, error)
	desktop  func() bool
}

// DetectWorkerTools looks at this machine: the MCP servers each framework is
// configured with, browser CLIs on PATH and whether a desktop session is open.
func DetectWorkerTools(runtimes []string) WorkerTools {
	home, _ := os.UserHomeDir()
	return toolProbe{home: home, goos: runtime.GOOS, lookPath: exec.LookPath, desktop: desktopSession}.detect(runtimes)
}

func (p toolProbe) detect(runtimes []string) WorkerTools {
	result := WorkerTools{Runtimes: map[string][]string{}, Desktop: p.desktop()}
	sharedBrowser := slices.ContainsFunc(browserCLIs, func(name string) bool {
		_, err := p.lookPath(name)
		return err == nil
	})
	for _, runtimeID := range runtimes {
		var tools []string
		names := p.mcpServers(runtimeID)
		if sharedBrowser || slices.ContainsFunc(names, browserToolName.MatchString) {
			tools = append(tools, WorkerToolBrowser)
		}
		if result.Desktop && slices.ContainsFunc(names, computerToolName.MatchString) {
			tools = append(tools, WorkerToolComputerUse)
		}
		if len(tools) > 0 {
			result.Runtimes[runtimeID] = tools
		}
	}
	return result
}

// mcpServers lists the names of the MCP servers a framework loads for every
// project, from its own user-level configuration.
func (p toolProbe) mcpServers(runtimeID string) []string {
	read := func(parts ...string) []byte {
		data, _ := os.ReadFile(filepath.Join(append([]string{p.home}, parts...)...))
		return data
	}
	jsonKeys := func(data []byte) []string {
		var parsed struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(data, &parsed) != nil {
			return nil
		}
		names := make([]string, 0, len(parsed.MCPServers))
		for name := range parsed.MCPServers {
			names = append(names, name)
		}
		return names
	}
	switch runtimeID {
	case "claudecode":
		return append(jsonKeys(read(".claude.json")), jsonKeys(read(".claude", "settings.json"))...)
	case "cursor":
		return jsonKeys(read(".cursor", "mcp.json"))
	case "codex":
		var names []string
		for _, match := range codexMCPHeader.FindAllSubmatch(read(".codex", "config.toml"), -1) {
			names = append(names, strings.TrimSpace(string(match[2])))
		}
		return names
	case "traecli":
		return jsonKeys(read(".trae", "mcp.json"))
	}
	return nil
}

// desktopSession reports whether this user has a graphical session a worker
// could see and control.
func desktopSession() bool {
	switch runtime.GOOS {
	case "darwin":
		info, err := os.Stat("/dev/console")
		if err != nil {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		return ok && int(stat.Uid) == os.Getuid()
	case "linux":
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return false
}
