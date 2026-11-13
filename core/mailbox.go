package core

import (
	"strings"
	"time"
)

// The mailbox carries messages between agents, and between people and
// agents, on one machine and across the machines linked to a hub.
//
// An address names one party the way the machine holding the message sees it:
//
//	agent:<id>             an agent on this machine
//	agent:<id>@<device>    an agent on a machine linked to this hub
//	agent:<id>@hub         an agent on this machine's hub
//	person:<login>         someone with an account on this machine
//	person:<login>@hub     someone with an account on this machine's hub
//
// A conversation is the messages between two addresses. There are no group
// conversations.

// SuperAgentSourceMailbox is the source kind of an event that arrived as a
// mailbox message. It is derived, never stored on an agent: who may write to
// an agent is decided by ownership and contacts, not by an allowed list.
const SuperAgentSourceMailbox = "mailbox"

const (
	MailPartyAgent  = "agent"
	MailPartyPerson = "person"
	// MailHub is the machine part of an address that names this machine's hub.
	MailHub = "hub"
)

// Contact states. A contact between parties of different owners starts
// pending and carries messages only once the other owner accepts it.
const (
	MailContactPending  = "pending"
	MailContactAccepted = "accepted"
)

// MailAddress is a parsed address.
type MailAddress struct {
	Kind    string // MailPartyAgent or MailPartyPerson
	ID      string // agent id or account login
	Machine string // empty for this machine; a device id or MailHub otherwise
}

// ParseMailAddress reads an address. ok is false for anything else.
func ParseMailAddress(value string) (MailAddress, bool) {
	kind, rest, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found || (kind != MailPartyAgent && kind != MailPartyPerson) {
		return MailAddress{}, false
	}
	id, machine, _ := strings.Cut(rest, "@")
	if id == "" || strings.ContainsAny(rest, " \t\r\n") || strings.Contains(machine, "@") || strings.HasSuffix(rest, "@") {
		return MailAddress{}, false
	}
	return MailAddress{Kind: kind, ID: id, Machine: machine}, true
}

func (a MailAddress) String() string {
	if a.Machine == "" {
		return a.Kind + ":" + a.ID
	}
	return a.Kind + ":" + a.ID + "@" + a.Machine
}

// Local reports whether the address names a party on this machine.
func (a MailAddress) Local() bool { return a.Machine == "" }

// MailPair is the key of the conversation between two addresses.
func MailPair(a, b string) string {
	if b < a {
		a, b = b, a
	}
	return a + " " + b
}

// MailMessage is one message. DeliveredAt is set once the message reached
// its recipient's machine (or, for a person, their inbox); until then the
// machine holding it keeps trying.
type MailMessage struct {
	ID          string     `json:"id"`
	From        string     `json:"from"`
	FromName    string     `json:"from_name,omitempty"`
	To          string     `json:"to"`
	ToName      string     `json:"to_name,omitempty"`
	Text        string     `json:"text"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	Attempts    int        `json:"attempts,omitempty"`
	Error       string     `json:"error,omitempty"`
	// FailedAt is set when AgentMux gave up delivering the message.
	FailedAt *time.Time `json:"failed_at,omitempty"`
}

// MailContact lets two parties of different owners write to each other.
// A and B are in address order.
type MailContact struct {
	ID     string `json:"id"`
	A      string `json:"a"`
	AName  string `json:"a_name,omitempty"`
	B      string `json:"b"`
	BName  string `json:"b_name,omitempty"`
	Status string `json:"status"`
	// RequestedBy is the person who asked for the contact. WaitingFor is the
	// side whose owner has not accepted it yet.
	RequestedBy string     `json:"requested_by,omitempty"`
	WaitingFor  string     `json:"waiting_for,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
}

// MailParty is one entry of the directory: someone a message can go to.
type MailParty struct {
	Address     string `json:"address"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Machine is the name of the machine an agent lives on; empty for this one.
	Machine string `json:"machine,omitempty"`
	// Owner is the namespace that owns the party; empty means the
	// administrators. OwnerName is shown to people.
	Owner     string `json:"-"`
	OwnerName string `json:"owner_name,omitempty"`
	// Admin marks a person who administers this machine.
	Admin  bool `json:"-"`
	Online bool `json:"online"`
}

// MailConversation is the latest state of the messages between two parties.
type MailConversation struct {
	Peer     MailParty   `json:"peer"`
	Self     MailParty   `json:"self"`
	Last     MailMessage `json:"last"`
	Unread   int         `json:"unread"`
	Messages int         `json:"messages"`
}
