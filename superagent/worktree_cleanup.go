package superagent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/workspace"
)

// A Ticket's worktree outlives its turn, since a follow-up or a retry
// continues in it. Once the Ticket has been quiet for a while the worktree is
// removed, as long as nothing in it would be lost: it is on a branch and has
// no uncommitted work. The branch stays in the repository, and a later turn
// checks it out in work/ again. A worktree with uncommitted work is kept.
const (
	worktreeSweepInterval = time.Hour
	// quietWorktreeAge is how long a ready or failed Ticket keeps its
	// worktree after anything last happened to it.
	quietWorktreeAge = 7 * 24 * time.Hour
	// stoppedWorktreeAge is shorter because a stopped Ticket never resumes.
	stoppedWorktreeAge = 24 * time.Hour
	// maxWorktreeRemovals bounds one sweep, which runs between routing passes.
	maxWorktreeRemovals = 10
)

// sweepTicketWorktrees removes the worktrees of Tickets on this machine that
// finished long enough ago. It runs in the routing pass, the only place a
// turn starts, at most once every worktreeSweepInterval.
func (s *Service) sweepTicketWorktrees(ctx context.Context) {
	now := s.now().UTC()
	s.mu.Lock()
	due := s.worktreesSwept.IsZero() || now.Sub(s.worktreesSwept) >= worktreeSweepInterval
	if due {
		s.worktreesSwept = now
	}
	s.mu.Unlock()
	if !due || ctx.Err() != nil {
		return
	}
	tickets, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketReady, core.SuperAgentTicketFailed, core.SuperAgentTicketStopped)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("list finished super agent tickets", "err", err)
		}
		return
	}
	removed := 0
	for _, ticket := range tickets {
		if removed >= maxWorktreeRemovals || ctx.Err() != nil {
			return
		}
		work := s.removableWorktree(ticket, now)
		if work == "" {
			continue
		}
		removal, err := workspace.RemoveCleanWorktree(ctx, work, now)
		switch {
		case removal.Removed:
			removed++
			s.log.Info("removed a finished ticket's worktree", "ticket", ticket.ID, "dir", work,
				"repository", removal.Note.Repository, "branch", removal.Note.Branch)
			if err != nil {
				s.log.Warn("record a removed worktree", "ticket", ticket.ID, "err", err)
			}
		case err != nil:
			if ctx.Err() == nil {
				s.log.Warn("remove a finished ticket's worktree", "ticket", ticket.ID, "dir", work, "err", err)
			}
		default:
			s.log.Debug("kept a finished ticket's worktree", "ticket", ticket.ID, "dir", work, "why", removal.Kept)
		}
	}
}

// removableWorktree returns the Ticket's work/ directory when the Ticket has
// been finished and untouched for long enough and work/ is a directory of the
// Ticket's own: not a link to a directory its Agent uses, and not on another
// machine. Whether it can go without losing work is the removal's to decide.
func (s *Service) removableWorktree(ticket core.SuperAgentTicket, now time.Time) string {
	if ticket.Worker.HostID != "" || ticket.FinishedAt.IsZero() {
		return ""
	}
	age := quietWorktreeAge
	if ticket.Status == core.SuperAgentTicketStopped {
		age = stoppedWorktreeAge
	}
	quietSince := ticket.FinishedAt
	if ticket.UpdatedAt.After(quietSince) {
		quietSince = ticket.UpdatedAt
	}
	if now.Sub(quietSince) < age {
		return ""
	}
	// A turn's goroutine outlives the Ticket's status while it is reviewed
	// and reported.
	s.mu.Lock()
	_, running := s.running[ticket.SuperAgentID+"|"+ticket.ID]
	s.mu.Unlock()
	if running {
		return ""
	}
	dir := s.existingTicketDir(ticket)
	if dir == "" {
		return ""
	}
	work := filepath.Join(dir, ticketWorkDir)
	if info, err := os.Lstat(work); err != nil || !info.IsDir() {
		return ""
	}
	return work
}
