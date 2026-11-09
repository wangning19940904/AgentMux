package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Each Super Agent has a home directory. Its router works there, and each
// Ticket has a folder below it that keeps the Ticket's record and, where the
// worker's Agent allows it, the worker's working directory:
//
//	<state>/super-agents/<id>/
//	├── CLAUDE.md, AGENTS.md   what this directory is, for the router and workers
//	├── memory/MEMORY.md       the router's memory index, shown with every event
//	└── tickets/<ticket id>/
//	    ├── ticket.json        the Ticket as AgentMux last saved it
//	    ├── inputs.md          everything AgentMux sent the worker
//	    ├── result.md          the worker's answer after each turn
//	    ├── progress.md        what the worker has been doing, as it happens
//	    └── work/              the worker's working directory, or a link to it
//
// Tickets created before homes existed have no folder and keep working where
// they started.
const (
	memoryDirName    = "memory"
	memoryIndexFile  = "MEMORY.md"
	ticketsDirName   = "tickets"
	ticketWorkDir    = "work"
	ticketRecordFile = "ticket.json"
	ticketInputsFile = "inputs.md"
	ticketResultFile = "result.md"
)

// homeGuide describes the home to whoever works in or below it. It is written
// once, so the operator can add their own notes.
const homeGuide = `# Super Agent home

This directory belongs to a Super Agent in AgentMux. Its router works here,
and each ticket's worker works in a folder below it.

- memory/ is the router's memory. memory/MEMORY.md indexes it, one line per file.
- tickets/<ticket id>/ holds one ticket:
  - ticket.json: its status, worker and working directory, kept current by AgentMux
  - inputs.md: everything AgentMux sent the worker
  - result.md: the worker's final answer after each turn
  - progress.md: what the worker has been doing, written as it happens
  - work/: the worker's working directory, or a link to the directory its Agent uses

AgentMux does not overwrite this file after creating it.
`

// memoryStarter is MEMORY.md before the router has written anything.
const memoryStarter = `# Memory

One line per memory file in this folder: - [Title](file.md) — what it holds.
`

// maxMemoryIndexRunes bounds the index shown with every event. The router
// reads the files themselves when it needs them.
const maxMemoryIndexRunes = 8000

// reservedHomeNames are directories under super-agents that are not homes.
var reservedHomeNames = map[string]bool{"router": true, "routers": true}

func validSuperAgentID(id string) error {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." || reservedHomeNames[id] {
		return fmt.Errorf("invalid super agent id %q", id)
	}
	return nil
}

// homeDir is the Super Agent's home directory. It does not create it.
func (s *Service) homeDir(superAgentID string) (string, error) {
	if err := validSuperAgentID(superAgentID); err != nil {
		return "", err
	}
	state, err := s.stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "super-agents", superAgentID), nil
}

// ensureHome creates the Super Agent's home and returns it.
func (s *Service) ensureHome(superAgentID string) (string, error) {
	home, err := s.homeDir(superAgentID)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{ticketsDirName, memoryDirName} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			return "", fmt.Errorf("create super agent home: %w", err)
		}
	}
	if err := writeIfMissing(filepath.Join(home, memoryDirName, memoryIndexFile), memoryStarter); err != nil {
		return "", err
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		if err := writeIfMissing(filepath.Join(home, name), homeGuide); err != nil {
			return "", err
		}
	}
	return home, nil
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// memoryIndex is memory/MEMORY.md as the router is shown it, cut short when
// it grows too long.
func (s *Service) memoryIndex(superAgentID string) string {
	home, err := s.homeDir(superAgentID)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, memoryDirName, memoryIndexFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("read super agent memory index", "super_agent", superAgentID, "err", err)
		}
		return ""
	}
	index := strings.TrimSpace(string(data))
	if runes := []rune(index); len(runes) > maxMemoryIndexRunes {
		index = string(runes[:maxMemoryIndexRunes]) + "\n… (cut short; keep MEMORY.md brief and put details in the files it lists)"
	}
	return index
}

func (s *Service) ticketDir(superAgentID, ticketID string) (string, error) {
	if ticketID == "" || ticketID != filepath.Base(ticketID) || ticketID == "." || ticketID == ".." {
		return "", fmt.Errorf("invalid ticket id %q", ticketID)
	}
	home, err := s.homeDir(superAgentID)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ticketsDirName, ticketID), nil
}

// openTicketFolder creates a new Ticket's folder. The worker's directory is
// created on its first turn, by whatever prepares it.
func (s *Service) openTicketFolder(superAgentID, ticketID string) error {
	if _, err := s.ensureHome(superAgentID); err != nil {
		return err
	}
	dir, err := s.ticketDir(superAgentID, ticketID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create ticket folder: %w", err)
	}
	return nil
}

// existingTicketDir returns the Ticket's folder, or "" for a Ticket created
// before homes existed.
func (s *Service) existingTicketDir(ticket core.SuperAgentTicket) string {
	dir, err := s.ticketDir(ticket.SuperAgentID, ticket.ID)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

// ticketPlacement is where a Ticket's worker works.
type ticketPlacement struct {
	// ConversationDir is passed with the worker's invocation: the worker's
	// conversation, or its worktree, is created there.
	ConversationDir string
	// WorkDir is the directory the worker works in, as far as this machine
	// knows; empty for a remote worker.
	WorkDir string
	// Link is a directory the worker's Agent already uses. work/ links there.
	Link string
}

// placeTicket decides where a Ticket's worker works. A worker whose Agent
// gives each conversation its own worktree gets it in the Ticket's work/
// folder, and so does an auto worker that was given no directory. Any other
// local Agent keeps the directory it was configured with, which work/ links
// to. A remote worker's directory is on its own machine.
func (s *Service) placeTicket(ctx context.Context, ticket core.SuperAgentTicket) (ticketPlacement, error) {
	dir := s.existingTicketDir(ticket)
	if dir == "" || ticket.Worker.HostID != "" {
		return ticketPlacement{}, nil
	}
	instance, err := s.store.GetAgentInstance(ctx, ticket.Worker.AgentID)
	if err != nil || instance == nil {
		return ticketPlacement{}, err
	}
	work := filepath.Join(dir, ticketWorkDir)
	if strings.EqualFold(strings.TrimSpace(instance.WorkspaceMode), "worktree") {
		return ticketPlacement{ConversationDir: work, WorkDir: work}, nil
	}
	configured, err := expandHome(instance.WorkDir)
	if err != nil {
		return ticketPlacement{}, err
	}
	if IsManagedAgent(*instance) {
		if fallback, err := s.homeDir(ticket.SuperAgentID); err == nil && filepath.Clean(configured) == filepath.Join(fallback, "work") {
			return ticketPlacement{ConversationDir: work, WorkDir: work}, nil
		}
	}
	if configured == "" {
		return ticketPlacement{}, nil
	}
	return ticketPlacement{WorkDir: configured, Link: configured}, nil
}

// linkTicketWork points work/ at the directory the worker's Agent uses, so
// the router finds every worker's files in the same place.
func linkTicketWork(dir string, placement ticketPlacement) error {
	if placement.Link == "" || dir == "" {
		return nil
	}
	work := filepath.Join(dir, ticketWorkDir)
	if _, err := os.Lstat(work); err == nil {
		return nil
	}
	return os.Symlink(placement.Link, work)
}

// ticketRecord is ticket.json: the Ticket without the inputs, output and
// reply routing, which live in their own files or stay with AgentMux.
type ticketRecord struct {
	ID             string                `json:"id"`
	SuperAgentID   string                `json:"super_agent_id"`
	Title          string                `json:"title"`
	Status         string                `json:"status"`
	SourceKind     string                `json:"source_kind"`
	ThreadKey      string                `json:"thread_key"`
	Actor          string                `json:"actor,omitempty"`
	Worker         core.SuperAgentWorker `json:"worker"`
	ConversationID string                `json:"conversation_id"`
	WorkDir        string                `json:"work_dir,omitempty"`
	DependsOn      []string              `json:"depends_on,omitempty"`
	BaseTicket     string                `json:"base_ticket,omitempty"`
	LinkedThreads  []string              `json:"linked_threads,omitempty"`
	Attempts       int                   `json:"attempts"`
	Error          string                `json:"error,omitempty"`
	ReplyError     string                `json:"reply_error,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	StartedAt      time.Time             `json:"started_at,omitzero"`
	FinishedAt     time.Time             `json:"finished_at,omitzero"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

// recordTicket rewrites ticket.json. The store stays the source of truth, so
// a failure here is only logged.
func (s *Service) recordTicket(ctx context.Context, ticket core.SuperAgentTicket) {
	dir := s.existingTicketDir(ticket)
	if dir == "" {
		return
	}
	placement, err := s.placeTicket(ctx, ticket)
	if err != nil {
		s.log.Warn("place super agent ticket", "ticket", ticket.ID, "err", err)
	}
	record := ticketRecord{
		ID: ticket.ID, SuperAgentID: ticket.SuperAgentID, Title: ticket.Title, Status: ticket.Status,
		SourceKind: ticket.SourceKind, ThreadKey: ticket.ThreadKey, Actor: ticket.Actor, Worker: ticket.Worker,
		ConversationID: ticket.ConversationID, WorkDir: placement.WorkDir, Attempts: ticket.Attempts,
		DependsOn: ticket.DependsOn, BaseTicket: ticket.BaseTicket,
		Error: ticket.Error, ReplyError: ticket.ReplyError, CreatedAt: ticket.CreatedAt,
		StartedAt: ticket.StartedAt, FinishedAt: ticket.FinishedAt, UpdatedAt: ticket.UpdatedAt,
	}
	for _, link := range ticket.Links {
		record.LinkedThreads = append(record.LinkedThreads, link.ThreadKey)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err == nil {
		err = writeFileAtomic(filepath.Join(dir, ticketRecordFile), append(data, '\n'))
	}
	if err != nil {
		s.log.Warn("write super agent ticket record", "ticket", ticket.ID, "err", err)
	}
}

// appendTicketLog adds one timestamped section to a file in the Ticket's
// folder. Like recordTicket, it never fails the Ticket.
func (s *Service) appendTicketLog(ticket core.SuperAgentTicket, name, text string) {
	dir := s.existingTicketDir(ticket)
	if dir == "" || strings.TrimSpace(text) == "" {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = fmt.Fprintf(file, "## %s\n\n%s\n\n", s.now().UTC().Format(time.RFC3339), strings.TrimSpace(text))
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		s.log.Warn("write super agent ticket log", "ticket", ticket.ID, "file", name, "err", err)
	}
}

func writeFileAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = temp.Write(data)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(temp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(temp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(temp.Name())
	}
	return err
}
