// Package mailbox carries messages between agents, and between people and
// agents, on one machine and across the machines linked to a hub.
//
// Every machine keeps the messages its own parties sent and received. A
// message for a party on another machine travels through the hub: a node
// sends it up its link, the hub checks that the two parties may write to
// each other, keeps it, and passes it down the recipient's link. Whichever
// machine holds an undelivered message keeps trying, so a machine that is
// offline gets its mail when it comes back.
//
// Who may write to whom: parties of the same owner, parties with an accepted
// contact, an administrator to anyone, and anyone answering a party that
// wrote to them first.
package mailbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	// MaxMessageRunes bounds one message.
	MaxMessageRunes = 16000
	// agentExchangeLimit bounds how many messages two agents trade in
	// agentExchangeWindow, so two agents answering each other cannot run on
	// without a person noticing.
	agentExchangeLimit  = 40
	agentExchangeWindow = time.Hour

	retryInterval   = 20 * time.Second
	giveUpAfter     = 7 * 24 * time.Hour
	deliveryTimeout = 20 * time.Second
	retryBatch      = 200
	directoryTTL    = time.Minute
	recentLimit     = 5000
	historyLimit    = 500
)

// Store is the part of the store the mailbox keeps its records in.
type Store interface {
	InsertMailMessage(context.Context, core.MailMessage) (bool, error)
	GetMailMessage(context.Context, string) (*core.MailMessage, error)
	ListMailConversation(ctx context.Context, a, b string, limit int) ([]core.MailMessage, error)
	ListRecentMail(context.Context, int) ([]core.MailMessage, error)
	ListUndeliveredMail(context.Context, int) ([]core.MailMessage, error)
	CountMailSince(ctx context.Context, a, b string, since time.Time) (int, error)
	HasMailFrom(ctx context.Context, from, to string) (bool, error)
	MarkMailDelivered(context.Context, string, time.Time) error
	MarkMailFailed(ctx context.Context, id, problem string, at time.Time) error
	RecordMailAttempt(ctx context.Context, id, problem string) error
	MarkMailRead(ctx context.Context, reader, from string, at time.Time) error
	UpsertMailContact(context.Context, core.MailContact) error
	GetMailContact(ctx context.Context, a, b string) (*core.MailContact, error)
	GetMailContactByID(context.Context, string) (*core.MailContact, error)
	ListMailContacts(context.Context) ([]core.MailContact, error)
	DeleteMailContact(context.Context, string) error
}

// Directory names the parties this machine knows: its own agents, the people
// with accounts on it and, on a hub, the agents of its linked machines.
type Directory interface {
	Parties(ctx context.Context) ([]core.MailParty, error)
	// Device describes a linked machine: who owns it and what it is called.
	Device(ctx context.Context, deviceID string) (owner, name string, known bool)
}

// Agents hands a message to an agent on this machine.
type Agents interface {
	ReceiveMail(ctx context.Context, agentID string, message core.MailMessage) error
}

// Devices is what a hub asks of a linked machine.
type Devices interface {
	// Deliver passes a message, written the way the machine sees addresses,
	// down that machine's link.
	Deliver(ctx context.Context, deviceID string, message core.MailMessage) error
}

// Hub is what a node asks of the hub it joined.
type Hub interface {
	Joined() bool
	// Send passes a message up the link, written the way this machine sees
	// addresses.
	Send(ctx context.Context, message core.MailMessage) error
	// Reachable lists who one of this machine's agents may write to
	// elsewhere, written the way this machine sees addresses.
	Reachable(ctx context.Context, agentID string) ([]core.MailParty, error)
}

// Refusal is a message that will never be deliverable as it stands: the
// parties may not write to each other, or the recipient does not exist.
type Refusal struct{ Reason string }

func (r Refusal) Error() string { return r.Reason }

// Refuse makes a Refusal.
func Refuse(format string, args ...any) error { return Refusal{Reason: fmt.Sprintf(format, args...)} }

// IsRefusal reports whether err says a message can never be delivered, as
// opposed to a path that is down for now.
func IsRefusal(err error) bool {
	var refusal Refusal
	return errors.As(err, &refusal)
}

// ErrForbidden is returned when a person may not do what they asked.
var ErrForbidden = errors.New("not allowed")

// Viewer is the person using the Console.
type Viewer struct {
	Address string
	Name    string
	Owner   string
	Admin   bool
}

func (v Viewer) party() core.MailParty {
	return core.MailParty{Address: v.Address, Kind: core.MailPartyPerson, Name: v.Name, Owner: v.Owner, Admin: v.Admin, Online: true}
}

func (v Viewer) controls(party core.MailParty) bool {
	if v.Admin {
		return true
	}
	if party.Kind == core.MailPartyPerson {
		return party.Address == v.Address
	}
	return party.Owner != "" && party.Owner == v.Owner
}

// Options holds the service's collaborators. Agents, Devices, Hub and
// Notify may be set later with their setters.
type Options struct {
	Store     Store
	Directory Directory
	Agents    Agents
	Devices   Devices
	Hub       Hub
	// Notify tells a person on this machine that a message arrived.
	Notify func(ctx context.Context, login string, message core.MailMessage)
	Log    *slog.Logger
	Now    func() time.Time
}

// Service is one machine's mailbox.
type Service struct {
	store     Store
	directory Directory
	log       *slog.Logger
	now       func() time.Time
	wake      chan struct{}

	mu      sync.Mutex
	agents  Agents
	devices Devices
	hub     Hub
	notify  func(ctx context.Context, login string, message core.MailMessage)
	remote  map[string]remoteDirectory
}

type remoteDirectory struct {
	at      time.Time
	parties []core.MailParty
}

func New(options Options) *Service {
	log := options.Log
	if log == nil {
		log = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: options.Store, directory: options.Directory, log: log, now: now, wake: make(chan struct{}, 1),
		agents: options.Agents, devices: options.Devices, hub: options.Hub, notify: options.Notify,
		remote: map[string]remoteDirectory{},
	}
}

func (s *Service) SetAgents(agents Agents) {
	s.mu.Lock()
	s.agents = agents
	s.mu.Unlock()
}

func (s *Service) SetDevices(devices Devices) {
	s.mu.Lock()
	s.devices = devices
	s.mu.Unlock()
}

func (s *Service) SetHub(hub Hub) {
	s.mu.Lock()
	s.hub = hub
	s.mu.Unlock()
}

func (s *Service) SetNotify(notify func(ctx context.Context, login string, message core.MailMessage)) {
	s.mu.Lock()
	s.notify = notify
	s.mu.Unlock()
}

func (s *Service) collaborators() (Agents, Devices, Hub, func(context.Context, string, core.MailMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents, s.devices, s.hub, s.notify
}

// Run retries undelivered messages until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	for {
		s.Retry(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// Wake retries undelivered messages soon, for example when a machine
// reconnects.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Retry tries every undelivered message once.
func (s *Service) Retry(ctx context.Context) {
	pending, err := s.store.ListUndeliveredMail(ctx, retryBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("mailbox: list undelivered messages failed", "err", err)
		}
		return
	}
	for _, message := range pending {
		if ctx.Err() != nil {
			return
		}
		if s.now().Sub(message.CreatedAt) > giveUpAfter {
			_ = s.store.MarkMailFailed(ctx, message.ID, "the recipient could not be reached for 7 days", s.now().UTC())
			continue
		}
		s.attempt(ctx, message)
	}
}

// attempt routes a stored message and records the outcome. It returns the
// refusal when the message can never be delivered.
func (s *Service) attempt(ctx context.Context, message core.MailMessage) error {
	routeCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	err := s.route(routeCtx, message)
	switch {
	case err == nil:
		if err := s.store.MarkMailDelivered(ctx, message.ID, s.now().UTC()); err != nil {
			s.log.Warn("mailbox: mark delivered failed", "message", message.ID, "err", err)
		}
		return nil
	case IsRefusal(err):
		_ = s.store.MarkMailFailed(ctx, message.ID, err.Error(), s.now().UTC())
		return err
	default:
		if ctx.Err() == nil {
			_ = s.store.RecordMailAttempt(ctx, message.ID, err.Error())
		}
		return nil
	}
}

// route moves a message one step towards its recipient.
func (s *Service) route(ctx context.Context, message core.MailMessage) error {
	to, ok := core.ParseMailAddress(message.To)
	if !ok {
		return Refuse("%q is not an address", message.To)
	}
	agents, devices, hub, notify := s.collaborators()
	switch {
	case to.Local() && to.Kind == core.MailPartyPerson:
		if notify != nil {
			notify(ctx, to.ID, message)
		}
		return nil
	case to.Local():
		if agents == nil {
			return errors.New("agents are not running on this machine")
		}
		return agents.ReceiveMail(ctx, to.ID, message)
	case to.Machine != core.MailHub && devices != nil && s.knownDevice(ctx, to.Machine):
		return devices.Deliver(ctx, to.Machine, viewFromDevice(message, to.Machine))
	case hub != nil && hub.Joined():
		return hub.Send(ctx, message)
	}
	return Refuse("%s is not on a machine this AgentMux is connected to", message.To)
}

func (s *Service) knownDevice(ctx context.Context, deviceID string) bool {
	if s.directory == nil {
		return false
	}
	_, _, known := s.directory.Device(ctx, deviceID)
	return known
}

// Send sends a message from a party on this machine. The message is kept
// and delivered now when its recipient can be reached, and later otherwise.
// A Refusal means it was not sent at all.
func (s *Service) Send(ctx context.Context, from, to, text string) (core.MailMessage, error) {
	sender, err := s.localParty(ctx, from)
	if err != nil {
		return core.MailMessage{}, err
	}
	return s.send(ctx, sender, to, text)
}

// SendAs sends a message from the person using the Console.
func (s *Service) SendAs(ctx context.Context, viewer Viewer, to, text string) (core.MailMessage, error) {
	return s.send(ctx, viewer.party(), to, text)
}

func (s *Service) send(ctx context.Context, sender core.MailParty, to, text string) (core.MailMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return core.MailMessage{}, Refuse("a message needs text")
	}
	if utf8.RuneCountInString(text) > MaxMessageRunes {
		text = string([]rune(text)[:MaxMessageRunes])
	}
	address, ok := core.ParseMailAddress(to)
	if !ok {
		return core.MailMessage{}, Refuse("%q is not an address", to)
	}
	to = address.String()
	if to == sender.Address {
		return core.MailMessage{}, Refuse("a message needs a recipient other than its sender")
	}
	message := core.MailMessage{
		ID: "mail_" + uuid.NewString(), From: sender.Address, FromName: sender.Name, To: to, Text: text, CreatedAt: s.now().UTC(),
	}
	recipient, known, err := s.party(ctx, to)
	if err != nil {
		return core.MailMessage{}, err
	}
	if known {
		// Both parties are known here, so this machine decides.
		if err := s.check(ctx, sender, recipient); err != nil {
			return core.MailMessage{}, err
		}
		message.ToName = recipient.Name
	} else {
		// The recipient is elsewhere: the hub decides when the message
		// reaches it.
		_, _, hub, _ := s.collaborators()
		if address.Local() || hub == nil || !hub.Joined() {
			return core.MailMessage{}, Refuse("there is no %s to write to", describeAddress(address))
		}
		if sender.Kind != core.MailPartyAgent {
			return core.MailMessage{}, Refuse("use the hub's Console to write to %s", to)
		}
		if remote, found := s.remoteParty(ctx, sender, to); found {
			message.ToName = remote.Name
		}
	}
	if _, err := s.store.InsertMailMessage(ctx, message); err != nil {
		return core.MailMessage{}, err
	}
	if err := s.attempt(ctx, message); err != nil {
		return core.MailMessage{}, err
	}
	if stored, err := s.store.GetMailMessage(ctx, message.ID); err == nil && stored != nil {
		message = *stored
	}
	return message, nil
}

// check decides whether one party may write to another, and refuses a pair
// of agents that has traded too many messages lately.
func (s *Service) check(ctx context.Context, from, to core.MailParty) error {
	allowed, err := s.allowed(ctx, from, to)
	if err != nil {
		return err
	}
	if !allowed {
		return Refuse("%s and %s are not contacts, so %s cannot write to %s", from.Name, to.Name, from.Name, to.Name)
	}
	if from.Kind == core.MailPartyAgent && to.Kind == core.MailPartyAgent {
		count, err := s.store.CountMailSince(ctx, from.Address, to.Address, s.now().Add(-agentExchangeWindow))
		if err != nil {
			return err
		}
		if count >= agentExchangeLimit {
			return Refuse("%s and %s have traded %d messages in the last hour; a person should look before they continue", from.Name, to.Name, count)
		}
	}
	return nil
}

// allowed is the rule for who may write to whom.
func (s *Service) allowed(ctx context.Context, from, to core.MailParty) (bool, error) {
	if from.Kind == core.MailPartyPerson && to.Kind == core.MailPartyPerson {
		return false, nil
	}
	if from.Owner == to.Owner || (from.Kind == core.MailPartyPerson && from.Admin) {
		return true, nil
	}
	contact, err := s.store.GetMailContact(ctx, from.Address, to.Address)
	if err != nil {
		return false, err
	}
	if contact != nil && contact.Status == core.MailContactAccepted {
		return true, nil
	}
	// Anyone may answer a party that wrote to them first.
	return s.store.HasMailFrom(ctx, to.Address, from.Address)
}

// AcceptFromDevice takes a message a linked machine sent up its link. The
// link names the machine, so the sender cannot claim to be anywhere else.
func (s *Service) AcceptFromDevice(ctx context.Context, deviceID string, message core.MailMessage) error {
	message = viewFromHub(message, deviceID)
	from, ok := core.ParseMailAddress(message.From)
	if !ok || from.Kind != core.MailPartyAgent || from.Machine != deviceID {
		return Refuse("a machine sends messages only for its own agents")
	}
	to, ok := core.ParseMailAddress(message.To)
	if !ok {
		return Refuse("%q is not an address", message.To)
	}
	if to.Machine == deviceID {
		return Refuse("a message between two agents on one machine stays on that machine")
	}
	if strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.Text) == "" {
		return Refuse("a message needs an id and text")
	}
	if existing, err := s.store.GetMailMessage(ctx, message.ID); err != nil {
		return err
	} else if existing != nil {
		return nil
	}
	sender := s.deviceSender(ctx, deviceID, from, message.FromName)
	recipient, known, err := s.party(ctx, message.To)
	if err != nil {
		return err
	}
	if !known {
		return Refuse("there is no %s to write to", describeAddress(to))
	}
	if err := s.check(ctx, sender, recipient); err != nil {
		return err
	}
	message.FromName, message.ToName = sender.Name, recipient.Name
	message.DeliveredAt, message.ReadAt, message.FailedAt, message.Attempts, message.Error = nil, nil, nil, 0, ""
	if message.CreatedAt.IsZero() || message.CreatedAt.After(s.now().Add(time.Minute)) {
		message.CreatedAt = s.now().UTC()
	}
	if utf8.RuneCountInString(message.Text) > MaxMessageRunes {
		message.Text = string([]rune(message.Text)[:MaxMessageRunes])
	}
	if _, err := s.store.InsertMailMessage(ctx, message); err != nil {
		return err
	}
	return s.attempt(ctx, message)
}

// deviceSender is the party behind a message from a linked machine: the
// directory's entry, or one made from the machine itself when its agent
// list has not reached the hub yet.
func (s *Service) deviceSender(ctx context.Context, deviceID string, from core.MailAddress, name string) core.MailParty {
	if party, known, err := s.party(ctx, from.String()); err == nil && known {
		return party
	}
	party := core.MailParty{Address: from.String(), Kind: core.MailPartyAgent, Name: firstNonEmpty(name, from.ID)}
	if s.directory != nil {
		party.Owner, party.Machine, _ = s.directory.Device(ctx, deviceID)
	}
	return party
}

// ReceiveFromHub takes a message the hub passed down this machine's link.
// The hub already decided that its sender may write to its recipient.
func (s *Service) ReceiveFromHub(ctx context.Context, message core.MailMessage) error {
	to, ok := core.ParseMailAddress(message.To)
	if !ok || !to.Local() || to.Kind != core.MailPartyAgent {
		return Refuse("%q is not an agent on this machine", message.To)
	}
	if from, ok := core.ParseMailAddress(message.From); !ok || from.Local() {
		return Refuse("%q is not a sender on another machine", message.From)
	}
	if strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.Text) == "" {
		return Refuse("a message needs an id and text")
	}
	if existing, err := s.store.GetMailMessage(ctx, message.ID); err != nil {
		return err
	} else if existing != nil && existing.DeliveredAt != nil {
		return nil
	}
	message.DeliveredAt, message.ReadAt, message.FailedAt, message.Attempts, message.Error = nil, nil, nil, 0, ""
	if _, err := s.store.InsertMailMessage(ctx, message); err != nil {
		return err
	}
	agents, _, _, _ := s.collaborators()
	if agents == nil {
		return errors.New("agents are not running on this machine")
	}
	if err := agents.ReceiveMail(ctx, to.ID, message); err != nil {
		if IsRefusal(err) {
			_ = s.store.MarkMailFailed(ctx, message.ID, err.Error(), s.now().UTC())
		}
		return err
	}
	return s.store.MarkMailDelivered(ctx, message.ID, s.now().UTC())
}

// Reachable lists who a party on this machine may write to: parties here
// and, for an agent on a node, parties elsewhere that the hub allows.
func (s *Service) Reachable(ctx context.Context, from string) ([]core.MailParty, error) {
	sender, err := s.localParty(ctx, from)
	if err != nil {
		return nil, err
	}
	return s.reachable(ctx, sender)
}

// ReachableAs lists who the person using the Console may write to.
func (s *Service) ReachableAs(ctx context.Context, viewer Viewer) ([]core.MailParty, error) {
	return s.reachable(ctx, viewer.party())
}

func (s *Service) reachable(ctx context.Context, sender core.MailParty) ([]core.MailParty, error) {
	parties, err := s.parties(ctx)
	if err != nil {
		return nil, err
	}
	out := []core.MailParty{}
	for _, party := range parties {
		if party.Address == sender.Address {
			continue
		}
		if allowed, err := s.allowed(ctx, sender, party); err != nil {
			return nil, err
		} else if allowed {
			out = append(out, party)
		}
	}
	if address, ok := core.ParseMailAddress(sender.Address); ok && address.Kind == core.MailPartyAgent && address.Local() {
		out = append(out, s.remoteParties(ctx, address.ID)...)
	}
	return out, nil
}

// ReachableFromDevice lists who an agent on a linked machine may write to,
// written the way that machine sees addresses. Agents on the machine itself
// are left out: it knows them better.
func (s *Service) ReachableFromDevice(ctx context.Context, deviceID, agentID string) ([]core.MailParty, error) {
	from := core.MailAddress{Kind: core.MailPartyAgent, ID: agentID, Machine: deviceID}
	sender := s.deviceSender(ctx, deviceID, from, "")
	parties, err := s.parties(ctx)
	if err != nil {
		return nil, err
	}
	out := []core.MailParty{}
	for _, party := range parties {
		address, ok := core.ParseMailAddress(party.Address)
		if !ok || address.Machine == deviceID {
			continue
		}
		if allowed, err := s.allowed(ctx, sender, party); err != nil {
			return nil, err
		} else if !allowed {
			continue
		}
		party.Address = addressFromDevice(party.Address, deviceID)
		if party.Machine == "" {
			party.Machine = "the hub"
		}
		out = append(out, party)
	}
	return out, nil
}

// remoteParties asks the hub who one of this machine's agents may write to,
// and remembers the answer for a minute.
func (s *Service) remoteParties(ctx context.Context, agentID string) []core.MailParty {
	_, _, hub, _ := s.collaborators()
	if hub == nil || !hub.Joined() {
		return nil
	}
	s.mu.Lock()
	cached, ok := s.remote[agentID]
	s.mu.Unlock()
	if ok && s.now().Sub(cached.at) < directoryTTL {
		return cached.parties
	}
	lookupCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	parties, err := hub.Reachable(lookupCtx, agentID)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Debug("mailbox: hub directory unavailable", "agent", agentID, "err", err)
		}
		return cached.parties
	}
	s.mu.Lock()
	s.remote[agentID] = remoteDirectory{at: s.now(), parties: parties}
	s.mu.Unlock()
	return parties
}

func (s *Service) remoteParty(ctx context.Context, sender core.MailParty, address string) (core.MailParty, bool) {
	from, ok := core.ParseMailAddress(sender.Address)
	if !ok {
		return core.MailParty{}, false
	}
	for _, party := range s.remoteParties(ctx, from.ID) {
		if party.Address == address {
			return party, true
		}
	}
	return core.MailParty{}, false
}

func (s *Service) parties(ctx context.Context) ([]core.MailParty, error) {
	if s.directory == nil {
		return nil, nil
	}
	return s.directory.Parties(ctx)
}

// party finds an address in this machine's directory.
func (s *Service) party(ctx context.Context, address string) (core.MailParty, bool, error) {
	parties, err := s.parties(ctx)
	if err != nil {
		return core.MailParty{}, false, err
	}
	for _, party := range parties {
		if party.Address == address {
			return party, true, nil
		}
	}
	return core.MailParty{}, false, nil
}

func (s *Service) localParty(ctx context.Context, address string) (core.MailParty, error) {
	parsed, ok := core.ParseMailAddress(address)
	if !ok || !parsed.Local() {
		return core.MailParty{}, Refuse("%q is not a party on this machine", address)
	}
	party, known, err := s.party(ctx, parsed.String())
	if err != nil {
		return core.MailParty{}, err
	}
	if !known {
		return core.MailParty{}, Refuse("there is no %s on this machine", describeAddress(parsed))
	}
	return party, nil
}

// Addresses differ by where they are read. A hub names its linked machines
// by device id; a linked machine names the hub "hub" and itself nothing.

// addressFromHub rewrites an address a linked machine wrote into the hub's
// form.
func addressFromHub(address, deviceID string) string {
	parsed, ok := core.ParseMailAddress(address)
	if !ok {
		return address
	}
	switch parsed.Machine {
	case "":
		parsed.Machine = deviceID
	case core.MailHub:
		parsed.Machine = ""
	}
	return parsed.String()
}

// addressFromDevice rewrites a hub address into a linked machine's form.
func addressFromDevice(address, deviceID string) string {
	parsed, ok := core.ParseMailAddress(address)
	if !ok {
		return address
	}
	switch parsed.Machine {
	case deviceID:
		parsed.Machine = ""
	case "":
		parsed.Machine = core.MailHub
	}
	return parsed.String()
}

func viewFromHub(message core.MailMessage, deviceID string) core.MailMessage {
	message.From, message.To = addressFromHub(message.From, deviceID), addressFromHub(message.To, deviceID)
	return message
}

func viewFromDevice(message core.MailMessage, deviceID string) core.MailMessage {
	message.From, message.To = addressFromDevice(message.From, deviceID), addressFromDevice(message.To, deviceID)
	return message
}

func describeAddress(address core.MailAddress) string {
	what := "agent"
	if address.Kind == core.MailPartyPerson {
		what = "person"
	}
	return fmt.Sprintf("%s %q", what, address.ID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sortParties(parties []core.MailParty) {
	slices.SortStableFunc(parties, func(a, b core.MailParty) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		if order := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); order != 0 {
			return order
		}
		return strings.Compare(a.Address, b.Address)
	})
}
