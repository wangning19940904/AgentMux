package superagent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	imapserver "github.com/emersion/go-imap/server"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/remote"
)

func githubBody(t *testing.T, action, sender, comment string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"action": action, "repository": map[string]any{"full_name": "acme/app"},
		"sender":  map[string]any{"login": sender, "type": "User"},
		"comment": map[string]any{"body": comment},
		"issue":   map[string]any{"number": 7, "title": "Broken build", "body": "details"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestGitHubWebhookVerifiesSignatureBeforeAnythingElse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	body := githubBody(t, "created", "alice", "@amux-bot fix it")
	if err := h.service.HandleGitHubWebhook(ctx, "gh", GitHubDelivery{Event: "issue_comment", DeliveryID: "d0", Signature: sign("wrong", body), Body: body}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("forged signature: %v", err)
	}
	if err := h.service.HandleGitHubWebhook(ctx, "gh", GitHubDelivery{Event: "issue_comment", DeliveryID: "d0", Body: body}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing signature: %v", err)
	}
	if err := h.service.HandleGitHubWebhook(ctx, "nope", GitHubDelivery{Body: body}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown source: %v", err)
	}
	if len(h.store.triggers) != 0 {
		t.Fatal("an unverified delivery must store nothing")
	}
	delivery := GitHubDelivery{Event: "issue_comment", DeliveryID: "d1", Signature: sign("s3cret", body), Body: body}
	for range 2 { // GitHub redelivers with the same id.
		if err := h.service.HandleGitHubWebhook(ctx, "gh", delivery); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.store.triggers) != 1 {
		t.Fatalf("triggers = %+v", h.store.triggers)
	}
	trigger := h.store.triggers[0]
	if trigger.ThreadKey != "github:acme/app#7" || !trigger.Addressed || trigger.Actor != "alice" {
		t.Fatalf("trigger = %+v", trigger)
	}
	var reply githubReply
	if err := json.Unmarshal(trigger.Reply, &reply); err != nil || reply.Repo != "acme/app" || reply.Number != 7 {
		t.Fatalf("reply = %s", trigger.Reply)
	}
}

func TestGitHubParserDropsLoopsAndNoise(t *testing.T) {
	source := core.SuperAgentSource{Config: map[string]string{"agent_login": "amux-bot", "repo": "acme/app"}}
	cases := map[string]struct {
		event string
		body  []byte
	}{
		"its own comment": {"issue_comment", githubBody(t, "created", "amux-bot", "@amux-bot done")},
		"edited comment":  {"issue_comment", githubBody(t, "edited", "alice", "@amux-bot")},
		"unrelated event": {"star", githubBody(t, "created", "alice", "")},
		"other repo":      {"issue_comment", bytes.ReplaceAll(githubBody(t, "created", "alice", "@amux-bot"), []byte("acme/app"), []byte("evil/app"))},
		"bot sender":      {"issue_comment", bytes.ReplaceAll(githubBody(t, "created", "ci", "@amux-bot"), []byte(`"User"`), []byte(`"Bot"`))},
	}
	for name, tc := range cases {
		if _, ok, err := parseGitHubEvent(source, GitHubDelivery{Event: tc.event, Body: tc.body}); ok || err != nil {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
		}
	}
	if _, _, err := parseGitHubEvent(source, GitHubDelivery{Event: "issue_comment", Body: []byte("not json")}); err == nil {
		t.Error("a corrupt payload must be an error")
	}
	assigned, _ := json.Marshal(map[string]any{
		"action": "assigned", "repository": map[string]any{"full_name": "acme/app"},
		"sender": map[string]any{"login": "alice"}, "assignee": map[string]any{"login": "AMUX-bot"},
		"issue": map[string]any{"number": 1, "title": "t", "body": "b"},
	})
	trigger, ok, err := parseGitHubEvent(source, GitHubDelivery{Event: "issues", Body: assigned})
	if err != nil || !ok || !trigger.Addressed {
		t.Fatalf("assigning the agent is a request: %+v ok=%v err=%v", trigger, ok, err)
	}
}

func TestMentionsLogin(t *testing.T) {
	for text, want := range map[string]bool{
		"@amux-bot please":       true,
		"hey @AMUX-BOT, please":  true,
		"(@amux-bot)":            true,
		"@amux-bot-two please":   false,
		"mail me@amux-bot":       false,
		"org/@amux-bot":          false,
		"amux-bot without at":    false,
		"see @amux-bot.":         true,
		"multi\nline @amux-bot!": true,
	} {
		if got := mentionsLogin(text, "amux-bot"); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
}

func feishuHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.service.channelHasAgent = func(context.Context, string) bool { return true }
	saved, err := h.service.Save(context.Background(), core.SuperAgent{
		ID: h.agent.ID, Name: "sa", RouterAgentID: "router", Enabled: true, Workers: h.agent.Workers,
		Sources: []core.SuperAgentSource{
			{ID: "msg", Kind: core.SuperAgentSourceFeishuMessage, Enabled: true, AllowedActors: []string{"ou_alice"}, Config: map[string]string{"channel_id": "ch1", "chat_ids": "oc_team"}},
			{ID: "doc", Kind: core.SuperAgentSourceFeishuDoc, Enabled: true, AllowedActors: []string{"ou_alice"}, Config: map[string]string{
				"channel_id": "ch1", "documents": "https://acme.feishu.cn/docx/DOCTOKEN", "notify_chat_id": "oc_team", "instructions": "Review the change.",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := h.store.GetSuperAgent(context.Background(), saved.ID)
	h.agent = *stored
	return h
}

func TestFeishuMessageClaimRules(t *testing.T) {
	h := feishuHarness(t)
	ctx := context.Background()
	message := func(id, chat, user, text string, mentioned bool) *core.Message {
		return &core.Message{ID: id, SourceMessageID: id, ChannelID: "ch1", ChatID: chat, UserID: user, Text: text, MentionedBot: mentioned, ChatType: "group"}
	}
	cases := []struct {
		name    string
		msg     *core.Message
		claimed bool
		stored  int
	}{
		{"mention from allowed sender", message("m1", "oc_team", "ou_alice", "fix it", true), true, 1},
		{"redelivery stays claimed and single", message("m1", "oc_team", "ou_alice", "fix it", true), true, 1},
		{"mention from a stranger is recorded, not routed", message("m2", "oc_team", "ou_eve", "fix it", true), true, 2},
		{"chat outside the list is the channel's", message("m3", "oc_other", "ou_alice", "fix it", true), false, 2},
		{"unaddressed chatter is the channel's", message("m4", "oc_team", "ou_alice", "lunch?", false), false, 2},
		{"commands are the channel's", message("m5", "oc_team", "ou_alice", "/help", true), false, 2},
		{"another channel is untouched", &core.Message{ID: "m6", ChannelID: "ch2", ChatID: "oc_team", UserID: "ou_alice", Text: "x", MentionedBot: true}, false, 2},
	}
	for _, tc := range cases {
		claimed, err := h.service.InterceptMessage(ctx, tc.msg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if claimed != tc.claimed || len(h.store.triggers) != tc.stored {
			t.Errorf("%s: claimed=%v stored=%d", tc.name, claimed, len(h.store.triggers))
		}
	}
	if h.store.triggers[1].Disposition != core.SuperAgentDispositionFiltered {
		t.Fatalf("stranger = %+v", h.store.triggers[1])
	}
	var reply feishuReply
	if err := json.Unmarshal(h.store.triggers[0].Reply, &reply); err != nil || reply.ChatID != "oc_team" || reply.MessageID != "m1" {
		t.Fatalf("reply = %s", h.store.triggers[0].Reply)
	}

	h.service.channelHasAgent = func(context.Context, string) bool { return false }
	if claimed, _ := h.service.InterceptMessage(ctx, message("m7", "oc_team", "ou_alice", "lunch?", false)); !claimed {
		t.Fatal("with no channel Agent to take it, unaddressed chatter must end here")
	}
}

func TestFeishuThreadRepliesShareATicketThread(t *testing.T) {
	first := feishuMessageTrigger(core.SuperAgentSource{}, &core.Message{ID: "m1", ChannelID: "c", ChatID: "g", MentionedBot: true})
	second := feishuMessageTrigger(core.SuperAgentSource{}, &core.Message{ID: "m2", RootID: "m1", ChannelID: "c", ChatID: "g"})
	if first.ThreadKey != second.ThreadKey {
		t.Fatalf("%q != %q", first.ThreadKey, second.ThreadKey)
	}
	direct := feishuMessageTrigger(core.SuperAgentSource{}, &core.Message{ID: "m3", ChannelID: "c", ChatID: "p", ChatType: "p2p"})
	if !direct.Addressed {
		t.Fatal("a direct message addresses the bot")
	}
}

func docEvent(token string, operators ...string) core.RelayEvent {
	list := []map[string]string{}
	for _, operator := range operators {
		list = append(list, map[string]string{"open_id": operator})
	}
	data, _ := json.Marshal(map[string]any{"event": map[string]any{"file_token": token, "file_type": "docx", "operator_id_list": list}})
	return core.RelayEvent{Type: feishuDocEditEvent, SourceRef: "channel:ch1", Data: data, OccurredAt: time.Unix(1_800_000_000, 0)}
}

func TestFeishuDocumentEvents(t *testing.T) {
	h := feishuHarness(t)
	ctx := context.Background()
	if got := h.service.EventTypes("channel:ch1", "feishu"); !slices.Equal(got, []string{feishuDocEditEvent}) {
		t.Fatalf("event types = %v", got)
	}
	for _, none := range [][2]string{{"channel:ch2", "feishu"}, {"channel:ch1", "telegram"}} {
		if got := h.service.EventTypes(none[0], none[1]); len(got) != 0 {
			t.Fatalf("%v: %v", none, got)
		}
	}
	mustHandle := func(event core.RelayEvent) {
		t.Helper()
		if err := h.service.HandleRelayEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	mustHandle(docEvent("OTHERTOKEN", "ou_alice"))
	mustHandle(docEvent("DOCTOKEN", "ou_bot"))
	if len(h.store.triggers) != 0 {
		t.Fatalf("unwatched documents and other editors must start nothing: %+v", h.store.triggers)
	}
	mustHandle(docEvent("DOCTOKEN", "ou_bot", "ou_alice"))
	burst := docEvent("DOCTOKEN", "ou_alice")
	burst.OccurredAt = burst.OccurredAt.Add(time.Second)
	mustHandle(burst)
	if len(h.store.triggers) != 1 {
		t.Fatalf("a burst of saves is one change: %+v", h.store.triggers)
	}
	later := docEvent("DOCTOKEN", "ou_alice")
	later.OccurredAt = later.OccurredAt.Add(time.Hour)
	mustHandle(later)
	if len(h.store.triggers) != 2 || h.store.triggers[0].ThreadKey != h.store.triggers[1].ThreadKey {
		t.Fatalf("a later edit is a new event on the same thread: %+v", h.store.triggers)
	}
	if !strings.Contains(h.store.triggers[0].Text, "Review the change.") || h.store.triggers[0].Actor != "ou_alice" {
		t.Fatalf("trigger = %+v", h.store.triggers[0])
	}
	var reply feishuReply
	if err := json.Unmarshal(h.store.triggers[0].Reply, &reply); err != nil || reply.ChatID != "oc_team" {
		t.Fatalf("reply = %s", h.store.triggers[0].Reply)
	}
}

func TestParseDocuments(t *testing.T) {
	refs := parseDocuments("https://a.feishu.cn/docx/D1, https://a.feishu.cn/sheets/S1\nhttps://a.feishu.cn/base/B1 https://a.feishu.cn/wiki/W1 BARE https://a.feishu.cn/other/X")
	want := []documentRef{
		{Token: "D1", FileType: "docx"}, {Token: "S1", FileType: "sheet"}, {Token: "B1", FileType: "bitable"},
		{Token: "W1", WikiNode: true}, {Token: "BARE", FileType: "docx"},
	}
	if !slices.Equal(refs, want) {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestDocumentSubscriptionAPI(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
		switch {
		case strings.HasSuffix(r.URL.Path, "tenant_access_token/internal"):
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tat"}`)
		case strings.Contains(r.URL.Path, "get_node"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"node":{"obj_token":"OBJ","obj_type":"docx"}}}`)
		case strings.Contains(r.URL.Path, "DENIED"):
			_, _ = io.WriteString(w, `{"code":1061004,"msg":"forbidden"}`)
		default:
			_, _ = io.WriteString(w, `{"code":0}`)
		}
	}))
	defer server.Close()
	docs := &FeishuAPI{Base: server.URL, HTTP: server.Client(), Channels: func(context.Context, string) (*core.Channel, error) {
		return &core.Channel{Type: "feishu", Config: map[string]string{"app_id": "a", "app_secret": "s"}}, nil
	}}
	h := newHarness(t)
	h.service.docs = docs
	source := core.SuperAgentSource{ID: "doc", Config: map[string]string{"channel_id": "c", "documents": "https://x.feishu.cn/wiki/W1"}}
	h.service.subscribeDocuments(context.Background(), source)
	if !slices.Contains(calls, "POST /open-apis/drive/v1/files/OBJ/subscribe?file_type=docx Bearer tat") {
		t.Fatalf("calls = %v", calls)
	}
	if tokens := h.service.documentTokens(source); len(tokens) != 1 || tokens[0].Token != "OBJ" {
		t.Fatalf("a wiki node must resolve to the token events carry: %+v", tokens)
	}
	if h.service.health["doc"].LastError != "" {
		t.Fatalf("health = %+v", h.service.health["doc"])
	}
	denied := core.SuperAgentSource{ID: "denied", Config: map[string]string{"channel_id": "c", "documents": "DENIED"}}
	h.service.subscribeDocuments(context.Background(), denied)
	if !strings.Contains(h.service.health["denied"].LastError, "forbidden") {
		t.Fatalf("an access problem must show on the Source: %+v", h.service.health["denied"])
	}
}

const signedMail = "Authentication-Results: mx.acme.com; dkim=pass header.d=acme.com; dmarc=pass\r\n" +
	"From: Alice <Alice@acme.com>\r\nTo: agent@acme.com\r\nSubject: =?utf-8?B?5L+u5aSN?= build\r\n" +
	"Message-Id: <m1@acme.com>\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
	"please =E4=BF=AE=E5=A4=8D it\r\n"

func emailSource() core.SuperAgentSource {
	return core.SuperAgentSource{ID: "mail", Kind: core.SuperAgentSourceEmail, Enabled: true, AllowedActors: []string{"alice@acme.com"}, Config: map[string]string{
		"imap_host": "imap.acme.com", "username": "agent@acme.com", "password": "pw", "smtp_host": "smtp.acme.com",
	}}
}

func TestParseEmail(t *testing.T) {
	source := emailSource()
	trigger, ok, err := parseEmail(source, []byte(signedMail))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if trigger.Actor != "alice@acme.com" || trigger.ActorName != "Alice" || trigger.Title != "修复 build" || !strings.Contains(trigger.Text, "please 修复 it") {
		t.Fatalf("trigger = %+v", trigger)
	}
	var reply emailReply
	if err := json.Unmarshal(trigger.Reply, &reply); err != nil || reply.To != "Alice@acme.com" || reply.Subject != "Re: 修复 build" || reply.MessageID != "<m1@acme.com>" {
		t.Fatalf("reply = %s", trigger.Reply)
	}

	threaded := strings.Replace(signedMail, "Message-Id: <m1@acme.com>", "Message-Id: <m2@acme.com>\r\nReferences: <root@acme.com> <m1@acme.com>", 1)
	next, _, _ := parseEmail(source, []byte(threaded))
	if next.ThreadKey != "email:<root@acme.com>" {
		t.Fatalf("thread = %q", next.ThreadKey)
	}

	drops := map[string]string{
		"forged, no authentication":    strings.Replace(signedMail, "Authentication-Results: mx.acme.com; dkim=pass header.d=acme.com; dmarc=pass\r\n", "", 1),
		"authentication failed":        strings.Replace(signedMail, "dkim=pass header.d=acme.com; dmarc=pass", "dkim=fail; dmarc=fail", 1),
		"signed by another domain":     strings.Replace(signedMail, "dkim=pass header.d=acme.com; dmarc=pass", "dkim=pass header.d=evil.com", 1),
		"forged header below the real": "Authentication-Results: mx.acme.com; dmarc=fail\r\n" + signedMail,
		"its own mail":                 strings.Replace(signedMail, "Alice@acme.com", "agent@acme.com", 1),
		"auto reply":                   "Auto-Submitted: auto-replied\r\n" + signedMail,
		"bulk":                         "Precedence: bulk\r\n" + signedMail,
		"mailing list":                 "List-Id: <dev.acme.com>\r\n" + signedMail,
	}
	for name, raw := range drops {
		if _, ok, err := parseEmail(source, []byte(raw)); ok || err != nil {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
		}
	}

	source.Config["trust_from_header"] = "true"
	if _, ok, _ := parseEmail(source, []byte(drops["forged, no authentication"])); !ok {
		t.Error("the operator can opt out when their server already filters senders")
	}
}

func TestParseEmailMultipartPrefersPlainText(t *testing.T) {
	raw := "Authentication-Results: mx; dmarc=pass\r\nFrom: alice@acme.com\r\nSubject: s\r\nContent-Type: multipart/alternative; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--B\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\ncGxhaW4gdGV4dA==\r\n--B--\r\n"
	trigger, ok, err := parseEmail(emailSource(), []byte(raw))
	if err != nil || !ok || !strings.Contains(trigger.Text, "plain text") || strings.Contains(trigger.Text, "html") {
		t.Fatalf("trigger=%+v ok=%v err=%v", trigger, ok, err)
	}
}

func TestBuildEmailCannotBeHeaderInjected(t *testing.T) {
	message := string(buildEmail("agent@acme.com", emailReply{
		To: "alice@acme.com\r\nBcc: evil@evil.com", Subject: "hi\r\nBcc: evil@evil.com", MessageID: "<m>\r\nBcc: evil@evil.com",
	}, "body"))
	headers, _, _ := strings.Cut(message, "\r\n\r\n")
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("injected header:\n%s", message)
		}
	}
	if !strings.Contains(message, "Auto-Submitted: auto-replied") {
		t.Fatal("replies must mark themselves automatic")
	}
}

func imapHarness(t *testing.T) (*IMAPClient, func(raw string)) {
	t.Helper()
	backend := memory.New()
	server := imapserver.New(backend)
	server.AllowInsecureAuth = true
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	user, err := backend.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := user.GetMailbox("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	client := &IMAPClient{Dial: func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}}
	deliver := func(raw string) {
		t.Helper()
		if err := mailbox.CreateMessage([]string{imap.RecentFlag}, time.Now(), bytes.NewBufferString(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return client, deliver
}

func mailFrom(index int) string {
	return strings.Replace(signedMail, "<m1@acme.com>", fmt.Sprintf("<new%d@acme.com>", index), 1)
}

func TestEmailPollerAgainstIMAPServer(t *testing.T) {
	client, deliver := imapHarness(t)
	h := newHarness(t)
	h.service.mail = client
	source := emailSource()
	source.Config["username"], source.Config["password"] = "username", "password"
	source.Config["from_address"] = "agent@acme.com"
	claim := sourceClaim{agent: h.agent, source: source}
	ctx := context.Background()
	poll := func() {
		t.Helper()
		if err := h.service.pollEmail(ctx, claim); err != nil {
			t.Fatal(err)
		}
	}

	deliver(mailFrom(0)) // History from before the Source existed.
	poll()
	if len(h.store.triggers) != 0 {
		t.Fatalf("the first poll must only set a baseline: %+v", h.store.triggers)
	}
	poll()
	if len(h.store.triggers) != 0 {
		t.Fatalf("the newest old message must not replay: %+v", h.store.triggers)
	}

	deliver(mailFrom(1))
	deliver(mailFrom(2))
	poll()
	poll()
	if len(h.store.triggers) != 2 {
		t.Fatalf("triggers = %+v", h.store.triggers)
	}
	if h.store.triggers[0].DedupeKey != "<new1@acme.com>" || h.store.triggers[1].DedupeKey != "<new2@acme.com>" {
		t.Fatalf("order = %q, %q", h.store.triggers[0].DedupeKey, h.store.triggers[1].DedupeKey)
	}

	restarted := New(Options{Store: h.store, Invoker: h.router, Workers: h.workers, Replies: h.replies, Mail: client, StateDir: h.service.state})
	if err := restarted.pollEmail(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if len(h.store.triggers) != 2 {
		t.Fatal("a restart must not replay mail")
	}
}

func TestEmailPollerResumesAtFailedMessage(t *testing.T) {
	h := newHarness(t)
	source := emailSource()
	claim := sourceClaim{agent: h.agent, source: source}
	h.store.state[source.ID] = `{"uid_validity":9,"last_uid":1}`
	fake := &scriptedMail{batch: MailBatch{Position: MailPosition{UIDValidity: 9}, Messages: []MailMessage{
		{UID: 2, Raw: []byte(mailFrom(2))}, {UID: 3, Raw: []byte("corrupt")}, {UID: 4, Raw: []byte(mailFrom(4))},
	}}}
	h.service.mail = fake
	if err := h.service.pollEmail(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if got := h.store.state[source.ID]; got != `{"uid_validity":9,"last_uid":4}` {
		t.Fatalf("an unreadable message must not block the mailbox: %s", got)
	}
	if len(h.store.triggers) != 2 {
		t.Fatalf("triggers = %+v", h.store.triggers)
	}
}

type scriptedMail struct{ batch MailBatch }

func (s *scriptedMail) Fetch(context.Context, core.SuperAgentSource, MailPosition, int) (MailBatch, error) {
	return s.batch, nil
}

func TestRepliesReachEachChannel(t *testing.T) {
	var comment struct {
		path, auth, body string
	}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		comment.path, comment.auth, comment.body = r.URL.Path, r.Header.Get("Authorization"), string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()
	chats := &chatStub{}
	var mailed string
	replies := &Replies{Chats: chats, HTTP: github.Client(), SendEmail: func(_ context.Context, _ core.SuperAgentSource, to string, message []byte) error {
		mailed = to + "\n" + string(message)
		return nil
	}}
	ctx := context.Background()

	gh := core.SuperAgentSource{Kind: core.SuperAgentSourceGitHub, Config: map[string]string{"github_token": "tok", "api_base": github.URL}}
	if err := replies.Send(ctx, gh, []byte(`{"repo":"acme/app","number":7}`), "fixed"); err != nil {
		t.Fatal(err)
	}
	if comment.path != "/repos/acme/app/issues/7/comments" || comment.auth != "Bearer tok" || !strings.Contains(comment.body, "fixed") {
		t.Fatalf("comment = %+v", comment)
	}
	delete(gh.Config, "github_token")
	if err := replies.Send(ctx, gh, []byte(`{"repo":"a/b","number":1}`), "x"); err == nil {
		t.Fatal("a missing token must surface as a reply error")
	}

	message := core.SuperAgentSource{Kind: core.SuperAgentSourceFeishuMessage}
	if err := replies.Send(ctx, message, []byte(`{"channel_id":"c","chat_id":"g","message_id":"m"}`), "hello"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(chats.calls, []string{"c g m hello"}) {
		t.Fatalf("chats = %v", chats.calls)
	}
	doc := core.SuperAgentSource{Kind: core.SuperAgentSourceFeishuDoc}
	if err := replies.Send(ctx, doc, []byte(`{"channel_id":"c"}`), "no notify chat"); err != nil || len(chats.calls) != 1 {
		t.Fatalf("a document Source without a notify chat records only: %v %v", err, chats.calls)
	}

	email := emailSource()
	if err := replies.Send(ctx, email, []byte(`{"to":"alice@acme.com","subject":"Re: x","message_id":"<m1>"}`), "done"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mailed, "alice@acme.com\n") || !strings.Contains(mailed, "In-Reply-To: <m1>") {
		t.Fatalf("mail = %s", mailed)
	}
}

type chatStub struct{ calls []string }

func (c *chatStub) ReplyToChat(_ context.Context, channelID, chatID, messageID, text string) error {
	c.calls = append(c.calls, strings.Join([]string{channelID, chatID, messageID, text}, " "))
	return nil
}

// fakeDialer routes the fleet's tunnel to a test server, so the remote path runs
// over real HTTP and SSE without SSH.
type fakeDialer struct {
	address string
	host    remote.Host
	dials   int
}

func (d *fakeDialer) Get(id string) (remote.Host, bool) {
	if id != d.host.ID {
		return remote.Host{}, false
	}
	return d.host, true
}

func (d *fakeDialer) DialContext(ctx context.Context, id, network string) (net.Conn, error) {
	d.dials++
	return (&net.Dialer{}).DialContext(ctx, network, d.address)
}

func remoteFleet(t *testing.T, handler http.HandlerFunc) (*Fleet, core.SuperAgentWorker) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dialer := &fakeDialer{address: server.Listener.Addr().String(), host: remote.Host{ID: "h1", Name: "devbox", RemoteAddr: "127.0.0.1:8765", APIToken: "bridge"}}
	return &Fleet{Hosts: dialer}, core.SuperAgentWorker{ID: "w", HostID: "h1", AgentID: "remote-agent"}
}

func sse(w http.ResponseWriter, event core.InvocationStreamEvent) {
	payload, _ := json.Marshal(event)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload)
	w.(http.Flusher).Flush()
}

func TestRemoteWorkerRunsOverInvocationStream(t *testing.T) {
	var request core.InvocationRequest
	var auth string
	fleet, worker := remoteFleet(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/invocations/stream" {
			t.Errorf("path = %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&request)
		io.WriteString(w, ": keepalive\n\n")
		sse(w, core.InvocationStreamEvent{Type: "started"})
		sse(w, core.InvocationStreamEvent{Type: "output", Text: "partial"})
		sse(w, core.InvocationStreamEvent{Type: "completed", Final: true, Result: &core.InvocationResult{Answer: "remote answer"}})
	})
	answer, err := fleet.Run(context.Background(), worker, "conv-1", "do it", RunOptions{})
	if err != nil || answer != "remote answer" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if request.AgentID != "remote-agent" || request.ConversationID != "conv-1" || request.Input != "do it" || auth != "Bearer bridge" {
		t.Fatalf("request=%+v auth=%q", request, auth)
	}
}

func TestRemoteWorkerFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		retryable bool
		contains  string
	}{
		{"turn error is the work's", func(w http.ResponseWriter, _ *http.Request) {
			sse(w, core.InvocationStreamEvent{Type: "error", Error: "tests failed"})
		}, false, "tests failed"},
		{"stream cut before a terminal event is the path's", func(w http.ResponseWriter, _ *http.Request) {
			sse(w, core.InvocationStreamEvent{Type: "output", Text: "partial"})
		}, true, "ended early"},
		{"busy conversation can be retried", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"conversation already has an active invocation"}`)
		}, true, "active invocation"},
		{"missing agent is final", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"invocation target not found"}`)
		}, false, "not found"},
	}
	for _, tc := range cases {
		fleet, worker := remoteFleet(t, tc.handler)
		_, err := fleet.Run(context.Background(), worker, "c", "i", RunOptions{})
		if err == nil || IsRetryable(err) != tc.retryable || !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: err=%v retryable=%v", tc.name, err, IsRetryable(err))
		}
	}
	if _, err := (&Fleet{Hosts: &fakeDialer{}}).Run(context.Background(), core.SuperAgentWorker{HostID: "gone"}, "c", "i", RunOptions{}); err == nil || IsRetryable(err) {
		t.Errorf("unknown host: %v", err)
	}
	if _, err := (&Fleet{}).Run(context.Background(), core.SuperAgentWorker{HostID: "x"}, "c", "i", RunOptions{}); err == nil {
		t.Error("remote control unavailable must be an error")
	}
}

func TestRemoteWorkerStopClosesTheStream(t *testing.T) {
	closed := make(chan struct{})
	entered := make(chan struct{})
	fleet, worker := remoteFleet(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // As the real handler does when decoding.
		sse(w, core.InvocationStreamEvent{Type: "started"})
		close(entered)
		<-r.Context().Done()
		close(closed)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fleet.Run(ctx, worker, "c", "i", RunOptions{})
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || IsRetryable(err) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote handler's context must end with the stream, which cancels its turn")
	}
}

func TestWorkerStatusLocalAndRemote(t *testing.T) {
	fleet, worker := remoteFleet(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]core.AgentInstance{{ID: "remote-agent", Name: "Coder", Enabled: true}, {ID: "off", Enabled: false}})
	})
	if status := fleet.Status(context.Background(), worker); !status.Available || status.Detail != "Coder" {
		t.Fatalf("status = %+v", status)
	}
	worker.AgentID = "off"
	if status := fleet.Status(context.Background(), worker); status.Available || !strings.Contains(status.Detail, "disabled") {
		t.Fatalf("status = %+v", status)
	}
	worker.AgentID = "gone"
	if status := fleet.Status(context.Background(), worker); status.Available {
		t.Fatalf("status = %+v", status)
	}
	local := &Fleet{Agents: newMemoryStore()}
	if status := local.Status(context.Background(), core.SuperAgentWorker{AgentID: "coder"}); !status.Available {
		t.Fatalf("status = %+v", status)
	}
	if status := local.Status(context.Background(), core.SuperAgentWorker{AgentID: "missing"}); status.Available {
		t.Fatalf("status = %+v", status)
	}
	unreachable := &Fleet{Hosts: &fakeDialer{address: "127.0.0.1:1", host: remote.Host{ID: "h", Name: "down"}}}
	if status := unreachable.Status(context.Background(), core.SuperAgentWorker{HostID: "h", AgentID: "a"}); status.Available || status.Detail == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestParseRouterDecision(t *testing.T) {
	decision, err := parseRouterDecision("On it.\n\n<agentmux-action>\n```json\n{\"action\":\"ACCEPT\",\"worker_id\":\"w\",\"instructions\":\"see memory/repos.md\"}\n```\n</agentmux-action>\n")
	if err != nil || len(decision.Actions) != 1 || decision.Actions[0].Action != actionAccept || decision.Actions[0].WorkerID != "w" || decision.Reply != "On it." || decision.Actions[0].Instructions != "see memory/repos.md" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	// Without a block, the whole message is the reply.
	decision, err = parseRouterDecision("  The build is green: tickets/tkt_1/result.md has the details.  ")
	if err != nil || len(decision.Actions) != 0 || decision.Reply != "The build is green: tickets/tkt_1/result.md has the details." {
		t.Fatalf("plain reply: decision=%+v err=%v", decision, err)
	}
	if decision, err := parseRouterDecision(""); err != nil || len(decision.Actions) != 0 || decision.Reply != "" {
		t.Fatalf("empty answer: decision=%+v err=%v", decision, err)
	}
	if decision, err := parseRouterDecision(`<agentmux-action>{"action":"none","reason":"chit-chat"}</agentmux-action>`); err != nil || len(decision.Actions) != 0 || decision.Reply != "" {
		t.Fatalf("no action: decision=%+v err=%v", decision, err)
	}
	// An answer in the old JSON-only form is never sent as a reply.
	if decision, err := parseRouterDecision(`{"decision":"stop","ticket_id":"t","reply":"stopped"}`); err != nil || len(decision.Actions) != 1 || decision.Actions[0].Action != actionStop || decision.Reply != "stopped" {
		t.Fatalf("legacy: decision=%+v err=%v", decision, err)
	}
	// A closing tag cut short, or missing at the end, still closes the block:
	// the router's work is not thrown away over a ">".
	for _, answer := range []string{
		"已转交。\n<agentmux-action>\n{\"action\":\"accept\",\"worker_id\":\"auto:codex\"}\n</agentmux-action",
		"已转交。\n<agentmux-action>\n{\"action\":\"accept\",\"worker_id\":\"auto:codex\"}\n",
	} {
		decision, err := parseRouterDecision(answer)
		if err != nil || decision.Reply != "已转交。" || len(decision.Actions) != 1 || decision.Actions[0].WorkerID != "auto:codex" {
			t.Fatalf("%q: decision=%+v err=%v", answer, decision, err)
		}
	}
	// Several actions: a list, or blocks in a row, in order.
	decision, err = parseRouterDecision("Two tasks.\n```\n<agentmux-action>\n[{\"action\":\"accept\",\"ref\":\"a\",\"worker_id\":\"w\"},{\"action\":\"accept\",\"worker_id\":\"w\",\"depends_on\":[\"a\"]}]\n</agentmux-action>\n```\n<agentmux-action>{\"action\":\"post\",\"to\":\"chat:oc_1\",\"text\":\"FYI\"}</agentmux-action>")
	if err != nil || decision.Reply != "Two tasks." || len(decision.Actions) != 3 || decision.Actions[1].DependsOn[0] != "a" || decision.Actions[2].To != "chat:oc_1" {
		t.Fatalf("several: decision=%+v err=%v", decision, err)
	}
	for _, bad := range []string{
		`<agentmux-action>{"action":"accept"}</agentmux-action>`,
		`<agentmux-action>{"action":"stop"}</agentmux-action>`,
		`<agentmux-action>{"action":"attach"}</agentmux-action>`,
		`<agentmux-action>{"action":"post","to":"chat:oc_1"}</agentmux-action>`,
		`<agentmux-action>{"action":"delete_repo"}</agentmux-action>`,
		`<agentmux-action>{broken}</agentmux-action>`,
	} {
		if _, err := parseRouterDecision(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
