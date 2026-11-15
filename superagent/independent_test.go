package superagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// newRepo makes a repository with one committed file.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "a.txt"), "one\n")
	writeFile(t, filepath.Join(repo, ".gitignore"), "*.log\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "base")
	return repo
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewSnapshotCopiesTheWorkAndIsRemoved(t *testing.T) {
	repo := newRepo(t)
	head := runGit(t, repo, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(repo, "a.txt"), "two\n")
	writeFile(t, filepath.Join(repo, "new.txt"), "new\n")
	writeFile(t, filepath.Join(repo, "build.log"), "ignored\n")
	before := runGit(t, repo, "status", "--porcelain")

	ctx := context.Background()
	folder := t.TempDir()
	dir, index := filepath.Join(folder, reviewSnapshotName), filepath.Join(folder, reviewSnapshotIndexName)
	snapshot, err := takeSnapshot(ctx, repo, dir, index)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "two\n" {
		t.Fatalf("the copy has the uncommitted change: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal("the copy has the untracked file")
	}
	if _, err := os.Stat(filepath.Join(dir, "build.log")); err == nil {
		t.Fatal("ignored files stay out of the copy")
	}
	if got := runGit(t, dir, "rev-parse", "HEAD"); got != snapshot.commit {
		t.Fatalf("the copy is checked out at the snapshot: %s != %s", got, snapshot.commit)
	}
	if parent := runGit(t, dir, "rev-parse", "HEAD^"); parent != head {
		t.Fatalf("the snapshot sits on the worker's HEAD: %s != %s", parent, head)
	}
	if branch := runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "HEAD" {
		t.Fatalf("the copy is detached: %s", branch)
	}
	if after := runGit(t, repo, "status", "--porcelain"); after != before {
		t.Fatalf("the worker's index and files are untouched:\n%s\n!=\n%s", after, before)
	}
	if _, err := os.Stat(index); err == nil {
		t.Fatal("the separate index is removed")
	}

	snapshot.base = head
	diff, err := snapshot.diff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-one", "+two", "new.txt"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff lacks %q:\n%s", want, diff)
		}
	}

	if err := snapshot.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the copy is removed")
	}
	if list := runGit(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, dir) {
		t.Fatalf("the repository forgets the copy:\n%s", list)
	}
	if after := runGit(t, repo, "status", "--porcelain"); after != before {
		t.Fatalf("removing the copy leaves the worker's files alone:\n%s", after)
	}
}

func TestIndependentReviewSeesTheTicketAndTheDiffOnly(t *testing.T) {
	h := newHarness(t)
	repo := newRepo(t)
	h.setLocalAgent(core.AgentInstance{ID: "coder", Name: "Coder", RuntimeID: "codex", WorkDir: repo, WorkspaceMode: "worktree", Enabled: true})
	h.agent.ReviewRuntime = "codex"
	if err := h.store.UpsertSuperAgent(context.Background(), h.agent); err != nil {
		t.Fatal(err)
	}
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return acceptWithCriteria(`,"done_when":["no float in pricing/"]`), nil
	}
	reviewer := reviewerAgentID(h.agent.ID, "codex")
	var mu sync.Mutex
	var reviewInput, reviewDir string
	var copyHad bool
	h.workers.run = func(_ context.Context, call workerCall) (string, error) {
		if call.worker.AgentID == reviewer {
			data, err := os.ReadFile(filepath.Join(call.dir, "pricing", "price.go"))
			mu.Lock()
			reviewInput, reviewDir, copyHad = call.input, call.dir, err == nil && strings.Contains(string(data), "float64")
			mu.Unlock()
			return "- [P1] pricing/price.go:3: price is still a float64. Fix: use decimal.\n<review-findings count=\"1\" blocking=\"1\"/>", nil
		}
		// The worker makes its worktree, as the in-process invoker would, and
		// leaves its change uncommitted.
		runGit(t, repo, "worktree", "add", "-q", "-b", "agentmux/t", call.dir)
		if err := os.MkdirAll(filepath.Join(call.dir, "pricing"), 0o755); err != nil {
			t.Error(err)
		}
		writeFile(t, filepath.Join(call.dir, "pricing", "price.go"), "package pricing\n\nvar Price float64\n")
		return "All done, trust me. WORKER-REPORT-7", nil
	}
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()

	if got := h.workers.callCount(); got != 2 {
		t.Fatalf("the worker and the reviewer run: calls = %d", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !copyHad {
		t.Fatal("the reviewer works in a copy that has the worker's uncommitted change")
	}
	ticket := h.store.allTickets()[0]
	if reviewDir != filepath.Join(h.ticketFolder(ticket), reviewSnapshotName) {
		t.Fatalf("review dir = %q", reviewDir)
	}
	if _, err := os.Stat(reviewDir); err == nil {
		t.Fatal("the copy is removed after the review")
	}
	for _, want := range []string{"TICKET: Fix rounding", "Use decimals.", "1. no float in pricing/", "+var Price float64", "<review-findings"} {
		if !strings.Contains(reviewInput, want) {
			t.Fatalf("the reviewer is not told %q:\n%s", want, reviewInput)
		}
	}
	for _, unwanted := range []string{"WORKER-REPORT-7", "fix rounding", "REQUEST"} {
		if strings.Contains(reviewInput, unwanted) {
			t.Fatalf("the reviewer must not see %q:\n%s", unwanted, reviewInput)
		}
	}
	update := h.router.updatePrompts[0]
	for _, want := range []string{"WORKER-REPORT-7", "Independent review by a fresh codex worker", "(1 findings, 1 blocking)", "[P1] pricing/price.go:3"} {
		if !strings.Contains(update, want) {
			t.Fatalf("the ticket update lacks %q:\n%s", want, update)
		}
	}
	if strings.Contains(update, "<review-findings") {
		t.Fatalf("the counts line is read, not passed on:\n%s", update)
	}
	if ticket.AutoReview == nil || ticket.AutoReview.Count != 1 || ticket.AutoReview.Blocking != 1 || ticket.AutoReview.Runtime != "codex" {
		t.Fatalf("auto review = %+v", ticket.AutoReview)
	}
	instance, _ := h.store.GetAgentInstance(context.Background(), reviewer)
	if instance == nil || instance.DefaultApprovalMode != core.ApprovalModePlan || instance.WorkspaceMode != "shared" || !IsManagedAgent(*instance) {
		t.Fatalf("reviewer instance = %+v", instance)
	}
	if status := runGit(t, filepath.Join(h.ticketFolder(ticket), ticketWorkDir), "status", "--porcelain"); !strings.Contains(status, "pricing/") {
		t.Fatalf("the worker's change stays uncommitted in its worktree: %q", status)
	}
}

func TestIndependentReviewSkipsWhatItCannotReview(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	agent := core.SuperAgent{ReviewRuntime: "codex"}
	local := core.SuperAgentTicket{}
	if !reviewable(ctx, agent, local, ticketPlacement{ConversationDir: repo}) {
		t.Fatal("a local worker in its own git worktree is reviewed")
	}
	remote := core.SuperAgentTicket{Worker: core.SuperAgentWorker{HostID: "host-1"}}
	if reviewable(ctx, agent, remote, ticketPlacement{ConversationDir: repo}) {
		t.Fatal("remote workers are not reviewed in this version")
	}
	if reviewable(ctx, agent, local, ticketPlacement{WorkDir: repo, Link: repo}) {
		t.Fatal("a worker in a shared directory has no change of its own to review")
	}
	if reviewable(ctx, agent, local, ticketPlacement{ConversationDir: t.TempDir()}) {
		t.Fatal("a directory outside git is not reviewed")
	}
	if reviewable(ctx, core.SuperAgent{}, local, ticketPlacement{ConversationDir: repo}) {
		t.Fatal("independent review is off by default")
	}
}

func TestReviewRuntimeIsChecked(t *testing.T) {
	service := New(Options{RuntimeAvailable: func(id string) bool { return id != "claudecode" }})
	for runtime, want := range map[string]string{"": "", " codex ": "", "claudecode": "not installed", "kimi": "read-only"} {
		agent := core.SuperAgent{ReviewRuntime: runtime}
		err := service.normalizeReviewRuntime(&agent)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Fatalf("%q: %v", runtime, err)
		}
	}
}

func TestParseReviewFindings(t *testing.T) {
	findings, count, blocking := parseReviewFindings("- [P2] a.go:1: x\n<review-findings count=\"1\" blocking=\"0\"/>\n")
	if findings != "- [P2] a.go:1: x" || count != 1 || blocking != 0 {
		t.Fatalf("%q %d %d", findings, count, blocking)
	}
	if _, count, _ := parseReviewFindings("Nothing to report."); count != -1 {
		t.Fatalf("no counts line: %d", count)
	}
}
