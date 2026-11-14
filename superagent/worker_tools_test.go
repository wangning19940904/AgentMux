package superagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func writeHomeFile(t *testing.T, home, name, content string) {
	t.Helper()
	path := filepath.Join(home, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noCLIs(string) (string, error) { return "", errors.New("not found") }

func TestDetectWorkerToolsReadsEachFrameworksMCPServers(t *testing.T) {
	home := t.TempDir()
	writeHomeFile(t, home, ".claude.json", `{"mcpServers":{"playwright":{},"computer-use":{}}}`)
	writeHomeFile(t, home, ".codex/config.toml", "model = \"x\"\n[mcp_servers.chrome-devtools]\ncommand = \"npx\"\n[mcp_servers.github]\n")
	writeHomeFile(t, home, ".cursor/mcp.json", `{"mcpServers":{"linear":{}}}`)

	probe := toolProbe{home: home, lookPath: noCLIs, desktop: func() bool { return true }}
	got := probe.detect([]string{"claudecode", "codex", "cursor"}).Runtimes
	if !slices.Equal(got["claudecode"], []string{WorkerToolBrowser, WorkerToolComputerUse}) {
		t.Fatalf("claudecode tools = %v", got["claudecode"])
	}
	if !slices.Equal(got["codex"], []string{WorkerToolBrowser}) {
		t.Fatalf("codex tools = %v", got["codex"])
	}
	if _, has := got["cursor"]; has {
		t.Fatalf("cursor has no browser or desktop server: %v", got["cursor"])
	}
}

func TestComputerUseNeedsADesktopSession(t *testing.T) {
	home := t.TempDir()
	writeHomeFile(t, home, ".claude.json", `{"mcpServers":{"computer-use":{}}}`)
	probe := toolProbe{home: home, lookPath: noCLIs, desktop: func() bool { return false }}
	if got := probe.detect([]string{"claudecode"}); got.Desktop || len(got.Runtimes["claudecode"]) != 0 {
		t.Fatalf("a headless machine cannot offer computer use: %+v", got)
	}
}

func TestABrowserCLIGivesEveryFrameworkTheBrowser(t *testing.T) {
	probe := toolProbe{home: t.TempDir(), desktop: func() bool { return false }, lookPath: func(name string) (string, error) {
		if name == "agent-browser" {
			return "/usr/local/bin/agent-browser", nil
		}
		return "", errors.New("not found")
	}}
	got := probe.detect([]string{"codex", "traecli"}).Runtimes
	if !slices.Equal(got["codex"], []string{WorkerToolBrowser}) || !slices.Equal(got["traecli"], []string{WorkerToolBrowser}) {
		t.Fatalf("tools = %v", got)
	}
}

type toolMachineStub struct {
	*machineStub
	tools map[string]WorkerTools
}

func (m toolMachineStub) Tools(_ context.Context, hostID string) (WorkerTools, error) {
	tools, ok := m.tools[hostID]
	if !ok {
		return WorkerTools{}, ErrMachineOutdated
	}
	return tools, nil
}

func TestRosterListsTheToolsEachMachineReports(t *testing.T) {
	h := newHarness(t)
	stub := toolMachineStub{machineStub: h.withMachines(), tools: map[string]WorkerTools{
		"dev_1": {Runtimes: map[string][]string{"dsh": {WorkerToolBrowser, WorkerToolComputerUse}}, Desktop: true},
	}}
	h.service.workers = stub
	h.service.detectTools = func([]string) WorkerTools {
		return WorkerTools{Runtimes: map[string][]string{"dsh": {WorkerToolBrowser}}}
	}
	h.store.mu.Lock()
	h.agent.WorkerRuntimes = []string{"dsh", "codex"}
	h.agent.WorkerMachines = append(h.agent.WorkerMachines, core.SuperAgentMachine{HostID: "dev_2"})
	h.store.agents[h.agent.ID] = h.agent
	h.store.mu.Unlock()

	statuses := h.service.machineStatuses(context.Background(), h.agent)
	var prompt strings.Builder
	writeRouterContext(&prompt, h.agent, nil, routerContext{Machines: statuses, LocalTools: h.service.localTools(h.agent.WorkerRuntimes)})
	text := prompt.String()
	entry := func(id string) string {
		_, rest, found := strings.Cut(text, `"id": "`+id+`"`)
		if !found {
			t.Fatalf("no roster entry %s:\n%s", id, text)
		}
		body, _, _ := strings.Cut(rest, `"available"`)
		return body
	}
	if body := entry("auto:dsh@dev_1"); !strings.Contains(body, `"browser"`) || !strings.Contains(body, `"computer_use"`) {
		t.Fatalf("dsh on studio should list both tools: %s", body)
	}
	if body := entry("auto:codex@dev_1"); strings.Contains(body, `"tools"`) {
		t.Fatalf("codex on studio reported no tools: %s", body)
	}
	if body := entry("auto:codex@dev_2"); strings.Contains(body, `"tools"`) {
		t.Fatalf("an older machine's tools are unknown: %s", body)
	}
	if body := entry("auto:dsh"); !strings.Contains(body, `"browser"`) || strings.Contains(body, `"computer_use"`) {
		t.Fatalf("a local dsh worker has the local browser only: %s", body)
	}
	if !strings.Contains(defaultRouterActions, `"computer_use" sees and controls`) {
		t.Fatalf("the router is not told what tools mean")
	}
}
