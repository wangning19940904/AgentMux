package superagent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func (h *harness) setup(answer string, draft core.SuperAgent) (SetupResult, error) {
	h.router.answer = func(core.InvocationRequest) (string, error) { return answer, nil }
	return h.service.Setup(context.Background(), SetupRequest{Instruction: "set this up", Draft: draft})
}

func TestSetupProposesATriggerAndStoresNothing(t *testing.T) {
	h := newHarness(t)
	before, _ := h.store.ListSuperAgents(context.Background())
	result, err := h.setup(`{"reply":"Added.","name":"Triage","mode":"act","router_prompt":"accept bugs","sources":[
		{"kind":"feishu_message","allowed_actors":["*"],"config":{"channel_id":"c1"}}]}`,
		core.SuperAgent{RouterAgentID: "router", AutoWorkers: true, Mode: core.SuperAgentModeObserve})
	if err != nil {
		t.Fatal(err)
	}
	draft := result.Draft
	if result.Reply != "Added." || draft.Name != "Triage" || draft.Mode != core.SuperAgentModeAct || draft.RouterPrompt != "accept bugs" {
		t.Fatalf("result = %+v", result)
	}
	if len(draft.Sources) != 1 || draft.Sources[0].ID == "" || !draft.Sources[0].Enabled || draft.Sources[0].Config["channel_id"] != "c1" {
		t.Fatalf("sources = %+v", draft.Sources)
	}
	if len(result.MissingSecrets) != 0 || len(result.Problems) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.HasPrefix(result.ConversationID, setupConversationPrefix) {
		t.Fatalf("conversation = %q", result.ConversationID)
	}
	after, _ := h.store.ListSuperAgents(context.Background())
	if len(after) != len(before) {
		t.Fatal("a proposal must not be stored")
	}
	if !strings.Contains(h.router.prompts[0], "Team bot") || strings.Contains(h.router.prompts[0], "Slack bot") {
		t.Fatalf("the prompt must offer Feishu channels only:\n%s", h.router.prompts[0])
	}
}

func TestSetupKeepsSecretsAwayFromTheModel(t *testing.T) {
	h := newHarness(t)
	draft := core.SuperAgent{RouterAgentID: "router", Sources: []core.SuperAgentSource{{
		ID: "mail", Kind: core.SuperAgentSourceEmail, Enabled: true, AllowedActors: []string{"a@example.com"},
		Config: map[string]string{"imap_host": "imap.example.com", "username": "u", "password": "typed-into-the-form"},
	}}}
	result, err := h.setup(`{"reply":"ok","sources":[
		{"id":"mail","kind":"email","allowed_actors":["b@example.com"],"config":{"mailbox":"Work","password":"from-the-model","trust_from_header":"true"}},
		{"kind":"github_webhook","allowed_actors":["alice"],"config":{"agent_login":"bot","webhook_secret":"from-the-model","github_token":"from-the-model"}}]}`, draft)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.router.prompts[0], "typed-into-the-form") {
		t.Fatalf("a secret reached the model:\n%s", h.router.prompts[0])
	}
	mail, github := result.Draft.Sources[0], result.Draft.Sources[1]
	if mail.Config["password"] != "typed-into-the-form" || mail.Config["mailbox"] != "Work" || mail.Config["imap_host"] != "imap.example.com" {
		t.Fatalf("mail = %+v", mail)
	}
	if !slices.Equal(mail.AllowedActors, []string{"b@example.com"}) {
		t.Fatalf("mail = %+v", mail)
	}
	if _, set := mail.Config["trust_from_header"]; set {
		t.Fatal("the assistant must not weaken sender checks")
	}
	if github.Config["webhook_secret"] != "" || github.Config["github_token"] != "" {
		t.Fatalf("github = %+v", github)
	}
	want := []SetupSecret{{SourceID: github.ID, Key: "webhook_secret"}}
	if !slices.Equal(result.MissingSecrets, want) {
		t.Fatalf("missing = %+v", result.MissingSecrets)
	}
	if len(result.Problems) != 0 {
		t.Fatalf("problems = %v", result.Problems)
	}
}

func TestSetupCannotChangeTheRouterOrWorkerLimits(t *testing.T) {
	h := newHarness(t)
	draft := core.SuperAgent{RouterAgentID: "router", AutoWorkers: true, AutoWorkerApprovalMode: core.ApprovalModeAutoEdit, Workers: []core.SuperAgentWorker{{ID: "w", AgentID: "coder"}}}
	result, err := h.setup(`{"reply":"ok","router_agent_id":"yolo","auto_workers":false,"auto_worker_approval_mode":"yolo","workers":[]}`, draft)
	if err != nil {
		t.Fatal(err)
	}
	got := result.Draft
	if got.RouterAgentID != "router" || !got.AutoWorkers || got.AutoWorkerApprovalMode != core.ApprovalModeAutoEdit || len(got.Workers) != 1 {
		t.Fatalf("draft = %+v", got)
	}
}

func TestSetupPlainAnswerIsAQuestion(t *testing.T) {
	h := newHarness(t)
	draft := core.SuperAgent{Name: "kept", RouterAgentID: "router"}
	result, err := h.setup("Which repository should it watch?", draft)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reply != "Which repository should it watch?" || result.Draft.Name != "kept" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSetupReportsWhatASaveWouldRefuse(t *testing.T) {
	h := newHarness(t)
	result, err := h.setup(`{"reply":"ok","sources":[{"kind":"github_webhook","allowed_actors":["*"],"config":{"agent_login":"bot"}}]}`, core.SuperAgent{RouterAgentID: "router"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Problems) != 1 || !strings.Contains(result.Problems[0], "only allowed for Feishu") {
		t.Fatalf("problems = %v", result.Problems)
	}
}

func TestSetupRunsOnAFrameworkWithoutASavedAgent(t *testing.T) {
	h := newHarness(t)
	var used string
	h.router.answer = func(request core.InvocationRequest) (string, error) {
		used = request.AgentID
		return `{"reply":"ok"}`, nil
	}
	result, err := h.service.Setup(context.Background(), SetupRequest{Instruction: "x", Draft: core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true}})
	if err != nil {
		t.Fatal(err)
	}
	if used != managedRouterID("codex") || result.Draft.RouterAgentID != used || result.Draft.RouterRuntime != "codex" {
		t.Fatalf("used %q, draft = %+v", used, result.Draft)
	}
	stored, _ := h.store.ListSuperAgents(context.Background())
	for _, agent := range stored {
		if agent.RouterRuntime == "codex" {
			t.Fatal("a draft must not be stored")
		}
	}
}

func TestSetupFlagsAMissingWorkDirectory(t *testing.T) {
	h := newHarness(t)
	result, err := h.setup(`{"reply":"ok","auto_worker_dir":"/no/such/directory"}`, core.SuperAgent{RouterAgentID: "router", AutoWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Problems) != 1 || !strings.Contains(result.Problems[0], "does not exist") {
		t.Fatalf("problems = %v", result.Problems)
	}
}

func TestSetupRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.router.answer = func(core.InvocationRequest) (string, error) { return `{"reply":"ok"}`, nil }
	if _, err := h.service.Setup(ctx, SetupRequest{Instruction: " ", Draft: core.SuperAgent{RouterAgentID: "router"}}); err == nil {
		t.Error("an empty instruction must be refused")
	}
	if _, err := h.service.Setup(ctx, SetupRequest{Instruction: "x", Draft: core.SuperAgent{RouterAgentID: "yolo"}}); err == nil {
		t.Error("a yolo router must be refused")
	}
	for name, answer := range map[string]string{
		"mode": `{"reply":"ok","mode":"chaos"}`,
		"kind": `{"reply":"ok","sources":[{"kind":"smoke-signal"}]}`,
	} {
		if _, err := h.setup(answer, core.SuperAgent{RouterAgentID: "router"}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSetupConversationContinuesOnlyItsOwnSession(t *testing.T) {
	h := newHarness(t)
	var seen []string
	h.router.answer = func(request core.InvocationRequest) (string, error) {
		seen = append(seen, request.ConversationID)
		return `{"reply":"ok"}`, nil
	}
	ctx := context.Background()
	draft := core.SuperAgent{RouterAgentID: "router"}
	first, err := h.service.Setup(ctx, SetupRequest{Instruction: "x", Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Setup(ctx, SetupRequest{Instruction: "y", Draft: draft, ConversationID: first.ConversationID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Setup(ctx, SetupRequest{Instruction: "z", Draft: draft, ConversationID: "superagent-tkt_someone-elses"}); err != nil {
		t.Fatal(err)
	}
	if seen[0] != seen[1] || seen[2] == "superagent-tkt_someone-elses" || !strings.HasPrefix(seen[2], setupConversationPrefix) {
		t.Fatalf("conversations = %v", seen)
	}
}

func TestSetupResultSavesThroughTheNormalPath(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	result, err := h.setup(`{"reply":"ok","sources":[{"kind":"feishu_message","allowed_actors":["*"],"config":{"channel_id":"c1"}}]}`, core.SuperAgent{RouterAgentID: "router", AutoWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Draft)
	var decoded core.SuperAgent
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	saved, err := h.service.Save(context.Background(), decoded)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "Super Agent (dsh)" || len(saved.Sources) != 1 || saved.Sources[0].Kind != core.SuperAgentSourceFeishuMessage {
		t.Fatalf("saved = %+v", saved)
	}
}
