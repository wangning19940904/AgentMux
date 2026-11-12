// Package push sends Web Push notifications about the things that need a
// person: an agent waiting for approval or an answer, a Super Agent Ticket
// that finished or failed, a linked machine that went offline or came back.
//
// A notification names what happened and where, never what an agent or a
// person wrote: no message bodies, commands, file paths or secrets. Tapping
// one opens the Console page that shows the rest.
package push

import (
	"fmt"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Event is one thing worth telling a person about.
type Event struct {
	// ID identifies the occurrence; the same ID is notified once.
	ID string
	// Kind is the core.PushKind* setting that covers it.
	Kind string
	// Tag groups notifications on the device: a newer one with the same tag
	// replaces the older one instead of piling up.
	Tag string
	// URL is the Console route a tap opens.
	URL string
	At  time.Time

	// What happened. Only names and states, never content.
	Waiting *Waiting
	Ticket  *TicketChange
	Machine *MachineChange
	Test    bool
}

// Waiting describes an agent that needs a person.
type Waiting struct {
	Question bool   // a question rather than an approval
	Agent    string // agent or channel name, when known
	Machine  string // a linked machine's name; empty for this machine
}

// TicketChange describes a Ticket that reached a final state.
type TicketChange struct {
	Failed     bool
	SuperAgent string
}

// MachineChange describes a linked machine coming or going.
type MachineChange struct {
	Name   string
	Online bool
}

// Message is the payload the service worker receives (web/sw.js).
type Message struct {
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Tag       string    `json:"tag,omitempty"`
	URL       string    `json:"url"`
	Timestamp time.Time `json:"timestamp"`
}

const (
	routeWaiting  = "/#sessions"
	routeTickets  = "/#super-agents"
	routeMachines = "/#machine-status"
	routeSettings = "/#settings"
)

// WaitingEvent is an agent waiting for a person. id names the interaction;
// machineID is empty for this machine.
func WaitingEvent(machineID, interactionID string, w Waiting, at time.Time) Event {
	scope := "local"
	if machineID != "" {
		scope = "device:" + machineID
	}
	return Event{
		ID: "waiting:" + scope + ":" + interactionID, Kind: core.PushKindWaiting,
		Tag: "waiting:" + scope + ":" + interactionID, URL: routeWaiting, At: at, Waiting: &w,
	}
}

// TicketEvent is a Ticket that finished (ready) or failed.
func TicketEvent(ticket core.SuperAgentTicket, superAgent string, at time.Time) Event {
	failed := ticket.Status == core.SuperAgentTicketFailed
	return Event{
		ID:   fmt.Sprintf("ticket:%s:%s:%d", ticket.ID, ticket.Status, ticket.FinishedAt.UnixNano()),
		Kind: core.PushKindTickets, Tag: "ticket:" + ticket.ID, URL: routeTickets, At: at,
		Ticket: &TicketChange{Failed: failed, SuperAgent: superAgent},
	}
}

// MachineEvent is a linked machine that went offline or came back.
func MachineEvent(deviceID, name string, online bool, at time.Time) Event {
	state := "offline"
	if online {
		state = "online"
	}
	return Event{
		ID:   fmt.Sprintf("machine:%s:%s:%d", deviceID, state, at.UnixNano()),
		Kind: core.PushKindMachines, Tag: "machine:" + deviceID, URL: routeMachines, At: at,
		Machine: &MachineChange{Name: name, Online: online},
	}
}

// TestEvent is the notification a person sends themselves from Settings.
func TestEvent(at time.Time) Event {
	return Event{ID: fmt.Sprintf("test:%d", at.UnixNano()), Tag: "test", URL: routeSettings, At: at, Test: true}
}

// Render writes the event in a Console language ("zh" or anything else for
// English).
func (e Event) Render(lang string) Message {
	zh := lang == "zh"
	m := Message{Kind: e.Kind, Tag: e.Tag, URL: e.URL, Timestamp: e.At}
	switch {
	case e.Test:
		m.Kind = "test"
		m.Title, m.Body = pick(zh, "AgentMux notifications work", "AgentMux 通知已可用"),
			pick(zh, "This device will be told when an agent needs you.", "有 Agent 需要你处理时，这台设备会收到通知。")
	case e.Waiting != nil:
		w := e.Waiting
		if w.Question {
			m.Title = pick(zh, "An agent has a question for you", "有 Agent 在等你回答问题")
		} else {
			m.Title = pick(zh, "An agent is waiting for your approval", "有 Agent 在等你批准")
		}
		m.Body = joinWhere(zh, w.Agent, w.Machine)
	case e.Ticket != nil:
		if e.Ticket.Failed {
			m.Title = pick(zh, "A Super Agent Ticket failed", "Super Agent 工单失败")
		} else {
			m.Title = pick(zh, "A Super Agent Ticket finished", "Super Agent 工单已完成")
		}
		m.Body = e.Ticket.SuperAgent
	case e.Machine != nil:
		name := fallback(e.Machine.Name, pick(zh, "A linked machine", "一台关联机器"))
		if e.Machine.Online {
			m.Title = pick(zh, name+" is back online", name+" 已重新上线")
		} else {
			m.Title = pick(zh, name+" went offline", name+" 已离线")
		}
		m.Body = pick(zh, "Linked machine", "关联机器")
	}
	return m
}

// summarize turns several events of one kind into one notification.
func summarize(kind string, events []Event, lang string, at time.Time) Message {
	zh := lang == "zh"
	n := len(events)
	m := Message{Kind: kind, Tag: "summary:" + kind, Timestamp: at}
	switch kind {
	case core.PushKindWaiting:
		m.URL = routeWaiting
		m.Title = pick(zh, fmt.Sprintf("%d requests are waiting for you", n), fmt.Sprintf("%d 个请求在等你处理", n))
		m.Body = names(zh, events, func(e Event) string { return joinWhere(zh, e.Waiting.Agent, e.Waiting.Machine) })
	case core.PushKindTickets:
		m.URL = routeTickets
		failed := 0
		for _, e := range events {
			if e.Ticket.Failed {
				failed++
			}
		}
		m.Title = pick(zh, fmt.Sprintf("%d Super Agent Tickets finished", n), fmt.Sprintf("%d 个 Super Agent 工单已结束", n))
		if failed > 0 {
			m.Title = pick(zh, fmt.Sprintf("%d Super Agent Tickets ended, %d failed", n, failed), fmt.Sprintf("%d 个 Super Agent 工单已结束，%d 个失败", n, failed))
		}
		m.Body = names(zh, events, func(e Event) string { return e.Ticket.SuperAgent })
	case core.PushKindMachines:
		m.URL = routeMachines
		offline := 0
		for _, e := range events {
			if !e.Machine.Online {
				offline++
			}
		}
		m.Title = pick(zh, fmt.Sprintf("%d linked machines changed: %d offline, %d back", n, offline, n-offline),
			fmt.Sprintf("%d 台关联机器状态变化：%d 台离线，%d 台恢复", n, offline, n-offline))
		m.Body = names(zh, events, func(e Event) string { return e.Machine.Name })
	}
	return m
}

func pick(zh bool, en, cn string) string {
	if zh {
		return cn
	}
	return en
}

func fallback(value, otherwise string) string {
	if strings.TrimSpace(value) == "" {
		return otherwise
	}
	return value
}

func joinWhere(zh bool, agent, machine string) string {
	agent, machine = strings.TrimSpace(agent), strings.TrimSpace(machine)
	switch {
	case agent != "" && machine != "":
		return agent + pick(zh, " on ", " · ") + machine
	case machine != "":
		return machine
	default:
		return agent
	}
}

// names lists up to three distinct names and how many more there are.
func names(zh bool, events []Event, name func(Event) string) string {
	seen := map[string]bool{}
	var list []string
	for _, e := range events {
		value := strings.TrimSpace(name(e))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		list = append(list, value)
	}
	if len(list) <= 3 {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:3], ", ") + pick(zh, fmt.Sprintf(" and %d more", len(list)-3), fmt.Sprintf(" 等 %d 个", len(list)))
}
