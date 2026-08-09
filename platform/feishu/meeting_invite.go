package feishu

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/wangning19940904/AgentMux/core"
)

const (
	meetingInvitedEventType = "vc.bot.meeting_invited_v1"
	meetingInviteAction     = "meeting_invite"

	meetingInviteDecisionJoin   = "join"
	meetingInviteDecisionReject = "reject"

	meetingInviteTTL             = 30 * time.Minute
	meetingInviteActionTimeout   = 45 * time.Second
	meetingInviteJoinAPIPath     = "/open-apis/vc/v1/bots/join"
	meetingInviteMessageAPIPath  = "/open-apis/vc/v1/bots/message"
	meetingMessageWriteScope     = "vc:meeting.message:write"
	meetingInviteMaxDisplayError = 160
)

type meetingInviteState string

const (
	meetingInvitePending  meetingInviteState = "pending"
	meetingInviteJoining  meetingInviteState = "joining"
	meetingInviteJoined   meetingInviteState = "joined"
	meetingInviteRejected meetingInviteState = "rejected"
	meetingInviteExpired  meetingInviteState = "expired"
)

var (
	errMeetingInviteNotFound       = errors.New("meeting invite not found")
	errMeetingInviteUnauthorized   = errors.New("meeting invite action is unauthorized")
	errMeetingInviteExpired        = errors.New("meeting invite expired")
	errMeetingInviteAlreadyDecided = errors.New("meeting invite was already decided")
	errMeetingInviteInvalidAction  = errors.New("invalid meeting invite action")
)

type botMeetingInvitedEvent struct {
	Schema string `json:"schema"`
	Header struct {
		EventID    string `json:"event_id"`
		EventType  string `json:"event_type"`
		AppID      string `json:"app_id"`
		CreateTime string `json:"create_time"`
	} `json:"header"`
	Event struct {
		CallID  string `json:"call_id"`
		Meeting struct {
			ID        string `json:"id"`
			MeetingNo string `json:"meeting_no"`
			Topic     string `json:"topic"`
			CallID    string `json:"call_id"`
		} `json:"meeting"`
		Bot        meetingInviteActor `json:"bot"`
		Inviter    meetingInviteActor `json:"inviter"`
		InviteTime string             `json:"invite_time"`
	} `json:"event"`
}

type meetingInviteActor struct {
	ID       meetingInviteActorID `json:"id"`
	OpenID   string               `json:"open_id"`
	UserName string               `json:"user_name"`
	Name     string               `json:"name"`
}

// meetingInviteActorID accepts both the current VC callback shape
// ("id":{"open_id":"ou_..."}) and the legacy flat string used by older
// examples. Keeping both forms here prevents a schema variation in the bot
// actor from dropping the entire invitation before it reaches the Console.
type meetingInviteActorID struct {
	OpenID string `json:"open_id"`
	legacy string
}

func (id *meetingInviteActorID) UnmarshalJSON(data []byte) error {
	var legacy string
	if err := json.Unmarshal(data, &legacy); err == nil {
		id.OpenID = ""
		id.legacy = strings.TrimSpace(legacy)
		return nil
	}

	type actorID meetingInviteActorID
	var nested actorID
	if err := json.Unmarshal(data, &nested); err != nil {
		return fmt.Errorf("decode meeting invite actor id: %w", err)
	}
	*id = meetingInviteActorID(nested)
	id.OpenID = strings.TrimSpace(id.OpenID)
	id.legacy = ""
	return nil
}

type meetingInvite struct {
	ID              string
	Nonce           string
	MeetingID       string
	MeetingNo       string
	CallID          string
	Topic           string
	InviterOpenID   string
	InviterName     string
	InviteTime      string
	CardMessageID   string
	ApprovalChatID  string
	JoinedMeetingID string
	GreetingSent    bool
	GreetingWarning string
	State           meetingInviteState
	LastError       string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	prompting       bool
}

type meetingJoinRequest struct {
	MeetingNo string
	CallID    string
}

type meetingJoinOutcome struct {
	MeetingID       string
	GreetingSent    bool
	GreetingWarning string
}

type meetingInviteTransport interface {
	SendMeetingInviteCard(context.Context, string, meetingInvite) (string, error)
	UpdateMeetingInviteCard(context.Context, string, meetingInvite) error
	JoinMeeting(context.Context, meetingJoinRequest) (meetingJoinOutcome, error)
}

type meetingInviteJoinObserver interface {
	MeetingInviteJoined(meetingID, inviterOpenID, approvalChatID string)
}

type meetingInviteChangeObserver interface {
	MeetingInviteChanged()
}

type meetingInviteController struct {
	mu        sync.Mutex
	invites   map[string]*meetingInvite
	transport meetingInviteTransport
	now       func() time.Time
	newNonce  func() (string, error)
	runAsync  func(func())
	report    func(string, error)
}

func newMeetingInviteController(transport meetingInviteTransport) *meetingInviteController {
	return &meetingInviteController{
		invites:   make(map[string]*meetingInvite),
		transport: transport,
		now:       time.Now,
		newNonce:  randomMeetingInviteNonce,
		runAsync:  func(fn func()) { go fn() },
		report: func(message string, err error) {
			slog.Error(message, "error", err)
		},
	}
}

func (c *meetingInviteController) HandleInvitation(ctx context.Context, payload []byte) error {
	invite, err := parseMeetingInvitation(payload, c.now(), c.newNonce)
	if err != nil {
		return err
	}
	invite, shouldPrompt := c.claimPrompt(invite)
	if !shouldPrompt {
		return nil
	}
	c.notifyChanged()

	messageID, err := c.transport.SendMeetingInviteCard(ctx, invite.InviterOpenID, invite)
	if err != nil {
		c.releasePrompt(invite.ID)
		return fmt.Errorf("send meeting invite approval card: %w", err)
	}
	c.completePrompt(invite.ID, messageID)
	return nil
}

func (c *meetingInviteController) HandleAction(appCtx context.Context, event *callback.CardActionTriggerEvent) (bool, *callback.CardActionTriggerResponse) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return false, nil
	}
	value := event.Event.Action.Value
	if stringValue(value[modelPickerActionKey]) != meetingInviteAction {
		return false, nil
	}
	if event.Event.Action.Tag != "button" {
		return true, meetingInviteToast("error", "无法识别这个操作")
	}

	operatorOpenID := ""
	if event.Event.Operator != nil {
		operatorOpenID = strings.TrimSpace(event.Event.Operator.OpenID)
	}
	messageID := ""
	chatID := ""
	if event.Event.Context != nil {
		messageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
		chatID = strings.TrimSpace(event.Event.Context.OpenChatID)
	}
	inviteID := stringValue(value["invite_id"])
	nonce := stringValue(value["nonce"])
	decision := stringValue(value["decision"])

	invite, err := c.beginDecision(inviteID, nonce, operatorOpenID, messageID, chatID, decision)
	if err != nil {
		if errors.Is(err, errMeetingInviteExpired) && invite.CardMessageID != "" {
			c.updateCardAsync(appCtx, invite)
		}
		switch {
		case errors.Is(err, errMeetingInviteUnauthorized):
			return true, meetingInviteToast("warning", "你无权处理这条会议邀请")
		case errors.Is(err, errMeetingInviteExpired):
			return true, meetingInviteToast("warning", "这条会议邀请已经失效")
		case errors.Is(err, errMeetingInviteAlreadyDecided):
			return true, meetingInviteToast("warning", "这条会议邀请已经处理过了")
		default:
			return true, meetingInviteToast("error", "无法处理这条会议邀请")
		}
	}
	c.notifyChanged()

	switch decision {
	case meetingInviteDecisionReject:
		c.updateCardAsync(appCtx, invite)
		return true, meetingInviteToast("success", "已拒绝，本次不会加入会议")
	case meetingInviteDecisionJoin:
		c.runAsync(func() {
			actionCtx, cancel := context.WithTimeout(appCtx, meetingInviteActionTimeout)
			defer cancel()
			_, _ = c.processJoin(actionCtx, invite)
		})
		return true, meetingInviteToast("success", "正在加入会议…")
	default:
		return true, meetingInviteToast("error", "无法识别这个操作")
	}
}

func (c *meetingInviteController) processJoin(ctx context.Context, invite meetingInvite) (meetingInvite, error) {
	if err := c.transport.UpdateMeetingInviteCard(ctx, invite.CardMessageID, invite); err != nil {
		c.report("update meeting invite card to joining", err)
	}

	outcome, joinErr := c.transport.JoinMeeting(ctx, meetingJoinRequest{
		MeetingNo: invite.MeetingNo,
		CallID:    invite.CallID,
	})
	finalInvite, err := c.finishJoin(invite.ID, outcome, joinErr)
	if err != nil {
		c.report("finish meeting invite join state", err)
		return meetingInvite{}, err
	}
	c.notifyChanged()
	if joinErr != nil {
		c.report("join meeting after approval", joinErr)
	}
	if finalInvite.State == meetingInviteJoined {
		if observer, ok := c.transport.(meetingInviteJoinObserver); ok {
			observer.MeetingInviteJoined(finalInvite.JoinedMeetingID, finalInvite.InviterOpenID, finalInvite.ApprovalChatID)
		}
	}
	if err := c.transport.UpdateMeetingInviteCard(ctx, finalInvite.CardMessageID, finalInvite); err != nil {
		c.report("update meeting invite card after join", err)
	}
	return finalInvite, joinErr
}

func (c *meetingInviteController) updateCardAsync(appCtx context.Context, invite meetingInvite) {
	c.runAsync(func() {
		updateCtx, cancel := context.WithTimeout(appCtx, meetingInviteActionTimeout)
		defer cancel()
		if err := c.transport.UpdateMeetingInviteCard(updateCtx, invite.CardMessageID, invite); err != nil {
			c.report("update meeting invite decision card", err)
		}
	})
}

func (c *meetingInviteController) claimPrompt(candidate meetingInvite) (meetingInvite, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked()

	if current, ok := c.invites[candidate.ID]; ok {
		if current.CardMessageID == "" && !current.prompting && current.State == meetingInvitePending {
			current.prompting = true
			return *current, true
		}
		return *current, false
	}
	candidate.prompting = true
	c.invites[candidate.ID] = &candidate
	return candidate, true
}

func (c *meetingInviteController) completePrompt(inviteID, messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if invite := c.invites[inviteID]; invite != nil {
		invite.CardMessageID = strings.TrimSpace(messageID)
		invite.prompting = false
	}
}

func (c *meetingInviteController) releasePrompt(inviteID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if invite := c.invites[inviteID]; invite != nil {
		invite.prompting = false
	}
}

func (c *meetingInviteController) PendingInvitations() []meetingInvite {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked()
	now := c.now()
	out := make([]meetingInvite, 0, len(c.invites))
	for _, invite := range c.invites {
		if invite.State == meetingInvitePending && !now.Before(invite.ExpiresAt) {
			invite.State = meetingInviteExpired
		}
		if invite.State == meetingInvitePending {
			out = append(out, *invite)
		}
	}
	return out
}

func (c *meetingInviteController) RespondFromConsole(
	ctx context.Context,
	inviteID, nonce, decision string,
) (meetingInvite, error) {
	invite, err := c.beginConsoleDecision(inviteID, nonce, decision)
	if err != nil {
		return invite, err
	}
	c.notifyChanged()
	if decision == meetingInviteDecisionReject {
		if err := c.transport.UpdateMeetingInviteCard(ctx, invite.CardMessageID, invite); err != nil {
			c.report("update meeting invite card after console rejection", err)
		}
		return invite, nil
	}
	return c.processJoin(ctx, invite)
}

func (c *meetingInviteController) notifyChanged() {
	if observer, ok := c.transport.(meetingInviteChangeObserver); ok {
		observer.MeetingInviteChanged()
	}
}

func (c *meetingInviteController) beginDecision(inviteID, nonce, operatorOpenID, messageID, chatID, decision string) (meetingInvite, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	invite := c.invites[inviteID]
	if invite == nil {
		return meetingInvite{}, errMeetingInviteNotFound
	}
	if !c.now().Before(invite.ExpiresAt) {
		invite.State = meetingInviteExpired
		return *invite, errMeetingInviteExpired
	}
	if operatorOpenID == "" || operatorOpenID != invite.InviterOpenID ||
		messageID == "" || messageID != invite.CardMessageID ||
		!constantTimeStringEqual(nonce, invite.Nonce) {
		return *invite, errMeetingInviteUnauthorized
	}
	if invite.State != meetingInvitePending {
		return *invite, errMeetingInviteAlreadyDecided
	}
	return transitionMeetingInviteLocked(invite, decision, chatID)
}

func (c *meetingInviteController) beginConsoleDecision(inviteID, nonce, decision string) (meetingInvite, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	invite := c.invites[inviteID]
	if invite == nil {
		return meetingInvite{}, errMeetingInviteNotFound
	}
	if !c.now().Before(invite.ExpiresAt) {
		invite.State = meetingInviteExpired
		return *invite, errMeetingInviteExpired
	}
	if !constantTimeStringEqual(nonce, invite.Nonce) {
		return *invite, errMeetingInviteUnauthorized
	}
	if invite.State != meetingInvitePending {
		return *invite, errMeetingInviteAlreadyDecided
	}
	return transitionMeetingInviteLocked(invite, decision, "")
}

func transitionMeetingInviteLocked(invite *meetingInvite, decision, chatID string) (meetingInvite, error) {
	switch decision {
	case meetingInviteDecisionJoin:
		invite.State = meetingInviteJoining
		invite.LastError = ""
		invite.ApprovalChatID = strings.TrimSpace(chatID)
	case meetingInviteDecisionReject:
		invite.State = meetingInviteRejected
		invite.LastError = ""
		invite.ApprovalChatID = strings.TrimSpace(chatID)
	default:
		return *invite, errMeetingInviteInvalidAction
	}
	return *invite, nil
}

func (c *meetingInviteController) finishJoin(inviteID string, outcome meetingJoinOutcome, joinErr error) (meetingInvite, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	invite := c.invites[inviteID]
	if invite == nil {
		return meetingInvite{}, errMeetingInviteNotFound
	}
	if invite.State != meetingInviteJoining {
		return *invite, errMeetingInviteAlreadyDecided
	}
	if joinErr != nil {
		invite.State = meetingInvitePending
		invite.LastError = safeMeetingInviteError(joinErr)
		return *invite, nil
	}
	if strings.TrimSpace(outcome.MeetingID) == "" {
		invite.State = meetingInvitePending
		invite.LastError = "飞书未返回会议 ID，请重试"
		return *invite, nil
	}
	invite.State = meetingInviteJoined
	invite.JoinedMeetingID = strings.TrimSpace(outcome.MeetingID)
	invite.GreetingSent = outcome.GreetingSent
	invite.GreetingWarning = strings.TrimSpace(outcome.GreetingWarning)
	invite.LastError = ""
	return *invite, nil
}

func (c *meetingInviteController) pruneLocked() {
	cutoff := c.now().Add(-24 * time.Hour)
	for id, invite := range c.invites {
		if invite.ExpiresAt.Before(cutoff) {
			delete(c.invites, id)
		}
	}
}

func parseMeetingInvitation(payload []byte, now time.Time, nonceFactory func() (string, error)) (meetingInvite, error) {
	var event botMeetingInvitedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return meetingInvite{}, fmt.Errorf("decode %s event: %w", meetingInvitedEventType, err)
	}
	if event.Header.EventType != "" && event.Header.EventType != meetingInvitedEventType {
		return meetingInvite{}, fmt.Errorf("unexpected meeting invite event type %q", event.Header.EventType)
	}
	meetingNo := strings.TrimSpace(event.Event.Meeting.MeetingNo)
	if !isNineDigitMeetingNumber(meetingNo) {
		return meetingInvite{}, fmt.Errorf("%s event has invalid 9-digit meeting_no", meetingInvitedEventType)
	}
	inviterOpenID := event.Event.Inviter.approvalOpenID()
	if inviterOpenID == "" {
		return meetingInvite{}, fmt.Errorf("%s event does not contain an inviter open_id", meetingInvitedEventType)
	}
	nonce, err := nonceFactory()
	if err != nil {
		return meetingInvite{}, fmt.Errorf("create meeting invite nonce: %w", err)
	}
	inviteID := strings.TrimSpace(event.Header.EventID)
	if inviteID == "" {
		sum := sha256.Sum256([]byte(strings.Join([]string{
			meetingNo,
			strings.TrimSpace(event.Event.Meeting.ID),
			strings.TrimSpace(event.Event.InviteTime),
			inviterOpenID,
		}, "\x00")))
		inviteID = "meeting-invite-" + hex.EncodeToString(sum[:12])
	}
	topic := strings.TrimSpace(event.Event.Meeting.Topic)
	if topic == "" {
		topic = "未命名会议"
	}
	inviterName := event.Event.Inviter.displayName()
	if inviterName == "" {
		inviterName = inviterOpenID
	}
	callID := strings.TrimSpace(event.Event.CallID)
	if callID == "" {
		callID = strings.TrimSpace(event.Event.Meeting.CallID)
	}
	return meetingInvite{
		ID:            inviteID,
		Nonce:         nonce,
		MeetingID:     strings.TrimSpace(event.Event.Meeting.ID),
		MeetingNo:     meetingNo,
		CallID:        callID,
		Topic:         topic,
		InviterOpenID: inviterOpenID,
		InviterName:   inviterName,
		InviteTime:    strings.TrimSpace(event.Event.InviteTime),
		State:         meetingInvitePending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(meetingInviteTTL),
	}, nil
}

func (a meetingInviteActor) approvalOpenID() string {
	if openID := strings.TrimSpace(a.OpenID); strings.HasPrefix(openID, "ou_") {
		return openID
	}
	if openID := strings.TrimSpace(a.ID.OpenID); strings.HasPrefix(openID, "ou_") {
		return openID
	}
	if id := strings.TrimSpace(a.ID.legacy); strings.HasPrefix(id, "ou_") {
		return id
	}
	return ""
}

func (a meetingInviteActor) displayName() string {
	if name := strings.TrimSpace(a.UserName); name != "" {
		return name
	}
	return strings.TrimSpace(a.Name)
}

func randomMeetingInviteNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) || left == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func isNineDigitMeetingNumber(value string) bool {
	if len(value) != 9 {
		return false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func safeMeetingInviteError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	runes := []rune(message)
	if len(runes) > meetingInviteMaxDisplayError {
		message = string(runes[:meetingInviteMaxDisplayError]) + "…"
	}
	return message
}

func meetingInviteToast(toastType, content string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: toastType, Content: content},
	}
}

func (c *larkClient) SendMeetingInviteCard(ctx context.Context, recipientOpenID string, invite meetingInvite) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("open_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(recipientOpenID).
			MsgType(larkim.MsgTypeInteractive).
			Content(buildMeetingInviteCard(invite)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s send meeting invite card failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send meeting invite card: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) UpdateMeetingInviteCard(ctx context.Context, messageID string, invite meetingInvite) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(buildMeetingInviteCard(invite)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Patch(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s update meeting invite card failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) JoinMeeting(ctx context.Context, request meetingJoinRequest) (meetingJoinOutcome, error) {
	body := map[string]any{
		"join_identify": map[string]string{"meeting_no": strings.TrimSpace(request.MeetingNo)},
		"join_type":     1,
	}
	if callID := strings.TrimSpace(request.CallID); callID != "" {
		body["call_id"] = callID
	}
	resp, err := c.api.Post(ctx, meetingInviteJoinAPIPath, body, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return meetingJoinOutcome{}, err
	}
	if resp == nil {
		return meetingJoinOutcome{}, errors.New("join meeting returned an empty response")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return meetingJoinOutcome{}, fmt.Errorf("join meeting returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Meeting struct {
				ID string `json:"id"`
			} `json:"meeting"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return meetingJoinOutcome{}, fmt.Errorf("decode join meeting response: %w", err)
	}
	if result.Code != 0 {
		return meetingJoinOutcome{}, fmt.Errorf("join meeting failed: %s (code %d)", strings.TrimSpace(result.Msg), result.Code)
	}
	meetingID := strings.TrimSpace(result.Data.Meeting.ID)
	if meetingID == "" {
		return meetingJoinOutcome{}, errors.New("join meeting response is missing meeting.id")
	}
	outcome := meetingJoinOutcome{MeetingID: meetingID}
	if c.meetingActivity != nil {
		c.meetingActivity.RegisterJoin(meetingID, strings.TrimSpace(request.MeetingNo), "", strings.TrimSpace(request.CallID))
		go c.meetingActivity.Backfill(context.Background(), meetingID, true)
	}
	if greeting := c.renderMeetingGreeting(); greeting != "" {
		if err := c.SendMeetingText(ctx, meetingID, greeting); err != nil {
			outcome.GreetingWarning = safeMeetingInviteError(err)
		} else {
			outcome.GreetingSent = true
		}
	}
	return outcome, nil
}

func (c *larkClient) SendMeetingText(ctx context.Context, meetingID, text string) error {
	return c.SendMeetingMessage(ctx, meetingID, text, "")
}

func (c *larkClient) SendMeetingMessage(ctx context.Context, meetingID, text, uuid string) error {
	body := map[string]any{
		"meeting_id": strings.TrimSpace(meetingID),
		"msg_type":   "text",
		"content":    strings.TrimSpace(text),
	}
	if uuid = strings.TrimSpace(uuid); uuid != "" {
		body["uuid"] = uuid
	}
	resp, err := c.api.Post(ctx, meetingInviteMessageAPIPath, body, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("send meeting greeting returned an empty response")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return meetingAPIResponseError("send meeting greeting", resp)
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return fmt.Errorf("decode meeting greeting response: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("send meeting greeting failed: %s (code %d)", strings.TrimSpace(result.Msg), result.Code)
	}
	if c.meetingActivity != nil {
		c.meetingActivity.RecordBotMessage(strings.TrimSpace(meetingID), strings.TrimSpace(text), strings.TrimSpace(uuid))
	}
	return nil
}

func (c *larkClient) renderMeetingGreeting() string {
	template := strings.TrimSpace(c.meetingGreeting)
	if template == "" {
		return ""
	}
	botName := strings.TrimSpace(c.botName)
	if botName == "" {
		botName = strings.TrimSpace(c.agentName)
	}
	if botName == "" {
		botName = strings.TrimSpace(c.channelName)
	}
	if botName == "" {
		botName = "AgentMux"
	}
	agentName := strings.TrimSpace(c.agentName)
	if agentName == "" {
		agentName = strings.TrimSpace(c.channelName)
	}
	if agentName == "" {
		agentName = botName
	}
	replacer := strings.NewReplacer(
		"{{bot_name}}", botName,
		"{{agent_name}}", agentName,
		"{{channel_name}}", strings.TrimSpace(c.channelName),
	)
	return strings.TrimSpace(replacer.Replace(template))
}

func meetingAPIResponseError(action string, resp *larkcore.ApiResp) error {
	if resp == nil {
		return fmt.Errorf("%s returned an empty response", action)
	}
	var result struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		Error struct {
			LogID string `json:"log_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err == nil && (result.Code != 0 || strings.TrimSpace(result.Msg) != "") {
		message := strings.TrimSpace(result.Msg)
		if result.Code == 99991672 {
			message = "missing required app scope " + meetingMessageWriteScope
		}
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		detail := fmt.Sprintf("%s failed: %s", action, message)
		if result.Code != 0 {
			detail += fmt.Sprintf(" (code %d)", result.Code)
		}
		if logID := strings.TrimSpace(result.Error.LogID); logID != "" {
			detail += ", log_id " + logID
		}
		return errors.New(detail)
	}
	return fmt.Errorf("%s returned HTTP %d", action, resp.StatusCode)
}

func (c *larkClient) MeetingInvitations() []core.MeetingInvitation {
	if c.meetingInvites == nil {
		return []core.MeetingInvitation{}
	}
	invites := c.meetingInvites.PendingInvitations()
	out := make([]core.MeetingInvitation, 0, len(invites))
	for _, invite := range invites {
		out = append(out, meetingInvitationView(invite))
	}
	return out
}

func (c *larkClient) RespondMeetingInvitation(
	ctx context.Context,
	invitationID, nonce, decision string,
) (core.MeetingInvitation, error) {
	if c.meetingInvites == nil {
		return core.MeetingInvitation{}, errors.New("meeting invitation controller is unavailable")
	}
	invite, err := c.meetingInvites.RespondFromConsole(ctx, invitationID, nonce, decision)
	return meetingInvitationView(invite), err
}

func (c *larkClient) JoinMeetingDirect(ctx context.Context, meetingNumber string) (core.MeetingJoinResult, error) {
	meetingNumber = strings.TrimSpace(meetingNumber)
	if !isNineDigitMeetingNumber(meetingNumber) {
		return core.MeetingJoinResult{}, errors.New("meeting number must be exactly 9 digits")
	}
	outcome, err := c.JoinMeeting(ctx, meetingJoinRequest{MeetingNo: meetingNumber})
	if err != nil {
		return core.MeetingJoinResult{}, err
	}
	c.MeetingInviteJoined(outcome.MeetingID, "", "")
	return core.MeetingJoinResult{
		MeetingID: outcome.MeetingID, MeetingNumber: meetingNumber,
		GreetingSent: outcome.GreetingSent, GreetingWarning: outcome.GreetingWarning,
	}, nil
}

func meetingInvitationView(invite meetingInvite) core.MeetingInvitation {
	return core.MeetingInvitation{
		ID: invite.ID, Nonce: invite.Nonce,
		MeetingID: invite.JoinedMeetingID, MeetingNumber: invite.MeetingNo,
		Topic: invite.Topic, InviterName: invite.InviterName, State: string(invite.State),
		LastError: invite.LastError, GreetingSent: invite.GreetingSent, GreetingWarning: invite.GreetingWarning,
		CreatedAt: invite.CreatedAt, ExpiresAt: invite.ExpiresAt,
	}
}

func buildMeetingInviteCard(invite meetingInvite) string {
	headerTemplate := "blue"
	headerTag := "待确认"
	headerTagColor := "blue"
	statusBlock := map[string]any(nil)

	switch invite.State {
	case meetingInviteJoining:
		headerTag = "加入中"
		statusBlock = meetingInviteStatusBlock("blue-50", "**正在加入会议，请稍候…**")
	case meetingInviteJoined:
		headerTemplate = "green"
		headerTag = "已加入"
		headerTagColor = "green"
		content := "**Bot 已加入会议**"
		if invite.JoinedMeetingID != "" {
			content += "\n<font color='grey'>会议 ID：" + escapeMeetingInviteMarkdown(invite.JoinedMeetingID) + "</font>"
		}
		statusBlock = meetingInviteStatusBlock("green-50", content)
	case meetingInviteRejected:
		headerTemplate = "red"
		headerTag = "已拒绝"
		headerTagColor = "red"
		statusBlock = meetingInviteStatusBlock("red-50", "**已拒绝邀请**\n本次不会加入会议。")
	case meetingInviteExpired:
		headerTemplate = "grey"
		headerTag = "已失效"
		headerTagColor = "neutral"
		statusBlock = meetingInviteStatusBlock("grey-50", "**邀请已失效**\nBot 未加入会议。")
	}

	infoContent := "**" + escapeMeetingInviteMarkdown(invite.Topic) + "**\n" +
		"<font color='grey'>会议号：</font>" + escapeMeetingInviteMarkdown(invite.MeetingNo) + "\n" +
		"<font color='grey'>邀请人：</font>" + escapeMeetingInviteMarkdown(invite.InviterName)
	infoBlock := map[string]any{
		"tag":              "column_set",
		"flex_mode":        "none",
		"background_style": "blue-50",
		"columns": []map[string]any{
			{
				"tag": "column", "width": "weighted", "weight": 1,
				"padding": "12px", "vertical_spacing": "4px",
				"elements": []map[string]any{
					{"tag": "markdown", "content": infoContent},
				},
			},
		},
	}

	elements := []map[string]any{infoBlock}
	if statusBlock != nil {
		elements = append(elements, statusBlock)
	} else {
		if invite.LastError != "" {
			elements = append(elements, meetingInviteStatusBlock(
				"red-50",
				"**加入失败，可以重试**\n<font color='grey'>"+escapeMeetingInviteMarkdown(invite.LastError)+"</font>",
			))
		}
		elements = append(elements, meetingInviteButtonBlock(invite))
	}

	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":   true,
			"width_mode":     "default",
			"enable_forward": false,
			"summary": map[string]string{
				"content": meetingInviteSummary(invite.State),
			},
		},
		"header": map[string]any{
			"title":    map[string]string{"tag": "plain_text", "content": "会议邀请"},
			"template": headerTemplate,
			"icon":     map[string]string{"tag": "standard_icon", "token": "calendar_colorful"},
			"text_tag_list": []map[string]any{
				{
					"tag":   "text_tag",
					"text":  map[string]string{"tag": "plain_text", "content": headerTag},
					"color": headerTagColor,
				},
			},
		},
		"body": map[string]any{
			"direction":        "vertical",
			"padding":          "12px 12px 20px 12px",
			"vertical_spacing": "12px",
			"elements":         elements,
		},
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		return `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"会议邀请"},"template":"blue"},"body":{"elements":[{"tag":"markdown","content":"收到会议邀请"}]}}`
	}
	return string(encoded)
}

func meetingInviteStatusBlock(background, content string) map[string]any {
	return map[string]any{
		"tag":              "column_set",
		"flex_mode":        "none",
		"background_style": background,
		"columns": []map[string]any{
			{
				"tag": "column", "width": "weighted", "weight": 1,
				"padding": "12px", "elements": []map[string]any{
					{"tag": "markdown", "content": content},
				},
			},
		},
	}
}

func meetingInviteButtonBlock(invite meetingInvite) map[string]any {
	button := func(label, buttonType, decision string) map[string]any {
		return map[string]any{
			"tag": "button", "type": buttonType, "width": "fill",
			"text": map[string]string{"tag": "plain_text", "content": label},
			"behaviors": []map[string]any{
				{
					"type": "callback",
					"value": map[string]any{
						modelPickerActionKey: meetingInviteAction,
						"invite_id":          invite.ID,
						"nonce":              invite.Nonce,
						"decision":           decision,
					},
				},
			},
		}
	}
	return map[string]any{
		"tag": "column_set", "flex_mode": "none", "horizontal_spacing": "8px",
		"columns": []map[string]any{
			{
				"tag": "column", "width": "weighted", "weight": 1,
				"elements": []map[string]any{button("加入会议", "primary_filled", meetingInviteDecisionJoin)},
			},
			{
				"tag": "column", "width": "weighted", "weight": 1,
				"elements": []map[string]any{button("拒绝", "danger", meetingInviteDecisionReject)},
			},
		},
	}
}

func escapeMeetingInviteMarkdown(value string) string {
	value = html.EscapeString(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		`\`, "&#92;",
		"`", "&#96;",
		"*", "&#42;",
		"_", "&#95;",
		"~", "&#126;",
		"[", "&#91;",
		"]", "&#93;",
		"(", "&#40;",
		")", "&#41;",
		"#", "&#35;",
	)
	return replacer.Replace(value)
}

func meetingInviteSummary(state meetingInviteState) string {
	switch state {
	case meetingInviteJoining:
		return "正在加入会议"
	case meetingInviteJoined:
		return "Bot 已加入会议"
	case meetingInviteRejected:
		return "已拒绝会议邀请"
	case meetingInviteExpired:
		return "会议邀请已失效"
	default:
		return "收到会议邀请，等待确认"
	}
}
