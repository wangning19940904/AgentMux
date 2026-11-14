package superagent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/wangning19940904/AgentMux/core"
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
	// Notes say, for each framework and tool, how a worker reaches it, so a
	// worker that is given such work knows what to use.
	Notes map[string]map[string]string `json:"notes,omitempty"`
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
	result := WorkerTools{Runtimes: map[string][]string{}, Notes: map[string]map[string]string{}, Desktop: p.desktop()}
	browserCLI := ""
	for _, name := range browserCLIs {
		if _, err := p.lookPath(name); err == nil {
			browserCLI = name
			break
		}
	}
	for _, runtimeID := range runtimes {
		var tools []string
		notes := map[string]string{}
		names := p.mcpServers(runtimeID)
		slices.Sort(names)
		var browsers []string
		for _, name := range names {
			if browserToolName.MatchString(name) {
				browsers = append(browsers, "the "+name+" MCP server")
			}
		}
		if browserCLI != "" {
			browsers = append(browsers, "the "+browserCLI+" command-line tool (run `"+browserCLI+" --help` first)")
		}
		if len(browsers) > 0 {
			tools = append(tools, WorkerToolBrowser)
			notes[WorkerToolBrowser] = strings.Join(browsers, ", or ")
		}
		if result.Desktop {
			var desktops []string
			for _, name := range names {
				if computerToolName.MatchString(name) {
					desktops = append(desktops, "the "+name+" MCP server")
				}
			}
			if len(desktops) > 0 {
				tools = append(tools, WorkerToolComputerUse)
				notes[WorkerToolComputerUse] = strings.Join(desktops, ", or ")
			}
		}
		if len(tools) > 0 {
			result.Runtimes[runtimeID], result.Notes[runtimeID] = tools, notes
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
			name := strings.TrimSpace(string(match[2]))
			if match[1] == nil || len(match[1]) == 0 {
				name, _, _ = strings.Cut(name, ".") // [mcp_servers.x.env] belongs to x
			}
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
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

// toolNotesLine tells a worker which tools its machine gives it and how to
// reach them, or "" when it has none.
func toolNotesLine(notes map[string]string) string {
	if len(notes) == 0 {
		return ""
	}
	var parts []string
	for _, tool := range []string{WorkerToolBrowser, WorkerToolComputerUse} {
		if how := notes[tool]; how != "" {
			label := map[string]string{WorkerToolBrowser: "a web browser", WorkerToolComputerUse: "this machine's desktop"}[tool]
			parts = append(parts, "for "+label+", use "+how)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "TOOLS ON YOUR MACHINE: " + strings.Join(parts, "; ") + ". Prefer these to other browser or desktop tools your framework may list, which may not work here."
}

// workerToolNotes finds how the worker an action names reaches its tools:
// on this machine, or by asking the machine the worker runs on.
func (s *Service) workerToolNotes(ctx context.Context, agent core.SuperAgent, workerID string) map[string]string {
	runtimeID, pathID, ok := parseAutoWorkerID(workerID)
	if !ok {
		return nil
	}
	hostID := ""
	if pathID != "" {
		machine, listed := agentMachine(agent, pathID)
		if !listed {
			return nil
		}
		hostID = machine.HostID
	}
	if hostID == "" {
		if s.detectTools == nil {
			return nil
		}
		return s.detectTools([]string{runtimeID}).Notes[runtimeID]
	}
	tooled, ok := s.machineFleet().(MachineToolsFleet)
	if !ok {
		return nil
	}
	toolsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tools, err := tooled.Tools(toolsCtx, hostID)
	if err != nil {
		return nil
	}
	return tools.Notes[runtimeID]
}
