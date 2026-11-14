package superagent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// What an agent's router, chat brain and workers are told is built from named
// parts, after the prompts of DeepSeek's dsh-superagent. Every part has a
// built-in default. An operator may replace the editable ones for an agent;
// a part they leave alone follows the default, including later changes to it.
// The parts that describe what the code does, or guard against what an event
// asks for, are fixed: the home's layout, the untrusted-text rules and the
// action format.

// The roles a prompt part belongs to.
const (
	promptRoleRouter = "router"
	promptRoleChat   = "chat"
	promptRoleWorker = "worker"
)

// maxPromptRunes bounds one part an operator writes. A longer one is refused
// instead of cut short.
const maxPromptRunes = 20000

// PromptPart is one built-in piece of a prompt.
type PromptPart struct {
	ID string `json:"id"`
	// Role is who is told it: the router, the chat brain or a worker.
	Role string `json:"role"`
	// Editable says whether an agent may carry its own text for the part.
	Editable bool `json:"editable"`
	// Placeholders are filled in when the prompt is built. An agent's own text
	// must keep the ones listed here.
	Placeholders []string `json:"placeholders,omitempty"`
	// Text is the built-in default.
	Text string `json:"text"`
	// Version names the default's text. An agent's own text records the
	// version it was written against, so a later change to the default shows.
	Version string `json:"version"`
}

// promptParts lists every part, in the order each role is told them.
var promptParts = withPromptVersions([]PromptPart{
	{ID: "router.role", Role: promptRoleRouter, Editable: true, Placeholders: []string{"{name}"}, Text: defaultRouterRole},
	{ID: "router.directory", Role: promptRoleRouter, Text: defaultRouterDirectory},
	{ID: "router.coordination", Role: promptRoleRouter, Editable: true, Text: defaultRouterCoordination},
	{ID: "router.colleagues", Role: promptRoleRouter, Editable: true, Text: defaultRouterColleagues},
	{ID: "router.memory", Role: promptRoleRouter, Editable: true, Text: defaultRouterMemory},
	{ID: "router.untrusted", Role: promptRoleRouter, Text: defaultRouterUntrusted},
	{ID: "router.replies", Role: promptRoleRouter, Editable: true, Text: defaultRouterReplies},
	{ID: "router.actions", Role: promptRoleRouter, Text: defaultRouterActions},
	{ID: "chat.role", Role: promptRoleChat, Editable: true, Placeholders: []string{"{name}"}, Text: defaultChatRole},
	{ID: "chat.home", Role: promptRoleChat, Text: defaultChatHome},
	{ID: "chat.actions", Role: promptRoleChat, Text: defaultChatActions},
	{ID: "worker.brief", Role: promptRoleWorker, Editable: true, Text: defaultWorkerBrief},
})

func withPromptVersions(parts []PromptPart) []PromptPart {
	for index := range parts {
		sum := sha256.Sum256([]byte(parts[index].Text))
		parts[index].Version = hex.EncodeToString(sum[:6])
	}
	return parts
}

// PromptParts returns the built-in prompt parts with their default text.
func PromptParts() []PromptPart {
	parts := slices.Clone(promptParts)
	for index := range parts {
		parts[index].Placeholders = slices.Clone(parts[index].Placeholders)
	}
	return parts
}

func promptPart(id string) (PromptPart, bool) {
	for _, part := range promptParts {
		if part.ID == id {
			return part, true
		}
	}
	return PromptPart{}, false
}

// promptFor builds what one role of an agent is told: its parts in order, the
// operator's text where they wrote one and the default elsewhere. values are
// placeholder and value pairs. They are filled in once, so a value that looks
// like a placeholder stays as it is.
func promptFor(agent core.SuperAgent, role string, values ...string) string {
	var texts []string
	for _, part := range promptParts {
		if part.Role != role {
			continue
		}
		text := part.Text
		if own := strings.TrimSpace(agent.Prompts[part.ID].Text); part.Editable && own != "" {
			text = own
		}
		texts = append(texts, text)
	}
	return strings.NewReplacer(values...).Replace(strings.Join(texts, "\n\n")) + "\n"
}

// normalizePrompts checks the prompt parts an operator wrote for an agent. An
// empty text, or one equal to the default, is dropped, so the part follows the
// default again. Each text kept records the version of the default it was
// written against: the one the operator confirmed, or the one in force when
// the text last changed.
func normalizePrompts(agent *core.SuperAgent, existing *core.SuperAgent) error {
	var saved map[string]core.SuperAgentPrompt
	if existing != nil {
		saved = existing.Prompts
	}
	kept := map[string]core.SuperAgentPrompt{}
	for id, own := range agent.Prompts {
		part, ok := promptPart(id)
		if !ok {
			// A part an older or newer build knew: its text has nowhere to go.
			if _, carried := saved[id]; carried {
				continue
			}
			return fmt.Errorf("unknown prompt part %q", id)
		}
		text := strings.TrimSpace(strings.ReplaceAll(own.Text, "\r\n", "\n"))
		if text == "" || text == part.Text {
			continue
		}
		if !part.Editable {
			return fmt.Errorf("prompt part %q is fixed and cannot be changed", id)
		}
		if count := len([]rune(text)); count > maxPromptRunes {
			return fmt.Errorf("prompt part %q has %d characters; the limit is %d", id, count, maxPromptRunes)
		}
		for _, placeholder := range part.Placeholders {
			if !strings.Contains(text, placeholder) {
				return fmt.Errorf("prompt part %q must keep %s", id, placeholder)
			}
		}
		base := part.Version
		if before, had := saved[id]; had && before.Text == text && before.Base != "" && own.Base != part.Version {
			base = before.Base
		}
		kept[id] = core.SuperAgentPrompt{Text: text, Base: base}
	}
	if len(kept) == 0 {
		kept = nil
	}
	agent.Prompts = kept
	return nil
}
