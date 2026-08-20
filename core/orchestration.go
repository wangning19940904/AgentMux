package core

import "time"

type OrchestrationStatus string

const (
	OrchestrationQueued    OrchestrationStatus = "queued"
	OrchestrationRunning   OrchestrationStatus = "running"
	OrchestrationSucceeded OrchestrationStatus = "succeeded"
	OrchestrationFailed    OrchestrationStatus = "failed"
	OrchestrationCancelled OrchestrationStatus = "cancelled"
)

type Orchestration struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	Status         OrchestrationStatus `json:"status"`
	MaxConcurrency int                 `json:"max_concurrency"`
	Error          string              `json:"error,omitempty"`
	Tasks          []OrchestrationTask `json:"tasks,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	StartedAt      time.Time           `json:"started_at,omitempty"`
	FinishedAt     time.Time           `json:"finished_at,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

type OrchestrationTask struct {
	ID              string              `json:"id"`
	OrchestrationID string              `json:"orchestration_id,omitempty"`
	AgentID         string              `json:"agent_id,omitempty"`
	Project         string              `json:"project,omitempty"`
	Input           string              `json:"input"`
	DependsOn       []string            `json:"depends_on,omitempty"`
	Status          OrchestrationStatus `json:"status"`
	Output          string              `json:"output,omitempty"`
	Error           string              `json:"error,omitempty"`
	InvocationID    string              `json:"invocation_id,omitempty"`
	ConversationID  string              `json:"conversation_id,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	StartedAt       time.Time           `json:"started_at,omitempty"`
	FinishedAt      time.Time           `json:"finished_at,omitempty"`
	UpdatedAt       time.Time           `json:"updated_at"`
}
