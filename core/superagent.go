package core

import (
	"encoding/json"
	"time"
)

// Super Agent source kinds. Each kind is one reusable event stream; a
// SuperAgentSource is one configuration of a kind.
const (
	SuperAgentSourceFeishuMessage = "feishu_message"
	SuperAgentSourceFeishuDoc     = "feishu_doc"
	SuperAgentSourceEmail         = "email"
	SuperAgentSourceGitHub        = "github_webhook"
)

// Super Agent modes. Observe records what the router would do without creating
// Tickets, so a new deployment can be tuned before it acts.
const (
	SuperAgentModeAct     = "act"
	SuperAgentModeObserve = "observe"
)

// Trigger dispositions record what happened to each stored event.
const (
	SuperAgentDispositionPending  = "pending"
	SuperAgentDispositionFiltered = "filtered"
	SuperAgentDispositionFollowUp = "follow_up"
	SuperAgentDispositionAccepted = "accepted"
	SuperAgentDispositionIgnored  = "ignored"
	SuperAgentDispositionAnswered = "answered"
	SuperAgentDispositionObserved = "observed"
	SuperAgentDispositionStopped  = "stopped"
	SuperAgentDispositionFailed   = "failed"
)

// Ticket states. A Ticket defines its own state instead of following the
// worker session: a follow-up can resume a ready Ticket on the same worker.
const (
	SuperAgentTicketQueued  = "queued"
	SuperAgentTicketRunning = "running"
	SuperAgentTicketReady   = "ready"
	SuperAgentTicketFailed  = "failed"
	SuperAgentTicketStopped = "stopped"
)

// SuperAgent is a persistent router that turns events from its Sources into
// Tickets and assigns them to local or remote workers.
type SuperAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// RouterRuntime names an installed framework to route with. The host then
	// creates the router Agent, and RouterAgentID records which one it is.
	RouterRuntime  string             `json:"router_runtime,omitempty"`
	RouterAgentID  string             `json:"router_agent_id"`
	RouterPrompt   string             `json:"router_prompt,omitempty"`
	Mode           string             `json:"mode"`
	MaxConcurrency int                `json:"max_concurrency"`
	Workers        []SuperAgentWorker `json:"workers"`
	// AutoWorkers lets the router launch a worker for a Ticket when none on the
	// roster suits it. WorkerRuntimes are the frameworks it may launch one with,
	// and default to the router's. AutoWorkerDir and AutoWorkerApprovalMode are
	// the operator's limits for those workers; a router decision cannot set them.
	AutoWorkers            bool               `json:"auto_workers,omitempty"`
	WorkerRuntimes         []string           `json:"worker_runtimes,omitempty"`
	AutoWorkerDir          string             `json:"auto_worker_dir,omitempty"`
	AutoWorkerApprovalMode string             `json:"auto_worker_approval_mode,omitempty"`
	Sources                []SuperAgentSource `json:"sources"`
	Enabled                bool               `json:"enabled"`
	CreatedAt              time.Time          `json:"created_at"`
	UpdatedAt              time.Time          `json:"updated_at"`
}

// SuperAgentWorker is one Agent the router may assign work to. An empty HostID
// is this daemon; otherwise it names a remote host reached over its SSH tunnel.
type SuperAgentWorker struct {
	ID           string `json:"id"`
	Label        string `json:"label,omitempty"`
	HostID       string `json:"host_id,omitempty"`
	AgentID      string `json:"agent_id"`
	Capabilities string `json:"capabilities,omitempty"`
}

// SuperAgentSource is one event stream. AllowedActors is the fail-closed list,
// in the Source's own identities, of who may originate work through it.
type SuperAgentSource struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	Name          string            `json:"name,omitempty"`
	Enabled       bool              `json:"enabled"`
	AllowedActors []string          `json:"allowed_actors"`
	Config        map[string]string `json:"config,omitempty"`
}

// SuperAgentTrigger is one immutable event. DedupeKey makes intake idempotent,
// so a redelivered webhook or a restarted poller creates one Ticket.
type SuperAgentTrigger struct {
	ID           string          `json:"id"`
	SuperAgentID string          `json:"super_agent_id"`
	SourceID     string          `json:"source_id"`
	SourceKind   string          `json:"source_kind"`
	DedupeKey    string          `json:"dedupe_key"`
	ThreadKey    string          `json:"thread_key"`
	Actor        string          `json:"actor,omitempty"`
	ActorName    string          `json:"actor_name,omitempty"`
	Addressed    bool            `json:"addressed"`
	Title        string          `json:"title,omitempty"`
	Text         string          `json:"text"`
	Reply        json.RawMessage `json:"reply,omitempty"`
	Disposition  string          `json:"disposition"`
	Reason       string          `json:"reason,omitempty"`
	TicketID     string          `json:"ticket_id,omitempty"`
	ReceivedAt   time.Time       `json:"received_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// SuperAgentTicket is one unit of authorized work and its current Assignment.
// ConversationID is stable, so retries and follow-ups continue the same native
// session on the same worker, whether that worker is local or remote.
type SuperAgentTicket struct {
	ID             string           `json:"id"`
	SuperAgentID   string           `json:"super_agent_id"`
	SourceID       string           `json:"source_id"`
	SourceKind     string           `json:"source_kind"`
	ThreadKey      string           `json:"thread_key"`
	TriggerID      string           `json:"trigger_id"`
	Title          string           `json:"title"`
	Actor          string           `json:"actor,omitempty"`
	Worker         SuperAgentWorker `json:"worker"`
	ConversationID string           `json:"conversation_id"`
	Status         string           `json:"status"`
	Inputs         []string         `json:"inputs,omitempty"`
	Output         string           `json:"output,omitempty"`
	Error          string           `json:"error,omitempty"`
	ReplyError     string           `json:"reply_error,omitempty"`
	Reply          json.RawMessage  `json:"reply,omitempty"`
	Attempts       int              `json:"attempts"`
	CreatedAt      time.Time        `json:"created_at"`
	StartedAt      time.Time        `json:"started_at,omitempty"`
	FinishedAt     time.Time        `json:"finished_at,omitempty"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// SuperAgentWorkerStatus is the router's and the Console's view of a worker.
type SuperAgentWorkerStatus struct {
	WorkerID  string `json:"worker_id"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}
