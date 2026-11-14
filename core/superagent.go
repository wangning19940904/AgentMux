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
	// SuperAgentSourceChannel is a channel bound to the agent's brain
	// (channels.agent_id). It is derived from the binding, never stored: the
	// channel engine runs the brain's turn, and the channel's own reply scope,
	// not an allowed list, decides who reaches it.
	SuperAgentSourceChannel = "channel"
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

// SuperAgentTriggerTicketUpdate tells the router that a Ticket's worker
// finished a turn, so it can report it, start dependent work or ask someone
// for a decision. An event from a Source has no kind.
const SuperAgentTriggerTicketUpdate = "ticket_update"

// SuperAgentTriggerTimer tells the router that a timer it set is due. Like a
// ticket update it comes from AgentMux, on the thread the timer was set on.
const SuperAgentTriggerTimer = "timer"

// SuperAgentTimerFollowUp is a one-off wake-up the router set for itself.
const SuperAgentTimerFollowUp = "follow_up"

// Timer states. A follow-up is done once it has fired.
const (
	SuperAgentTimerActive    = "active"
	SuperAgentTimerDone      = "done"
	SuperAgentTimerCancelled = "cancelled"
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

// SuperAgent is an agent: AgentMux has one kind. It has an identity, a home
// with memory and Tickets, a brain and the sources and worker policy around
// it. The brain is the Agent instance RouterAgentID names: its framework,
// model, approval mode and directory. An agent created through the Console or
// the agent-instances API shares its id with its brain, so channel bindings,
// grants and invocations that name the id reach the brain. The brain answers
// directly when it can and starts workers, as Tickets, when it cannot.
type SuperAgent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// RouterRuntime names an installed framework to route with. The host then
	// creates the router Agent, and RouterAgentID records which one it is.
	RouterRuntime string `json:"router_runtime,omitempty"`
	RouterAgentID string `json:"router_agent_id"`
	RouterPrompt  string `json:"router_prompt,omitempty"`
	// Prompts are the operator's own text for built-in prompt parts, by part
	// id. A part without one follows the built-in default.
	Prompts        map[string]SuperAgentPrompt `json:"prompts,omitempty"`
	Mode           string                      `json:"mode"`
	MaxConcurrency int                         `json:"max_concurrency"`
	Workers        []SuperAgentWorker          `json:"workers"`
	// AutoWorkers lets the router launch a worker for a Ticket when none on the
	// roster suits it. WorkerRuntimes are the frameworks it may launch one with,
	// and default to the router's. AutoWorkerDir and AutoWorkerApprovalMode are
	// the operator's limits for those workers; a router decision cannot set them.
	AutoWorkers            bool     `json:"auto_workers,omitempty"`
	WorkerRuntimes         []string `json:"worker_runtimes,omitempty"`
	AutoWorkerDir          string   `json:"auto_worker_dir,omitempty"`
	AutoWorkerApprovalMode string   `json:"auto_worker_approval_mode,omitempty"`
	// WorkerMachines are the directories the router may launch a worker in
	// besides this machine's default one: paths on this machine, and paths on
	// other machines. Like the rest of the auto worker settings they are the
	// operator's: a router decision picks among them and cannot add one.
	WorkerMachines []SuperAgentMachine `json:"worker_machines,omitempty"`
	Sources        []SuperAgentSource  `json:"sources"`
	Enabled        bool                `json:"enabled"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

// SuperAgentPrompt is an operator's text for one built-in prompt part. Base
// names the version of the default it was written against, so the Console can
// say when the default has changed since.
type SuperAgentPrompt struct {
	Text string `json:"text"`
	Base string `json:"base,omitempty"`
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

// SuperAgentMachine is a directory an agent may launch workers in. An empty
// HostID is this machine; otherwise it names a device linked to this hub or an
// SSH host. Dir is a directory on that machine and Capabilities tells the
// router what it is good for. An empty Dir, allowed only on another machine,
// gives the workers a directory of their own there. ID names the entry on the
// roster, as auto:<framework>@<id>; it is assigned when the agent is saved,
// and an entry saved before IDs existed goes by its HostID.
type SuperAgentMachine struct {
	ID           string `json:"id,omitempty"`
	HostID       string `json:"host_id"`
	Dir          string `json:"dir,omitempty"`
	Capabilities string `json:"capabilities,omitempty"`
}

// SuperAgentMachineStatus is what an agent's machine can run right now.
type SuperAgentMachineStatus struct {
	HostID    string   `json:"host_id"`
	Name      string   `json:"name,omitempty"`
	Available bool     `json:"available"`
	Runtimes  []string `json:"runtimes,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

// HostedWorkerRequest asks a machine to keep a worker for an agent that lives
// on another machine. The machine that receives it decides what the worker
// may be: it checks the framework, the directory and the approval mode
// against what it has, exactly as it would for an agent of its own.
type HostedWorkerRequest struct {
	OwnerID string `json:"owner_id"`
	// PathID tells apart workers of one framework in different directories
	// on the machine. Empty keeps the single worker per framework.
	PathID       string `json:"path_id,omitempty"`
	OwnerName    string `json:"owner_name,omitempty"`
	RuntimeID    string `json:"runtime_id"`
	Dir          string `json:"dir,omitempty"`
	ApprovalMode string `json:"approval_mode,omitempty"`
}

// HostedWorker is the Agent a machine keeps for a HostedWorkerRequest.
type HostedWorker struct {
	AgentID       string `json:"agent_id"`
	Name          string `json:"name"`
	WorkDir       string `json:"work_dir"`
	WorkspaceMode string `json:"workspace_mode"`
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
	Kind         string          `json:"kind,omitempty"`
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
	// DependsOn holds a queued Ticket until each of these is ready. A new
	// worktree starts from BaseTicket's latest commit instead of the default.
	DependsOn  []string `json:"depends_on,omitempty"`
	BaseTicket string   `json:"base_ticket,omitempty"`
	// Links are the other threads that steered or asked about this Ticket, so
	// the router can tell them when it moves.
	Links      []SuperAgentTicketLink `json:"links,omitempty"`
	Attempts   int                    `json:"attempts"`
	CreatedAt  time.Time              `json:"created_at"`
	StartedAt  time.Time              `json:"started_at,omitempty"`
	FinishedAt time.Time              `json:"finished_at,omitempty"`
	UpdatedAt  time.Time              `json:"updated_at"`
}

// SuperAgentTicketLink is a thread other than a Ticket's own that cares about
// it, with the address a message to that thread goes to.
type SuperAgentTicketLink struct {
	ThreadKey string          `json:"thread_key"`
	SourceID  string          `json:"source_id"`
	Reply     json.RawMessage `json:"reply,omitempty"`
}

// SuperAgentTimer wakes an agent's router later. A due timer never starts a
// turn itself: it becomes a Trigger on ThreadKey, so it is ordered, retried
// and recorded like any other event. SourceID, SourceKind and Reply are those
// of the Trigger that set it, so the router's reply goes back to the same place.
type SuperAgentTimer struct {
	ID           string          `json:"id"`
	SuperAgentID string          `json:"super_agent_id"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	ThreadKey    string          `json:"thread_key"`
	SourceID     string          `json:"source_id"`
	SourceKind   string          `json:"source_kind"`
	Reply        json.RawMessage `json:"reply,omitempty"`
	// TicketID is the Ticket a follow-up is about, if any. Stopping that
	// Ticket cancels the follow-up.
	TicketID string `json:"ticket_id,omitempty"`
	// Note is what the router told its later self to do when the timer fires.
	Note  string    `json:"note"`
	DueAt time.Time `json:"due_at"`
	// SetBy is the Trigger whose turn set the timer.
	SetBy     string    `json:"set_by,omitempty"`
	FiredAt   time.Time `json:"fired_at,omitzero"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SuperAgentWorkerStatus is the router's and the Console's view of a worker.
type SuperAgentWorkerStatus struct {
	WorkerID  string `json:"worker_id"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

// FeishuUserGrant is one person's own authorization of a Feishu channel's app.
// With it the Console lists that person's groups and contacts and adds the
// channel's bot to a group as them, so the bot needs no scope to read people
// or chats it is not in. The tokens never leave the daemon.
type FeishuUserGrant struct {
	ChannelID        string    `json:"channel_id"`
	OpenID           string    `json:"open_id"`
	Name             string    `json:"name,omitempty"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
	Scopes           []string  `json:"scopes,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}
