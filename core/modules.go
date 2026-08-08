package core

import (
	"context"
	"time"
)

// This file declares the interfaces for the four control-plane modules that
// round out AgentMux beyond Connect (platform/), Router (agent/) and
// Ledger (usage/): Memory, Skills, MCP Registry and Guard. As with the other
// subsystems, core only declares the contracts; the concrete adapters live in
// their own packages and register themselves via the registry below.

// MemoryEntry is a single unit of shared memory: a fact, summary or snippet
// that should persist across agent sessions and be retrievable by later turns.
type MemoryEntry struct {
	ID        string            `json:"id"`
	Scope     string            `json:"scope"` // global, project:<name>, session:<id>
	Content   string            `json:"content"`
	Tags      []string          `json:"tags,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// MemoryStore is the unified, cross-agent and cross-session memory layer
// (AgentMux Memory). Implementations may be backed by SQLite, a vector
// store, or a remote service.
type MemoryStore interface {
	// Name returns the registered backend id, e.g. "postgres".
	Name() string
	// Put writes (inserts or updates) a memory entry and returns its id.
	Put(ctx context.Context, e *MemoryEntry) (string, error)
	// Get fetches a single entry by id.
	Get(ctx context.Context, id string) (*MemoryEntry, error)
	// Search returns entries within scope matching query (empty query = all).
	Search(ctx context.Context, scope, query string, limit int) ([]*MemoryEntry, error)
	// Delete removes an entry by id.
	Delete(ctx context.Context, id string) error
}

// Skill describes one installed or discovered Agent Skill (AgentMux Skills).
type Skill struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Enabled     bool     `json:"enabled"`
	Source      string   `json:"source,omitempty"` // builtin, local, git url...
}

// SkillManager discovers, installs and manages Agent Skills.
type SkillManager interface {
	// Name returns the registered provider id, e.g. "fs".
	Name() string
	// List returns all known skills.
	List(ctx context.Context) ([]Skill, error)
	// Install adds a skill from a source ref (path or url).
	Install(ctx context.Context, ref string) (*Skill, error)
	// SetEnabled toggles a skill by name.
	SetEnabled(ctx context.Context, name string, enabled bool) error
}

// WorkspaceInitOptions describes the agent workspace that must be prepared
// before a local agent runtime starts.
type WorkspaceInitOptions struct {
	AgentID         string          `json:"agent_id,omitempty"`
	AgentName       string          `json:"agent_name,omitempty"`
	RuntimeID       string          `json:"runtime_id,omitempty"`
	WorkDir         string          `json:"work_dir,omitempty"`
	Skills          []string        `json:"skills,omitempty"`
	MCPServers      []string        `json:"mcp_servers,omitempty"`
	RuntimeDefaults RuntimeSettings `json:"runtime_defaults,omitempty"`
}

// ConversationBaseDir returns the root under which per-conversation working
// directories live for the given agent workspace. When the agent configures a
// WorkDir it lives beside it under .agentmux/conversations; otherwise it
// falls back to ~/.agentmux/conversations.
func (o WorkspaceInitOptions) ConversationBaseDir() string {
	return conversationBaseDir(o.WorkDir)
}

// WorkspaceInitResult reports what the initializer created or warned about.
type WorkspaceInitResult struct {
	WorkDir   string   `json:"work_dir"`
	Created   []string `json:"created,omitempty"`
	Updated   []string `json:"updated,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	RuntimeID string   `json:"runtime_id,omitempty"`
	AgentID   string   `json:"agent_id,omitempty"`
}

// WorkspaceInitializer prepares a work directory for one agent run.
type WorkspaceInitializer interface {
	InitializeWorkspace(ctx context.Context, opts WorkspaceInitOptions) (*WorkspaceInitResult, error)
}

// MCPServer is a registered Model Context Protocol server definition
// (AgentMux MCP Registry) that can be distributed to agents/tools.
type MCPServer struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // stdio, sse, http
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	URL       string            `json:"url,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Enabled   bool              `json:"enabled"`
}

// MCPRegistry registers, orchestrates and distributes MCP server configs.
type MCPRegistry interface {
	// Name returns the registered registry id, e.g. "store".
	Name() string
	// List returns all registered MCP servers.
	List(ctx context.Context) ([]MCPServer, error)
	// Upsert inserts or updates an MCP server definition.
	Upsert(ctx context.Context, s *MCPServer) error
	// Delete removes a server by name.
	Delete(ctx context.Context, name string) error
}

// GuardDecision is the outcome of evaluating a tool call against policy.
type GuardDecision string

const (
	// GuardAllow permits the call without human approval.
	GuardAllow GuardDecision = "allow"
	// GuardDeny blocks the call outright.
	GuardDeny GuardDecision = "deny"
	// GuardAsk requires an explicit human approval before proceeding.
	GuardAsk GuardDecision = "ask"
)

// GuardRequest is a tool-call permission request flowing through the gate.
type GuardRequest struct {
	Project string            `json:"project"`
	Tool    string            `json:"tool"`
	Action  string            `json:"action,omitempty"`
	Args    map[string]string `json:"args,omitempty"`
}

// Guard is the permission-approval and policy gate for tool calls
// (AgentMux Guard).
type Guard interface {
	// Name returns the registered guard id, e.g. "policy".
	Name() string
	// Evaluate returns the policy decision for a tool-call request.
	Evaluate(ctx context.Context, req *GuardRequest) (GuardDecision, error)
}
