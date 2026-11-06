package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

const setupConversationPrefix = "superagent-setup-"

// setupConfigKeys are the Source settings the assistant may write. Secrets and
// settings that weaken a check, such as trust_from_header, stay with the form.
var setupConfigKeys = map[string][]string{
	core.SuperAgentSourceFeishuMessage: {"channel_id", "chat_ids"},
	core.SuperAgentSourceFeishuDoc:     {"channel_id", "documents", "notify_chat_id", "instructions", "debounce_seconds"},
	core.SuperAgentSourceEmail:         {"imap_host", "imap_port", "username", "mailbox", "smtp_host", "smtp_port", "from_address"},
	core.SuperAgentSourceGitHub:        {"agent_login", "repo", "api_base", "delivery"},
}

// SetupRequest is one instruction from a Console administrator. Draft is the
// unsaved form, and may carry secrets the administrator typed into it.
type SetupRequest struct {
	ConversationID string          `json:"conversation_id,omitempty"`
	Instruction    string          `json:"instruction"`
	Draft          core.SuperAgent `json:"draft"`
}

// SetupSecret names a secret the form must collect, since none reach the model.
type SetupSecret struct {
	SourceID string `json:"source_id"`
	Key      string `json:"key"`
}

// SetupResult is a proposal. Nothing is stored until the administrator saves
// its Draft, which passes through the same validation as any other save.
type SetupResult struct {
	ConversationID string          `json:"conversation_id"`
	Reply          string          `json:"reply"`
	Draft          core.SuperAgent `json:"draft"`
	MissingSecrets []SetupSecret   `json:"missing_secrets"`
	Problems       []string        `json:"problems"`
}

type setupAnswer struct {
	Reply          string         `json:"reply"`
	Name           *string        `json:"name"`
	RouterPrompt   *string        `json:"router_prompt"`
	Mode           *string        `json:"mode"`
	MaxConcurrency *int           `json:"max_concurrency"`
	AutoWorkerDir  *string        `json:"auto_worker_dir"`
	Sources        *[]setupSource `json:"sources"`
}

type setupSource struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	Enabled       *bool             `json:"enabled"`
	AllowedActors []string          `json:"allowed_actors"`
	Config        map[string]string `json:"config"`
}

// Setup turns an administrator's instruction into a proposed configuration. It
// runs only for the Console, never for Trigger text: an event cannot reach it.
func (s *Service) Setup(ctx context.Context, request SetupRequest) (SetupResult, error) {
	instruction := strings.TrimSpace(request.Instruction)
	if instruction == "" {
		return SetupResult{}, errors.New("instruction is required")
	}
	draft := request.Draft
	draft.RouterAgentID = strings.TrimSpace(draft.RouterAgentID)
	current := ""
	if id := strings.TrimSpace(draft.ID); id != "" {
		saved, err := s.store.GetSuperAgent(ctx, id)
		if err != nil {
			return SetupResult{}, err
		}
		if saved != nil {
			current = saved.RouterAgentID
		}
	}
	if _, err := s.resolveRouter(ctx, &draft, current, false); err != nil {
		return SetupResult{}, err
	}
	conversationID := strings.TrimSpace(request.ConversationID)
	if !strings.HasPrefix(conversationID, setupConversationPrefix) {
		conversationID = setupConversationPrefix + uuid.NewString()
	}
	prompt, err := s.setupPrompt(ctx, draft, truncateRunes(instruction, maxTriggerRunes))
	if err != nil {
		return SetupResult{}, err
	}
	setupCtx, cancel := context.WithTimeout(ctx, routerTimeout)
	defer cancel()
	response, err := s.invoker.Invoke(setupCtx, core.InvocationRequest{AgentID: draft.RouterAgentID, ConversationID: conversationID, Input: prompt})
	if err != nil {
		return SetupResult{}, err
	}
	answer, parsed := parseSetupAnswer(response.Answer)
	if !parsed {
		// A plain answer is usually a question back. The draft stays as it was.
		answer = setupAnswer{Reply: strings.TrimSpace(response.Answer)}
	}
	if err := applySetupAnswer(&draft, answer); err != nil {
		return SetupResult{}, err
	}
	return SetupResult{
		ConversationID: conversationID, Reply: answer.Reply, Draft: draft,
		MissingSecrets: missingSecrets(draft), Problems: setupProblems(draft),
	}, nil
}

func parseSetupAnswer(answer string) (setupAnswer, bool) {
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end <= start {
		return setupAnswer{}, false
	}
	var parsed setupAnswer
	if err := json.Unmarshal([]byte(answer[start:end+1]), &parsed); err != nil {
		return setupAnswer{}, false
	}
	return parsed, true
}

func applySetupAnswer(draft *core.SuperAgent, answer setupAnswer) error {
	if answer.Name != nil {
		draft.Name = strings.TrimSpace(*answer.Name)
	}
	if answer.RouterPrompt != nil {
		draft.RouterPrompt = strings.TrimSpace(*answer.RouterPrompt)
	}
	if answer.Mode != nil {
		if *answer.Mode != core.SuperAgentModeAct && *answer.Mode != core.SuperAgentModeObserve {
			return fmt.Errorf("assistant proposed unknown mode %q", *answer.Mode)
		}
		draft.Mode = *answer.Mode
	}
	if answer.MaxConcurrency != nil {
		draft.MaxConcurrency = *answer.MaxConcurrency
	}
	if answer.AutoWorkerDir != nil {
		draft.AutoWorkerDir = strings.TrimSpace(*answer.AutoWorkerDir)
	}
	if answer.Sources == nil {
		return nil
	}
	sources := make([]core.SuperAgentSource, 0, len(*answer.Sources))
	for _, proposed := range *answer.Sources {
		allowedKeys, known := setupConfigKeys[proposed.Kind]
		if !known {
			return fmt.Errorf("assistant proposed unknown trigger kind %q", proposed.Kind)
		}
		// Merging keeps secrets and settings the assistant may not write.
		source := core.SuperAgentSource{Kind: proposed.Kind, Enabled: true, Config: map[string]string{}}
		if index := slices.IndexFunc(draft.Sources, func(item core.SuperAgentSource) bool {
			return item.ID == proposed.ID && item.Kind == proposed.Kind
		}); proposed.ID != "" && index >= 0 {
			source = draft.Sources[index]
			config := make(map[string]string, len(source.Config))
			for key, value := range source.Config {
				config[key] = value
			}
			source.Config = config
		} else {
			source.ID = "src_" + uuid.NewString()
		}
		if proposed.Enabled != nil {
			source.Enabled = *proposed.Enabled
		}
		if proposed.AllowedActors != nil {
			source.AllowedActors = normalizeActors(proposed.AllowedActors)
		}
		for _, key := range allowedKeys {
			if value, ok := proposed.Config[key]; ok {
				source.Config[key] = strings.TrimSpace(value)
			}
		}
		sources = append(sources, source)
	}
	draft.Sources = sources
	return nil
}

func missingSecrets(draft core.SuperAgent) []SetupSecret {
	missing := []SetupSecret{}
	for _, source := range draft.Sources {
		for _, key := range requiredSecrets(source) {
			if strings.TrimSpace(source.Config[key]) == "" {
				missing = append(missing, SetupSecret{SourceID: source.ID, Key: key})
			}
		}
	}
	return missing
}

// setupProblems reports what a save would refuse, apart from the secrets the
// form is about to collect.
func setupProblems(draft core.SuperAgent) []string {
	problems := []string{}
	for _, source := range draft.Sources {
		check := source
		check.Config = make(map[string]string, len(source.Config))
		for key, value := range source.Config {
			check.Config[key] = value
		}
		for _, key := range requiredSecrets(source) {
			if strings.TrimSpace(check.Config[key]) == "" {
				check.Config[key] = redactedValue
			}
		}
		if err := validateSource(check); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", source.Kind, err))
		}
	}
	if err := validateAutoWorkerDir(draft.AutoWorkerDir); err != nil {
		problems = append(problems, err.Error())
	}
	return problems
}

func (s *Service) setupPrompt(ctx context.Context, draft core.SuperAgent, instruction string) (string, error) {
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return "", err
	}
	type channelView struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	feishu := []channelView{}
	for _, channel := range channels {
		if channel.Type == "feishu" || channel.Type == "lark" {
			feishu = append(feishu, channelView{ID: channel.ID, Name: channel.Name})
		}
	}
	redacted := redactAgent(draft)
	current, err := json.MarshalIndent(map[string]any{
		"name": redacted.Name, "router_prompt": redacted.RouterPrompt, "mode": redacted.Mode,
		"max_concurrency": redacted.MaxConcurrency, "auto_worker_dir": redacted.AutoWorkerDir, "sources": redacted.Sources,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	channelsJSON, err := json.MarshalIndent(feishu, "", "  ")
	if err != nil {
		return "", err
	}
	return `You help an administrator configure a Super Agent, which routes events from triggers to workers. Translate the instruction into configuration changes. Do not call tools, edit files or run commands: AgentMux shows your proposal to the administrator, who decides whether to save it.

Answer with one JSON object and nothing else. Include only the fields you change, and always include "reply":
{"reply":"","name":"","router_prompt":"","mode":"observe|act","max_concurrency":2,"auto_worker_dir":"","sources":[{"id":"","kind":"","enabled":true,"allowed_actors":[],"config":{}}]}

- "reply": one or two sentences in the administrator's language, saying what you changed or asking the one thing you need.
- "sources": the complete list of triggers after the change. Keep the "id" of a trigger you keep, and leave it empty for a new one. A trigger left out is removed.
- "router_prompt": standing guidance for routing decisions, such as which kinds of request to accept.
- "mode": "observe" only records decisions. Set "act" only when the administrator asks for it to run for real.

Trigger kinds and their config keys:
- feishu_message: channel_id (required, from FEISHU CHANNELS), chat_ids (comma separated; empty means every chat). allowed_actors are open_ids, or "*" for everyone who can reach the bot.
- feishu_doc: channel_id (required), documents (required, URLs or tokens), notify_chat_id, instructions (what to do on a change), debounce_seconds. allowed_actors are open_ids; "*" is refused.
- email: imap_host (required), imap_port, username (required), mailbox, smtp_host, smtp_port, from_address. allowed_actors are email addresses; "*" is refused.
- github_webhook: agent_login (required, the agent's GitHub account), repo (owner/name), api_base, delivery ("poll" reads the account's notifications and needs no public address; "webhook" or empty needs GitHub to reach this machine). allowed_actors are GitHub logins; "*" is refused.

Rules:
- An enabled trigger needs allowed_actors. You cannot look up a person's open_id from a name: ask for it, or ask whether everyone may use the trigger. Never invent ids, logins, addresses or channels.
- Never ask for or write passwords, tokens or webhook secrets. AgentMux collects them in a secure field after your reply.
- Workers are launched automatically; do not plan or list them. When the instruction names a directory to work in, set "auto_worker_dir" to it.

CURRENT CONFIGURATION:
` + string(current) + `

FEISHU CHANNELS:
` + string(channelsJSON) + `

INSTRUCTION:
` + instruction + "\n", nil
}
