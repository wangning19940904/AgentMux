package core

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// RelayEvent is the versioned envelope delivered to external subscribers.
type RelayEvent struct {
	SchemaVersion string            `json:"schema_version"`
	ID            string            `json:"id"`
	Source        string            `json:"source"`
	Type          string            `json:"type"`
	SourceRef     string            `json:"source_ref"`
	SourceEventID string            `json:"source_event_id,omitempty"`
	OccurredAt    time.Time         `json:"occurred_at"`
	ReceivedAt    time.Time         `json:"received_at"`
	Attributes    map[string]string `json:"attributes"`
	Data          json.RawMessage   `json:"data"`
}

type EventSubscription struct {
	ID            string              `json:"id"`
	OwnerTenantID string              `json:"owner_tenant_id,omitempty"`
	Key           string              `json:"key"`
	Name          string              `json:"name"`
	Source        string              `json:"source"`
	SourceRefs    []string            `json:"source_refs"`
	EventTypes    []string            `json:"event_types"`
	Filters       map[string][]string `json:"filters,omitempty"`
	CallbackURL   string              `json:"callback_url"`
	Paused        bool                `json:"paused"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	Deleted       bool                `json:"deleted,omitempty"`
	Secret        string              `json:"-"`
	Pending       int                 `json:"pending"`
	Dead          int                 `json:"dead"`
	LastSuccessAt *time.Time          `json:"last_success_at,omitempty"`
	LastError     string              `json:"last_error,omitempty"`
}

func (s EventSubscription) Matches(e RelayEvent) bool {
	if s.Deleted || s.Source != e.Source || !slices.Contains(s.SourceRefs, e.SourceRef) || !slices.Contains(s.EventTypes, e.Type) {
		return false
	}
	for k, values := range s.Filters {
		value, ok := e.Attributes[k]
		if !ok || !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

type EventDelivery struct {
	ID             string                 `json:"id"`
	SubscriptionID string                 `json:"subscription_id"`
	EventID        string                 `json:"event_id"`
	Status         string                 `json:"status"`
	Attempts       int                    `json:"attempts"`
	NextAttemptAt  time.Time              `json:"next_attempt_at"`
	FirstAttemptAt *time.Time             `json:"first_attempt_at,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	LastError      string                 `json:"last_error,omitempty"`
	Event          *RelayEvent            `json:"event,omitempty"`
	History        []EventDeliveryAttempt `json:"history,omitempty"`
	LeaseToken     string                 `json:"-"`
}

type EventDeliveryAttempt struct {
	ID         string     `json:"id"`
	DeliveryID string     `json:"delivery_id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	HTTPStatus int        `json:"http_status"`
	Error      string     `json:"error,omitempty"`
}

type EventSource struct {
	Ref              string               `json:"ref"`
	Name             string               `json:"name"`
	Sources          []string             `json:"sources"`
	EventTypes       []string             `json:"event_types"`
	Connected        bool                 `json:"connected"`
	State            string               `json:"state"`
	PlatformVerified bool                 `json:"platform_verified"`
	Ingestion        EventIngestionHealth `json:"ingestion"`
}

type EventIngestionHealth struct {
	Failures      uint64     `json:"failures"`
	LastError     string     `json:"last_error,omitempty"`
	LastFailureAt *time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
}

// PlatformEventIngress is injected into an adapter without importing a store.
// EventTypes is read when building an immutable SDK dispatcher.
type PlatformEventIngress struct {
	Publish    func(context.Context, RelayEvent) error
	EventTypes func() []string
}

func LifecycleEventTypes() []string {
	return []string{string(HookMessageReceived), string(HookMessageSent), string(HookSessionStarted), string(HookSessionEnded), string(HookCronTriggered), string(HookWebhookTriggered), string(HookPermission), string(HookTaskQueued), string(HookTaskStarted), string(HookTaskSteered), string(HookTaskTakenOver), string(HookTaskInterrupted), string(HookTaskCompleted), string(HookInteractionResolved), string(HookFeedbackReceived), string(HookThreadBound), string(HookError)}
}

func LifecycleSourceRef(data map[string]string) string {
	for _, kind := range []string{"channel", "trigger", "agent"} {
		if id := strings.TrimSpace(data[kind+"_id"]); id != "" {
			return kind + ":" + id
		}
	}
	return "system"
}

// ConfigureEventIngress is called by bootstrap before channels start.
func (e *Engine) ConfigureEventIngress(factory func(Channel) PlatformEventIngress) {
	e.eventIngress = factory
}
