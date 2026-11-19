package superagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/workspace"
)

// worktreeTicket runs one Ticket to ready on a worker whose Agent gives each
// conversation a worktree, and makes that worktree in the Ticket's work/, as
// the worker's first turn would have.
func (h *harness) worktreeTicket(repo, thread string) (core.SuperAgentTicket, string) {
	h.t.Helper()
	h.setLocalAgent(core.AgentInstance{ID: "tree", Name: "tree", Enabled: true, WorkDir: repo, WorkspaceMode: "worktree"})
	if len(h.agent.Workers) == 2 {
		h.agent.Workers = append(h.agent.Workers, core.SuperAgentWorker{ID: "tree", AgentID: "tree"})
		saved, err := h.service.Save(context.Background(), h.agent)
		if err != nil {
			h.t.Fatal(err)
		}
		h.agent = saved
		h.agent.Sources = h.store.agents[saved.ID].Sources
	}
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("tree"), nil }
	h.ingest("alice", thread, "job", true)
	h.settle()
	tickets, _ := h.store.ListSuperAgentThreadTickets(context.Background(), h.agent.ID, thread)
	if len(tickets) != 1 || tickets[0].Status != core.SuperAgentTicketReady {
		h.t.Fatalf("tickets = %+v", tickets)
	}
	work := filepath.Join(h.ticketFolder(tickets[0]), ticketWorkDir)
	runGit(h.t, repo, "worktree", "add", "-q", "-b", "agentmux/"+tickets[0].ID, work)
	return tickets[0], work
}

// sweepAfter moves the clock on and looks at finished Tickets' worktrees.
func (h *harness) sweepAfter(duration time.Duration) {
	h.t.Helper()
	h.advance(duration)
	h.service.Pass(context.Background())
}

func TestAFinishedTicketsWorktreeIsRemovedOnceItIsQuiet(t *testing.T) {
	h := newHarness(t)
	repo := newRepo(t)
	ticket, work := h.worktreeTicket(repo, "github:o/r#1")
	writeFile(t, filepath.Join(work, "b.txt"), "work\n")
	writeFile(t, filepath.Join(work, "build.log"), "ignored\n")
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "work")
	head := runGit(t, work, "rev-parse", "HEAD")

	h.sweepAfter(quietWorktreeAge - time.Hour)
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("a follow-up may still come, so the worktree stays: %v", err)
	}

	h.sweepAfter(2 * time.Hour)
	if _, err := os.Lstat(work); !os.IsNotExist(err) {
		t.Fatalf("the quiet ticket's worktree is removed: %v", err)
	}
	if list := runGit(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, ticket.ID) {
		t.Fatalf("the repository forgets the worktree:\n%s", list)
	}
	if got := runGit(t, repo, "rev-parse", "agentmux/"+ticket.ID); got != head {
		t.Fatalf("the branch keeps the work: %s != %s", got, head)
	}
	note, ok := workspace.ReadRemovedWorktree(work)
	if !ok || note.Branch != "agentmux/"+ticket.ID || note.Commit != head {
		t.Fatalf("note = %+v ok=%v", note, ok)
	}
	for _, name := range []string{ticketRecordFile, ticketInputsFile, ticketResultFile} {
		if _, err := os.Stat(filepath.Join(h.ticketFolder(ticket), name)); err != nil {
			t.Fatalf("the ticket's record stays: %v", err)
		}
	}
	// A Ticket that builds on this one still starts from its last commit.
	if base := h.service.baseCommit(context.Background(), core.SuperAgentTicket{ID: "next", BaseTicket: ticket.ID}); base != head {
		t.Fatalf("base commit = %q, want %q", base, head)
	}
}

func TestAWorktreeWithUncommittedWorkIsKept(t *testing.T) {
	h := newHarness(t)
	repo := newRepo(t)
	_, work := h.worktreeTicket(repo, "github:o/r#1")
	writeFile(t, filepath.Join(work, "draft.txt"), "not committed\n")

	h.sweepAfter(quietWorktreeAge + 2*time.Hour)
	if data, err := os.ReadFile(filepath.Join(work, "draft.txt")); err != nil || string(data) != "not committed\n" {
		t.Fatalf("uncommitted work stays where it is: %q, %v", data, err)
	}
	if _, ok := workspace.ReadRemovedWorktree(work); ok {
		t.Fatal("a kept worktree leaves no note")
	}
}

func TestAStoppedTicketsWorktreeGoesSooner(t *testing.T) {
	h := newHarness(t)
	repo := newRepo(t)
	ticket, work := h.worktreeTicket(repo, "github:o/r#1")
	if err := h.service.StopTicket(context.Background(), ticket.ID); err != nil {
		t.Fatal(err)
	}

	h.sweepAfter(stoppedWorktreeAge - time.Hour)
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("the worktree stays for a day: %v", err)
	}
	h.sweepAfter(2 * time.Hour)
	if _, err := os.Lstat(work); !os.IsNotExist(err) {
		t.Fatalf("a stopped ticket's worktree is removed: %v", err)
	}
}

func TestADirectoryTheAgentUsesIsNeverRemoved(t *testing.T) {
	h := newHarness(t)
	repo := newRepo(t)
	// The default worker's Agent works in the repository itself; work/ links
	// there.
	h.setLocalAgent(core.AgentInstance{ID: "coder", Name: "coder", Enabled: true, WorkDir: repo})
	h.ingest("alice", "github:o/r#1", "job", true)
	h.settle()
	ticket := h.store.allTickets()[0]
	link := filepath.Join(h.ticketFolder(ticket), ticketWorkDir)
	if target, err := os.Readlink(link); err != nil || target != repo {
		t.Fatalf("work link = %q, %v", target, err)
	}

	h.sweepAfter(quietWorktreeAge + 2*time.Hour)
	if _, err := os.Readlink(link); err != nil {
		t.Fatalf("the link stays: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatalf("the Agent's directory is untouched: %v", err)
	}
}
