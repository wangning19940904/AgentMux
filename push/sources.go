package push

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/superagent"
)

// Where the events come from. Each source is a narrow adapter around
// something that already exists, so the code that owns the event is not
// changed to send notifications.

// InteractionLookup finds the names a waiting notification shows.
type InteractionLookup interface {
	GetChannelInteraction(ctx context.Context, id string) (*core.ChannelInteraction, error)
	GetChannel(ctx context.Context, id string) (*core.Channel, error)
	GetAgentInstance(ctx context.Context, id string) (*core.AgentInstance, error)
}

// EngineSink is a core.EventSink for engine.SubscribeEventSink: an agent on
// this machine asked a person to approve something or answer a question.
// Requests Guard decides on its own carry a guard_decision and need nobody.
func (s *Service) EngineSink(lookup InteractionLookup) core.EventSink {
	return func(event core.HookEvent, data map[string]string) {
		if s == nil || event != core.HookPermission || data["guard_decision"] != "" {
			return
		}
		interactionID := strings.TrimSpace(data["interaction_id"])
		if interactionID == "" {
			return
		}
		channelID := data["channel_id"]
		// Sinks run on the engine's goroutine; names are looked up apart.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			w := Waiting{}
			if lookup != nil {
				if record, err := lookup.GetChannelInteraction(ctx, interactionID); err == nil && record != nil {
					if record.Status != "" && record.Status != core.ChannelInteractionPending {
						return // already answered
					}
					w.Question = record.Request.Kind == core.AgentInteractionUserInput
					if channelID == "" {
						channelID = record.ChannelID
					}
				}
				w.Agent = channelAgentName(ctx, lookup, channelID)
			}
			s.Publish(WaitingEvent("", interactionID, w, time.Now().UTC()))
		}()
	}
}

func channelAgentName(ctx context.Context, lookup InteractionLookup, channelID string) string {
	if channelID == "" {
		return ""
	}
	channel, err := lookup.GetChannel(ctx, channelID)
	if err != nil || channel == nil {
		return ""
	}
	if channel.AgentID != "" {
		if agent, err := lookup.GetAgentInstance(ctx, channel.AgentID); err == nil && agent != nil && agent.Name != "" {
			return agent.Name
		}
	}
	return channel.Name
}

// PendingInteractionsCollection is the report a node sends the hub of the
// interactions waiting on it (link.DefaultReports).
const PendingInteractionsCollection = "interactions_pending"

// ReportSink wraps the hub's report store: when a node reports a new pending
// interaction, the hub notifies from its copy. Names come from what the node
// reported of its channels and agents.
func (s *Service) ReportSink(next link.ReportSink, names DeviceNames) link.ReportSink {
	return &reportSink{ReportSink: next, service: s, names: names}
}

// DeviceNames finds names in the hub's copy of a node's data.
type DeviceNames interface {
	DeviceName(ctx context.Context, deviceID string) string
	DeviceAgentName(ctx context.Context, deviceID, channelID string) string
}

type reportSink struct {
	link.ReportSink
	service *Service
	names   DeviceNames
}

func (r *reportSink) ApplyDeviceReport(ctx context.Context, deviceID string, report core.DeviceReport, at time.Time) error {
	if err := r.ReportSink.ApplyDeviceReport(ctx, deviceID, report, at); err != nil {
		return err
	}
	if r.service == nil || report.Collection != PendingInteractionsCollection {
		return nil
	}
	for _, doc := range report.Upserts {
		var interaction core.ChannelInteraction
		if json.Unmarshal(doc.Data, &interaction) != nil || interaction.ID == "" {
			continue
		}
		if interaction.Status != "" && interaction.Status != core.ChannelInteractionPending {
			continue
		}
		w := Waiting{Question: interaction.Request.Kind == core.AgentInteractionUserInput}
		if r.names != nil {
			w.Machine = r.names.DeviceName(ctx, deviceID)
			w.Agent = r.names.DeviceAgentName(ctx, deviceID, interaction.ChannelID)
		}
		if w.Machine == "" {
			w.Machine = deviceID
		}
		r.service.Publish(WaitingEvent(deviceID, interaction.ID, w, at.UTC()))
	}
	return nil
}

// TicketStore wraps the Super Agent store: a Ticket saved as ready (its
// worker finished) or failed, coming from another state, is notified.
func (s *Service) TicketStore(next superagent.Store) superagent.Store {
	if s == nil {
		return next
	}
	return &ticketStore{Store: next, service: s}
}

type ticketStore struct {
	superagent.Store
	service *Service
}

func (t *ticketStore) UpsertSuperAgentTicket(ctx context.Context, ticket core.SuperAgentTicket) error {
	previous := ""
	final := ticket.Status == core.SuperAgentTicketReady || ticket.Status == core.SuperAgentTicketFailed
	if final {
		if old, err := t.Store.GetSuperAgentTicket(ctx, ticket.ID); err == nil && old != nil {
			previous = old.Status
		}
	}
	if err := t.Store.UpsertSuperAgentTicket(ctx, ticket); err != nil {
		return err
	}
	if !final || previous == ticket.Status {
		return nil
	}
	name := ""
	if agent, err := t.Store.GetSuperAgent(ctx, ticket.SuperAgentID); err == nil && agent != nil {
		name = agent.Name
	}
	t.service.Publish(TicketEvent(ticket, name, time.Now().UTC()))
	return nil
}
