package superagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func (h *harness) setLocalAgent(agent core.AgentInstance) {
	h.t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	h.store.local[agent.ID] = agent
}

func (h *harness) ticketFolder(ticket core.SuperAgentTicket) string {
	h.t.Helper()
	dir, err := h.service.ticketDir(ticket.SuperAgentID, ticket.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return dir
}

func readTicketRecord(t *testing.T, dir string) ticketRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ticketRecordFile))
	if err != nil {
		t.Fatal(err)
	}
	var record ticketRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRouterWorksInTheSuperAgentHome(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	home, err := h.service.homeDir(h.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	router, _ := h.store.GetAgentInstance(ctx, "router")
	if router.WorkDir != home || router.WorkspaceMode != "shared" {
		t.Fatalf("router directory = %q (%s), want the home %q", router.WorkDir, router.WorkspaceMode, home)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		if guide := readText(t, filepath.Join(home, name)); !strings.Contains(guide, "tickets/<ticket id>/") {
			t.Fatalf("%s does not describe the home:\n%s", name, guide)
		}
	}
	// The operator's notes in the guide survive the next save.
	if err := os.WriteFile(filepath.Join(home, "CLAUDE.md"), []byte("my notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Save(ctx, h.agent); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(home, "CLAUDE.md")); got != "my notes" {
		t.Fatalf("guide was overwritten: %q", got)
	}
}

func TestTicketFolderKeepsTheTicketsRecord(t *testing.T) {
	h := newHarness(t)
	coderDir := t.TempDir()
	h.setLocalAgent(core.AgentInstance{ID: "coder", Name: "coder", Enabled: true, WorkDir: coderDir})
	h.workers.run = func(context.Context, workerCall) (string, error) { return "fixed the bug", nil }
	h.ingest("alice", "github:o/r#1", "please fix it", true)
	h.settle()
	h.ingest("alice", "github:o/r#1", "also add a test", true)
	h.router.answer = func(core.InvocationRequest) (string, error) {
		ticket := h.store.allTickets()[0]
		return `{"decision":"attach","ticket_id":"` + ticket.ID + `"}`, nil
	}
	h.settle()

	tickets := h.store.allTickets()
	if len(tickets) != 1 || tickets[0].Status != core.SuperAgentTicketReady {
		t.Fatalf("tickets = %+v", tickets)
	}
	dir := h.ticketFolder(tickets[0])
	record := readTicketRecord(t, dir)
	if record.ID != tickets[0].ID || record.Status != core.SuperAgentTicketReady || record.Worker.AgentID != "coder" || record.WorkDir != coderDir {
		t.Fatalf("record = %+v", record)
	}
	inputs := readText(t, filepath.Join(dir, ticketInputsFile))
	if !strings.Contains(inputs, "look closely") || !strings.Contains(inputs, "also add a test") || strings.Count(inputs, "\n## ") != 1 {
		t.Fatalf("inputs.md must hold both turns' input:\n%s", inputs)
	}
	if result := readText(t, filepath.Join(dir, ticketResultFile)); strings.Count(result, "fixed the bug") != 2 {
		t.Fatalf("result.md must hold both turns' answers:\n%s", result)
	}
	// A worker that has a directory of its own keeps it; work/ links there.
	if target, err := os.Readlink(filepath.Join(dir, ticketWorkDir)); err != nil || target != coderDir {
		t.Fatalf("work link = %q, %v", target, err)
	}
	for _, call := range h.workers.calls {
		if call.dir != "" {
			t.Fatalf("a worker with its own directory must not be moved: %+v", call)
		}
	}
}

func TestStoppedTicketRecordsItsStatus(t *testing.T) {
	h := newHarness(t)
	block := make(chan struct{})
	h.workers.run = func(ctx context.Context, _ workerCall) (string, error) {
		<-ctx.Done()
		close(block)
		return "", ctx.Err()
	}
	h.ingest("alice", "github:o/r#1", "work", true)
	for range 200 {
		h.service.Pass(context.Background())
		if h.workers.callCount() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ticket := h.store.allTickets()[0]
	if err := h.service.StopTicket(context.Background(), ticket.ID); err != nil {
		t.Fatal(err)
	}
	<-block
	h.service.wg.Wait()
	if record := readTicketRecord(t, h.ticketFolder(ticket)); record.Status != core.SuperAgentTicketStopped {
		t.Fatalf("record = %+v", record)
	}
}

func TestWorkersWorkBelowTheHomeWhereTheirAgentAllows(t *testing.T) {
	h := newHarness(t)
	h.setLocalAgent(core.AgentInstance{ID: "tree", Name: "tree", Enabled: true, WorkDir: t.TempDir(), WorkspaceMode: "worktree"})
	h.agent.Workers = append(h.agent.Workers, core.SuperAgentWorker{ID: "tree", AgentID: "tree"})
	h.agent.AutoWorkers, h.agent.WorkerRuntimes = true, []string{"codex"}
	saved, err := h.service.Save(context.Background(), h.agent)
	if err != nil {
		t.Fatal(err)
	}
	h.agent = saved
	h.agent.Sources = h.store.agents[saved.ID].Sources

	for index, worker := range []string{"tree", "auto:codex", "remote"} {
		answer := acceptAnswer(worker)
		h.router.answer = func(core.InvocationRequest) (string, error) { return answer, nil }
		h.ingest("alice", "github:o/r#"+worker, "job", true)
		h.settle()
		tickets, _ := h.store.ListSuperAgentThreadTickets(context.Background(), h.agent.ID, "github:o/r#"+worker)
		if len(tickets) != 1 {
			t.Fatalf("%s: tickets = %+v", worker, tickets)
		}
		ticket := tickets[0]
		call := h.workers.calls[index]
		dir := h.ticketFolder(ticket)
		work := filepath.Join(dir, ticketWorkDir)
		switch worker {
		case "tree", "auto:codex":
			// A worktree, or an auto worker given no directory, goes in work/.
			if call.dir != work {
				t.Fatalf("%s: conversation directory = %q, want %q", worker, call.dir, work)
			}
			if record := readTicketRecord(t, dir); record.WorkDir != work {
				t.Fatalf("%s: record = %+v", worker, record)
			}
		case "remote":
			if call.dir != "" {
				t.Fatalf("a remote worker's directory is on its own machine: %q", call.dir)
			}
			if _, err := os.Lstat(work); !os.IsNotExist(err) {
				t.Fatalf("remote ticket has a work entry: %v", err)
			}
			if record := readTicketRecord(t, dir); record.WorkDir != "" || record.Worker.HostID != "host-1" {
				t.Fatalf("remote record = %+v", record)
			}
		}
	}
}

func TestTicketFromBeforeHomesKeepsItsDirectory(t *testing.T) {
	h := newHarness(t)
	now := h.clock.UTC()
	ticket := core.SuperAgentTicket{
		ID: "tkt_old", SuperAgentID: h.agent.ID, SourceID: "gh", SourceKind: core.SuperAgentSourceGitHub, ThreadKey: "t",
		Title: "old", Worker: h.agent.Workers[0], ConversationID: "superagent-tkt_old", Status: core.SuperAgentTicketQueued,
		Inputs: []string{"continue"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.store.UpsertSuperAgentTicket(context.Background(), ticket); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if h.workers.callCount() != 1 || h.workers.calls[0].dir != "" {
		t.Fatalf("calls = %+v", h.workers.calls)
	}
	if _, err := os.Stat(h.ticketFolder(ticket)); !os.IsNotExist(err) {
		t.Fatalf("an old ticket got a folder: %v", err)
	}
}

func TestStartGivesEachSuperAgentItsOwnRouter(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	scratch, err := h.service.SaveRouter(ctx, Router{RuntimeID: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	scratchRouter, _ := h.store.GetAgentInstance(ctx, scratch.ID)
	// Saved before each router belonged to one Super Agent: two share the
	// harness's router, and one has a router still in its scratch directory.
	later := h.clock.Add(time.Hour)
	sharing := core.SuperAgent{ID: "sa_sharing", Name: "Sharing", RouterAgentID: "router", Workers: h.agent.Workers, CreatedAt: later}
	unbound := core.SuperAgent{ID: "sa_unbound", Name: "Unbound", RouterAgentID: scratch.ID, Workers: h.agent.Workers, CreatedAt: later}
	for _, agent := range []core.SuperAgent{sharing, unbound} {
		if err := h.store.UpsertSuperAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}

	h.service.adoptRouters(ctx)

	if kept, _ := h.store.GetSuperAgent(ctx, h.agent.ID); kept.RouterAgentID != "router" {
		t.Fatalf("the first Super Agent keeps the shared router: %q", kept.RouterAgentID)
	}
	moved, _ := h.store.GetSuperAgent(ctx, sharing.ID)
	if moved.RouterAgentID == "router" || !strings.HasPrefix(moved.RouterAgentID, routerIDPrefix) {
		t.Fatalf("the later Super Agent needs its own router: %q", moved.RouterAgentID)
	}
	original, _ := h.store.GetAgentInstance(ctx, "router")
	copied, _ := h.store.GetAgentInstance(ctx, moved.RouterAgentID)
	if copied == nil || copied.Source != routerAgentSource || copied.Name != "Sharing router" || copied.RuntimeID != original.RuntimeID || copied.DefaultApprovalMode != core.ApprovalModePlan {
		t.Fatalf("copied router = %+v", copied)
	}
	for id, router := range map[string]*core.AgentInstance{h.agent.ID: original, sharing.ID: copied} {
		if home, _ := h.service.homeDir(id); router.WorkDir != home {
			t.Fatalf("router of %s works in %q, want %q", id, router.WorkDir, home)
		}
	}
	bound, _ := h.store.GetAgentInstance(ctx, scratch.ID)
	if home, _ := h.service.homeDir(unbound.ID); bound.WorkDir != home {
		t.Fatalf("unbound router works in %q, want %q", bound.WorkDir, home)
	}
	if _, err := os.Stat(scratchRouter.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("the router's scratch directory was kept: %v", err)
	}

	// Running it again changes nothing.
	h.service.adoptRouters(ctx)
	if again, _ := h.store.GetSuperAgent(ctx, sharing.ID); again.RouterAgentID != moved.RouterAgentID {
		t.Fatalf("adopting twice made another copy: %q", again.RouterAgentID)
	}
}
