package superagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Independent review is optional. When an agent names a review framework and
// a local worker in its own git worktree finishes a turn, a fresh worker of
// that framework reviews the change before the brain hears about it. The
// reviewer works in a detached copy of the worktree, the worker's HEAD plus
// its uncommitted changes, with a read-only approval mode, and is given only
// the Ticket's title, instructions, criteria and the diff against its base:
// never the worker's transcript or answer. Its findings go with the ticket
// update. Remote workers are not reviewed yet: their files are on another
// machine.

const (
	// reviewerPathID tells a review framework's Agent instance apart from
	// the auto workers of the same framework.
	reviewerPathID          = "review"
	independentReviewTime   = 30 * time.Minute
	maxReviewDiffRunes      = 60_000
	maxReviewFindingsRunes  = 6000
	reviewSnapshotName      = ".review"
	reviewSnapshotIndexName = ".review-index"
	reviewSnapshotMessage   = "AgentMux review snapshot"
)

var reviewTrailer = regexp.MustCompile(`(?m)^\s*<review-findings\s+count="(\d+)"\s+blocking="(\d+)"\s*/>\s*$`)

func reviewerAgentID(superAgentID, runtimeID string) string {
	return autoWorkerAgentID(superAgentID, runtimeID, reviewerPathID)
}

// normalizeReviewRuntime checks the review framework: it must run here and
// offer the read-only plan mode the reviewer runs in.
func (s *Service) normalizeReviewRuntime(agent *core.SuperAgent) error {
	agent.ReviewRuntime = strings.TrimSpace(agent.ReviewRuntime)
	runtimeID := agent.ReviewRuntime
	if runtimeID == "" {
		return nil
	}
	if s.runtimeAvailable != nil && !s.runtimeAvailable(runtimeID) {
		return fmt.Errorf("review framework %q is not installed on this machine", runtimeID)
	}
	if !core.ApprovalModeSupported(runtimeID, core.ApprovalModePlan) {
		return fmt.Errorf("review framework %q has no read-only %s mode, so it cannot review", runtimeID, core.ApprovalModePlan)
	}
	return nil
}

// reviewable reports whether a finished turn's work can be reviewed: a local
// worker in a git worktree of the Ticket's own.
func reviewable(ctx context.Context, agent core.SuperAgent, ticket core.SuperAgentTicket, placement ticketPlacement) bool {
	if agent.ReviewRuntime == "" || ticket.Worker.HostID != "" || placement.ConversationDir == "" {
		return false
	}
	output, err := exec.CommandContext(ctx, "git", "-C", placement.ConversationDir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

// independentReview has a fresh reviewer look at a finished turn's work. It
// returns the text the ticket update carries, and records the outcome on the
// Ticket. A review that cannot run says why instead.
func (s *Service) independentReview(ctx context.Context, agent core.SuperAgent, ticket core.SuperAgentTicket, workDir string) string {
	reviewCtx, cancel := context.WithTimeout(ctx, independentReviewTime)
	defer cancel()
	record := core.SuperAgentAutoReview{Runtime: agent.ReviewRuntime}
	findings, err := s.runIndependentReview(reviewCtx, agent, ticket, workDir)
	if err != nil {
		record.Error = err.Error()
	} else {
		record.Findings, record.Count, record.Blocking = parseReviewFindings(findings)
	}
	record.At = s.now().UTC()
	if ctx.Err() == nil {
		if err := s.mutateTicket(ctx, ticket.ID, func(current *core.SuperAgentTicket) error {
			current.AutoReview = &record
			return nil
		}); err != nil {
			s.log.Warn("record independent review", "ticket", ticket.ID, "err", err)
		}
	}
	if record.Error != "" {
		s.log.Warn("independent review failed", "ticket", ticket.ID, "err", record.Error)
		return fmt.Sprintf("Independent review (%s) could not run: %s", record.Runtime, record.Error)
	}
	return fmt.Sprintf("Independent review by a fresh %s worker that saw only the ticket and its diff, not the worker's report (%d findings, %d blocking):\n%s",
		record.Runtime, record.Count, record.Blocking, truncateRunes(record.Findings, maxReviewFindingsRunes))
}

func (s *Service) runIndependentReview(ctx context.Context, agent core.SuperAgent, ticket core.SuperAgentTicket, workDir string) (string, error) {
	folder := s.existingTicketDir(ticket)
	if folder == "" {
		return "", errors.New("the ticket has no folder for the review's copy")
	}
	snapshot, err := takeSnapshot(ctx, workDir, filepath.Join(folder, reviewSnapshotName), filepath.Join(folder, reviewSnapshotIndexName))
	if err != nil {
		return "", err
	}
	defer func() {
		if err := snapshot.remove(); err != nil {
			s.log.Warn("remove review copy", "ticket", ticket.ID, "dir", snapshot.dir, "err", err)
		}
	}()
	snapshot.base = s.reviewBase(ctx, ticket, snapshot)
	diff, err := snapshot.diff(ctx)
	if err != nil {
		return "", err
	}
	reviewer, err := s.ensureReviewer(ctx, agent)
	if err != nil {
		return "", err
	}
	conversation := fmt.Sprintf("superagent-review-%s-%d", ticket.ID, s.now().UnixNano())
	answer, err := s.workers.Run(ctx, reviewer, conversation, reviewerPrompt(ticket, snapshot, diff), RunOptions{Dir: snapshot.dir})
	if err != nil {
		return "", fmt.Errorf("the reviewer failed: %w", err)
	}
	return answer, nil
}

// ensureReviewer keeps the Agent instance that runs an agent's reviews. Like
// an auto worker, it takes the router's model and environment when it shares
// its framework; unlike one, it runs in the read-only plan mode, and each
// review is a new conversation in that review's copy.
func (s *Service) ensureReviewer(ctx context.Context, agent core.SuperAgent) (core.SuperAgentWorker, error) {
	runtimeID := agent.ReviewRuntime
	home, err := s.ensureHome(agent.ID)
	if err != nil {
		return core.SuperAgentWorker{}, err
	}
	id := reviewerAgentID(agent.ID, runtimeID)
	now := s.now().UTC()
	instance := core.AgentInstance{
		ID: id, Name: fmt.Sprintf("%s reviewer (%s)", agent.Name, runtimeID), RuntimeID: runtimeID, SessionBackend: "structured",
		WorkDir: home, WorkspaceMode: "shared", DefaultApprovalMode: core.ApprovalModePlan,
		Enabled: true, Source: autoWorkerAgentSource, CreatedAt: now, UpdatedAt: now,
	}
	if existing, err := s.store.GetAgentInstance(ctx, id); err != nil {
		return core.SuperAgentWorker{}, err
	} else if existing != nil {
		instance.CreatedAt = existing.CreatedAt
	}
	if router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID); err == nil && router != nil && router.RuntimeID == runtimeID {
		instance.SessionBackend = router.SessionBackend
		instance.DefaultModel, instance.DefaultReasoningEffort, instance.DefaultServiceTier = router.DefaultModel, router.DefaultReasoningEffort, router.DefaultServiceTier
		instance.Env = router.Env
	}
	if err := s.store.UpsertAgentInstance(ctx, &instance); err != nil {
		return core.SuperAgentWorker{}, err
	}
	return core.SuperAgentWorker{ID: "review:" + runtimeID, Label: instance.Name, AgentID: id}, nil
}

// reviewBase is the commit the change is measured from: where the worker's
// branch left its base Ticket's work, or the repository's checkout. Failing
// both, it is the worker's HEAD, so the review covers what is not committed.
func (s *Service) reviewBase(ctx context.Context, ticket core.SuperAgentTicket, snapshot *reviewSnapshot) string {
	from := s.baseCommit(ctx, ticket)
	if from == "" {
		if common, err := git(ctx, snapshot.top, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			from, _ = git(ctx, "", "--git-dir="+common, "rev-parse", "HEAD")
		}
	}
	if from != "" {
		if base, err := git(ctx, snapshot.top, "merge-base", snapshot.head, from); err == nil && base != "" {
			return base
		}
	}
	return snapshot.head
}

// reviewSnapshot is a detached worktree holding a worker's work as it is:
// its HEAD plus its uncommitted changes, untracked files included.
type reviewSnapshot struct {
	top, dir, head, commit, base string
}

// takeSnapshot copies the work in workDir to dir without touching the
// worker's index or files: it stages everything into a separate index,
// commits that tree on top of HEAD as an unreferenced commit, and checks the
// commit out, detached, at dir.
func takeSnapshot(ctx context.Context, workDir, dir, index string) (*reviewSnapshot, error) {
	top, err := git(ctx, workDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("find the worker's repository: %w", err)
	}
	head, err := git(ctx, top, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read the worker's HEAD: %w", err)
	}
	if err := clearSnapshot(ctx, top, dir); err != nil {
		return nil, err
	}
	_ = os.Remove(index)
	defer os.Remove(index)
	staged := []string{"GIT_INDEX_FILE=" + index}
	for _, args := range [][]string{{"read-tree", head}, {"add", "-A"}} {
		if _, err := gitEnv(ctx, top, staged, args...); err != nil {
			return nil, fmt.Errorf("stage the worker's changes: %w", err)
		}
	}
	tree, err := gitEnv(ctx, top, staged, "write-tree")
	if err != nil {
		return nil, fmt.Errorf("stage the worker's changes: %w", err)
	}
	identity := []string{"GIT_AUTHOR_NAME=AgentMux", "GIT_AUTHOR_EMAIL=agentmux@localhost", "GIT_COMMITTER_NAME=AgentMux", "GIT_COMMITTER_EMAIL=agentmux@localhost"}
	commit, err := gitEnv(ctx, top, identity, "commit-tree", tree, "-p", head, "-m", reviewSnapshotMessage)
	if err != nil {
		return nil, fmt.Errorf("record the worker's changes: %w", err)
	}
	if _, err := git(ctx, top, "worktree", "add", "--detach", dir, commit); err != nil {
		return nil, fmt.Errorf("check out the review copy: %w", err)
	}
	return &reviewSnapshot{top: top, dir: dir, head: head, commit: commit}, nil
}

// remove deletes the copy and its record in the repository.
func (snapshot *reviewSnapshot) remove() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return clearSnapshot(ctx, snapshot.top, snapshot.dir)
}

// clearSnapshot removes a review copy left at dir, such as one from a review
// the daemon stopped in the middle of.
func clearSnapshot(ctx context.Context, top, dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, removeErr := git(ctx, top, "worktree", "remove", "--force", dir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if _, err := git(ctx, top, "worktree", "prune"); err != nil && removeErr != nil {
		return removeErr
	}
	return nil
}

// diff is the change against the base, cut short when it is long.
func (snapshot *reviewSnapshot) diff(ctx context.Context) (string, error) {
	stat, err := git(ctx, snapshot.dir, "diff", "--no-color", "--no-ext-diff", "--stat", snapshot.base, snapshot.commit)
	if err != nil {
		return "", fmt.Errorf("read the change: %w", err)
	}
	patch, err := git(ctx, snapshot.dir, "diff", "--no-color", "--no-ext-diff", snapshot.base, snapshot.commit)
	if err != nil {
		return "", fmt.Errorf("read the change: %w", err)
	}
	if strings.TrimSpace(patch) == "" {
		return "(no change against the base)", nil
	}
	if runes := []rune(patch); len(runes) > maxReviewDiffRunes {
		patch = string(runes[:maxReviewDiffRunes]) + fmt.Sprintf("\n[the diff is cut short here; run git diff %s HEAD for the rest]", snapshot.base)
	}
	return stat + "\n\n" + patch, nil
}

// reviewerPrompt is all the reviewer is told: the Ticket as the brain wrote
// it and the change.
func reviewerPrompt(ticket core.SuperAgentTicket, snapshot *reviewSnapshot, diff string) string {
	var prompt strings.Builder
	prompt.WriteString(`You are an independent reviewer in AgentMux. Another agent has just finished the work described below. You are given only what it was asked to do and the change it made, not its reasoning or its own account of the work, so check every claim yourself.

The current directory is a disposable copy of the work after the change. HEAD is the change; ` + snapshot.base + ` is where it started. Read any file and run read-only checks and tests. Do not commit, push, switch branches or change anything remote: the copy is deleted after the review.

- Report only problems this change introduced, and requirements or criteria below that it does not meet. Leave out problems that were there before.
- Write one finding per line: "- [P0] path:line: what is wrong and why. Fix: a short suggestion." P0 blocks: wrong behavior, lost data, a security problem, or a requirement not met. P1 is likely wrong or clearly untested. P2 is minor; give at most two style findings, all P2.
- Report only what you are confident of, with a path:line a reader can check. When you find nothing, say so; do not pad.
- Answer directly. Ask nothing and wait for nothing.

The last line of your answer must be <review-findings count="N" blocking="M"/>, where N is the number of findings and M the number of P0 and P1 ones.
`)
	prompt.WriteString("\nTICKET: " + ticket.Title + "\n")
	if ticket.Instructions != "" {
		prompt.WriteString("\nWHAT IT WAS ASKED TO DO:\n" + ticket.Instructions + "\n")
	}
	if len(ticket.DoneWhen) > 0 {
		prompt.WriteString("\nDONE WHEN:\n")
		for index, criterion := range ticket.DoneWhen {
			fmt.Fprintf(&prompt, "%d. %s\n", index+1, criterion)
		}
	}
	prompt.WriteString("\nTHE CHANGE:\n" + diff + "\n")
	return prompt.String()
}

// parseReviewFindings splits the reviewer's answer into its findings and the
// counts its last line gives. Without that line the counts are unknown (-1).
func parseReviewFindings(answer string) (string, int, int) {
	answer = strings.TrimSpace(answer)
	matches := reviewTrailer.FindAllStringSubmatchIndex(answer, -1)
	if len(matches) == 0 {
		return answer, -1, -1
	}
	last := matches[len(matches)-1]
	count, _ := strconv.Atoi(answer[last[2]:last[3]])
	blocking, _ := strconv.Atoi(answer[last[4]:last[5]])
	return strings.TrimSpace(answer[:last[0]] + answer[last[1]:]), count, blocking
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitEnv(ctx, dir, nil, args...)
}

func gitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	name := strings.Join(args[:min(2, len(args))], " ")
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", name, err, oneLine(stderr.String(), 300))
	}
	return strings.TrimSpace(string(output)), nil
}
