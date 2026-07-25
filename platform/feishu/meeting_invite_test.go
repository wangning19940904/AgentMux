package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type fakeMeetingInviteTransport struct {
	mu sync.Mutex

	sentRecipients []string
	sentInvites    []meetingInvite
	sentCards      []string
	sendErrors     []error
	updates        []meetingInvite
	joinCalls      []string
	joinResults    []fakeMeetingJoinResult
	observedJoinID string
	observedUserID string
	observedChatID string
}

type fakeMeetingJoinResult struct {
	meetingID string
	err       error
}

func (f *fakeMeetingInviteTransport) SendMeetingInviteCard(_ context.Context, recipient string, invite meetingInvite) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentRecipients = append(f.sentRecipients, recipient)
	f.sentInvites = append(f.sentInvites, invite)
	f.sentCards = append(f.sentCards, buildMeetingInviteCard(invite))
	if len(f.sendErrors) > 0 {
		err := f.sendErrors[0]
		f.sendErrors = f.sendErrors[1:]
		if err != nil {
			return "", err
		}
	}
	return "om_meeting_invite", nil
}

func (f *fakeMeetingInviteTransport) UpdateMeetingInviteCard(_ context.Context, _ string, invite meetingInvite) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, invite)
	return nil
}

func (f *fakeMeetingInviteTransport) JoinMeeting(_ context.Context, meetingNo string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joinCalls = append(f.joinCalls, meetingNo)
	if len(f.joinResults) == 0 {
		return "meeting-long-id", nil
	}
	result := f.joinResults[0]
	f.joinResults = f.joinResults[1:]
	return result.meetingID, result.err
}

func (f *fakeMeetingInviteTransport) MeetingInviteJoined(meetingID, inviterOpenID, approvalChatID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observedJoinID = meetingID
	f.observedUserID = inviterOpenID
	f.observedChatID = approvalChatID
}

func newTestMeetingInviteController(transport meetingInviteTransport) *meetingInviteController {
	controller := newMeetingInviteController(transport)
	controller.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	controller.newNonce = func() (string, error) { return "0123456789abcdef", nil }
	controller.runAsync = func(fn func()) { fn() }
	controller.report = func(string, error) {}
	return controller
}

func meetingInvitePayload(eventID string) []byte {
	payload := map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":   eventID,
			"event_type": meetingInvitedEventType,
			"app_id":     "cli_test",
		},
		"event": map[string]any{
			"meeting": map[string]any{
				"id":         "meeting-source-id",
				"meeting_no": "123456789",
				"topic":      "研发周会",
			},
			"bot": map[string]any{
				"id":        "ou_bot",
				"user_name": "AgentMux",
			},
			"inviter": map[string]any{
				"id":        "ou_inviter",
				"user_name": "张三",
			},
			"invite_time": "1800000000000",
		},
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

func meetingInviteActionEvent(invite meetingInvite, operator, decision string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: operator},
			Context: &callback.Context{
				OpenChatID:    "oc_direct",
				OpenMessageID: "om_meeting_invite",
			},
			Action: &callback.CallBackAction{
				Tag: "button",
				Value: map[string]interface{}{
					modelPickerActionKey: meetingInviteAction,
					"invite_id":          invite.ID,
					"nonce":              invite.Nonce,
					"decision":           decision,
				},
			},
		},
	}
}

func TestMeetingInvitationSendsOneApprovalCardForDuplicateEvent(t *testing.T) {
	transport := &fakeMeetingInviteTransport{}
	controller := newTestMeetingInviteController(transport)
	payload := meetingInvitePayload("evt-invite-1")

	if err := controller.HandleInvitation(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if err := controller.HandleInvitation(context.Background(), payload); err != nil {
		t.Fatal(err)
	}

	if len(transport.sentRecipients) != 1 || transport.sentRecipients[0] != "ou_inviter" {
		t.Fatalf("sent recipients = %v", transport.sentRecipients)
	}
	if len(transport.sentCards) != 1 {
		t.Fatalf("sent cards = %d, want 1", len(transport.sentCards))
	}
	card := transport.sentCards[0]
	if !json.Valid([]byte(card)) {
		t.Fatalf("card is invalid JSON: %s", card)
	}
	for _, want := range []string{
		`"schema":"2.0"`,
		`"content":"加入会议"`,
		`"content":"拒绝"`,
		`"agentmux_action":"meeting_invite"`,
		`"invite_id":"evt-invite-1"`,
		`"nonce":"0123456789abcdef"`,
		`"enable_forward":false`,
		"123456789",
		"研发周会",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q: %s", want, card)
		}
	}
	if strings.Contains(card, `"meeting_no"`) {
		t.Fatalf("card callback leaks authoritative meeting_no: %s", card)
	}
}

func TestMeetingInvitationCanRetryAfterCardSendFailure(t *testing.T) {
	transport := &fakeMeetingInviteTransport{
		sendErrors: []error{errors.New("temporary IM failure"), nil},
	}
	controller := newTestMeetingInviteController(transport)
	payload := meetingInvitePayload("evt-card-retry")

	if err := controller.HandleInvitation(context.Background(), payload); err == nil {
		t.Fatal("first card send unexpectedly succeeded")
	}
	if err := controller.HandleInvitation(context.Background(), payload); err != nil {
		t.Fatalf("retry card send: %v", err)
	}
	if len(transport.sentCards) != 2 {
		t.Fatalf("sent cards = %d, want 2 attempts", len(transport.sentCards))
	}
}

func TestMeetingInviteJoinRequiresExplicitAuthorizedClickAndIsIdempotent(t *testing.T) {
	transport := &fakeMeetingInviteTransport{}
	controller := newTestMeetingInviteController(transport)
	if err := controller.HandleInvitation(context.Background(), meetingInvitePayload("evt-join")); err != nil {
		t.Fatal(err)
	}
	invite := transport.sentInvites[0]

	handled, response := controller.HandleAction(
		context.Background(),
		meetingInviteActionEvent(invite, "ou_inviter", meetingInviteDecisionJoin),
	)
	if !handled || response == nil || response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("join callback response = %+v, handled=%v", response, handled)
	}
	if len(transport.joinCalls) != 1 || transport.joinCalls[0] != "123456789" {
		t.Fatalf("join calls = %v", transport.joinCalls)
	}
	if len(transport.updates) != 2 ||
		transport.updates[0].State != meetingInviteJoining ||
		transport.updates[1].State != meetingInviteJoined ||
		transport.updates[1].JoinedMeetingID != "meeting-long-id" {
		t.Fatalf("updates = %+v", transport.updates)
	}
	if transport.observedJoinID != "meeting-long-id" ||
		transport.observedUserID != "ou_inviter" ||
		transport.observedChatID != "oc_direct" {
		t.Fatalf("observed join = meeting %q user %q chat %q", transport.observedJoinID, transport.observedUserID, transport.observedChatID)
	}

	handled, response = controller.HandleAction(
		context.Background(),
		meetingInviteActionEvent(invite, "ou_inviter", meetingInviteDecisionJoin),
	)
	if !handled || response == nil || response.Toast == nil || response.Toast.Type != "warning" {
		t.Fatalf("duplicate callback response = %+v, handled=%v", response, handled)
	}
	if len(transport.joinCalls) != 1 {
		t.Fatalf("duplicate callback joined again: %v", transport.joinCalls)
	}
}

func TestMeetingInviteRejectNeverCallsJoin(t *testing.T) {
	transport := &fakeMeetingInviteTransport{}
	controller := newTestMeetingInviteController(transport)
	if err := controller.HandleInvitation(context.Background(), meetingInvitePayload("evt-reject")); err != nil {
		t.Fatal(err)
	}
	invite := transport.sentInvites[0]

	handled, response := controller.HandleAction(
		context.Background(),
		meetingInviteActionEvent(invite, "ou_inviter", meetingInviteDecisionReject),
	)
	if !handled || response == nil || response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("reject callback response = %+v, handled=%v", response, handled)
	}
	if len(transport.joinCalls) != 0 {
		t.Fatalf("reject called JoinMeeting: %v", transport.joinCalls)
	}
	if len(transport.updates) != 1 || transport.updates[0].State != meetingInviteRejected {
		t.Fatalf("updates = %+v", transport.updates)
	}
}

func TestMeetingInviteRejectsUnauthorizedOrTamperedActions(t *testing.T) {
	transport := &fakeMeetingInviteTransport{}
	controller := newTestMeetingInviteController(transport)
	if err := controller.HandleInvitation(context.Background(), meetingInvitePayload("evt-auth")); err != nil {
		t.Fatal(err)
	}
	invite := transport.sentInvites[0]

	for name, mutate := range map[string]func(*callback.CardActionTriggerEvent){
		"wrong operator": func(event *callback.CardActionTriggerEvent) {
			event.Event.Operator.OpenID = "ou_someone_else"
		},
		"wrong nonce": func(event *callback.CardActionTriggerEvent) {
			event.Event.Action.Value["nonce"] = "tampered"
		},
		"wrong message": func(event *callback.CardActionTriggerEvent) {
			event.Event.Context.OpenMessageID = "om_forwarded"
		},
	} {
		t.Run(name, func(t *testing.T) {
			event := meetingInviteActionEvent(invite, "ou_inviter", meetingInviteDecisionJoin)
			mutate(event)
			handled, response := controller.HandleAction(context.Background(), event)
			if !handled || response == nil || response.Toast == nil || response.Toast.Type != "warning" {
				t.Fatalf("response = %+v, handled=%v", response, handled)
			}
		})
	}
	if len(transport.joinCalls) != 0 {
		t.Fatalf("unauthorized action called JoinMeeting: %v", transport.joinCalls)
	}
}

func TestMeetingInviteJoinFailureCanBeRetried(t *testing.T) {
	transport := &fakeMeetingInviteTransport{
		joinResults: []fakeMeetingJoinResult{
			{err: errors.New("meeting is not ready")},
			{meetingID: "meeting-after-retry"},
		},
	}
	controller := newTestMeetingInviteController(transport)
	if err := controller.HandleInvitation(context.Background(), meetingInvitePayload("evt-retry")); err != nil {
		t.Fatal(err)
	}
	invite := transport.sentInvites[0]
	event := meetingInviteActionEvent(invite, "ou_inviter", meetingInviteDecisionJoin)

	controller.HandleAction(context.Background(), event)
	if got := transport.updates[len(transport.updates)-1]; got.State != meetingInvitePending || !strings.Contains(got.LastError, "not ready") {
		t.Fatalf("failed join update = %+v", got)
	}
	controller.HandleAction(context.Background(), event)
	if len(transport.joinCalls) != 2 {
		t.Fatalf("join calls = %v", transport.joinCalls)
	}
	if got := transport.updates[len(transport.updates)-1]; got.State != meetingInviteJoined || got.JoinedMeetingID != "meeting-after-retry" {
		t.Fatalf("retried join update = %+v", got)
	}
}

func TestParseMeetingInvitationValidatesMeetingNumberAndInviter(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	nonce := func() (string, error) { return "nonce", nil }

	var payload map[string]any
	if err := json.Unmarshal(meetingInvitePayload("evt-invalid"), &payload); err != nil {
		t.Fatal(err)
	}
	event := payload["event"].(map[string]any)
	meeting := event["meeting"].(map[string]any)
	meeting["meeting_no"] = "123"
	invalidNumber, _ := json.Marshal(payload)
	if _, err := parseMeetingInvitation(invalidNumber, now, nonce); err == nil || !strings.Contains(err.Error(), "9-digit") {
		t.Fatalf("invalid meeting number error = %v", err)
	}

	meeting["meeting_no"] = "123456789"
	event["inviter"] = map[string]any{"id": "not-an-open-id"}
	invalidInviter, _ := json.Marshal(payload)
	if _, err := parseMeetingInvitation(invalidInviter, now, nonce); err == nil || !strings.Contains(err.Error(), "open_id") {
		t.Fatalf("invalid inviter error = %v", err)
	}
}

func TestJoinMeetingUsesTenantTokenAndRequiredJoinType(t *testing.T) {
	var joinRequests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = writer.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"tenant-token"}`))
		case meetingInviteJoinAPIPath:
			joinRequests++
			if got := request.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Errorf("Authorization = %q", got)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				JoinIdentify struct {
					MeetingNo string `json:"meeting_no"`
				} `json:"join_identify"`
				JoinType int `json:"join_type"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode join request: %v; body=%s", err, body)
			}
			if payload.JoinIdentify.MeetingNo != "123456789" || payload.JoinType != 1 {
				t.Errorf("join payload = %+v", payload)
			}
			_, _ = writer.Write([]byte(`{"code":0,"msg":"success","data":{"meeting":{"id":"meeting-long-id"}}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := &larkClient{
		platform: "feishu",
		api: lark.NewClient(
			"cli_test",
			"secret",
			lark.WithOpenBaseUrl(server.URL),
		),
	}
	meetingID, err := client.JoinMeeting(context.Background(), "123456789")
	if err != nil {
		t.Fatal(err)
	}
	if meetingID != "meeting-long-id" || joinRequests != 1 {
		t.Fatalf("meetingID=%q joinRequests=%d", meetingID, joinRequests)
	}
}
