package core

import "time"

// Push notification kinds a person can turn on per device.
const (
	// PushKindWaiting: an agent is waiting for a person to approve something
	// or answer a question.
	PushKindWaiting = "waiting"
	// PushKindTickets: a Super Agent Ticket finished or failed.
	PushKindTickets = "tickets"
	// PushKindMachines: a linked machine went offline or came back.
	PushKindMachines = "machines"
)

// PushKinds lists every kind, in the order the Console shows them.
func PushKinds() []string {
	return []string{PushKindWaiting, PushKindTickets, PushKindMachines}
}

// PushSubscription is one browser on one device that asked to be notified.
// Endpoint and the two keys come from the browser's PushManager. Endpoint is
// a capability: anyone holding it with the keys can notify that browser, so
// it is never returned by the API.
type PushSubscription struct {
	ID string `json:"id"`
	// Subscriber is who the subscription belongs to: "admin" for the
	// administrator, "tenant:<id>" for a tenant. Notifications are addressed
	// to subscribers, so a later user identity only needs a new form here.
	Subscriber string   `json:"subscriber"`
	Endpoint   string   `json:"-"`
	P256dh     string   `json:"-"`
	Auth       string   `json:"-"`
	Kinds      []string `json:"kinds"`
	Label      string   `json:"label,omitempty"`
	// Lang is the Console language ("en" or "zh") notifications are written in.
	Lang          string    `json:"lang,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

// Wants reports whether the subscription receives notifications of kind.
func (s PushSubscription) Wants(kind string) bool {
	for _, k := range s.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// PushVAPIDKeys is the hub's application server key pair, base64url encoded
// the way browsers and the Web Push protocol expect.
type PushVAPIDKeys struct {
	PublicKey  string
	PrivateKey string
	CreatedAt  time.Time
}
