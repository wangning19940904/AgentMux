package feishu

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/wangning19940904/AgentMux/core"
	larkws "github.com/wangning19940904/AgentMux/internal/larkws"
)

func (c *larkClient) requestedEventTypes() []string {
	if c.eventIngress.EventTypes == nil {
		return nil
	}
	types := slices.Clone(c.eventIngress.EventTypes())
	slices.Sort(types)
	return slices.Compact(types)
}

// Listen replaces only the websocket when the immutable handler set changes.
// Agent sessions, meeting state and outbound API clients keep their identity.
func (c *larkClient) Listen(ctx context.Context, project string, inbound chan<- *core.Message) error {
	for ctx.Err() == nil {
		types := c.requestedEventTypes()
		handler := c.eventDispatcher(ctx, project, inbound, types)
		logger := &larkWSHealthLogger{client: c, delegate: larkcore.NewDefaultLogger(larkcore.LogLevelInfo)}
		ws := larkws.NewClient(c.appID, c.appSecret, larkws.WithDomain(c.domain), larkws.WithEventHandler(handler), larkws.WithLogger(logger),
			larkws.WithOnReady(c.markReady), larkws.WithOnReconnecting(c.markReconnecting), larkws.WithOnReconnected(func() {
				c.markReady()
				if c.meetingActivity != nil {
					go c.meetingActivity.Recover(ctx)
				}
			}), larkws.WithOnDisconnected(c.markDisconnected), larkws.WithOnError(c.markError))
		wsCtx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			cancel()
			return nil
		}
		c.ws = ws
		c.cancel = cancel
		c.mu.Unlock()
		errCh := make(chan error, 1)
		go func() { errCh <- ws.Start(wsCtx) }()
		ticker := time.NewTicker(time.Second)
		restart := false
		var runErr error
	wait:
		for {
			select {
			case <-wsCtx.Done():
				break wait
			case runErr = <-errCh:
				break wait
			case <-ticker.C:
				if !slices.Equal(types, c.requestedEventTypes()) {
					restart = true
					break wait
				}
			}
		}
		ticker.Stop()
		cancel()
		ws.Close()
		// Start returns on cancellation; never open the replacement before the old
		// connection is closed. Its remaining loops observe the same cancelled ctx.
		if runErr == nil {
			select {
			case <-errCh:
			case <-time.After(11 * time.Second):
			}
		}
		c.mu.Lock()
		if c.ws == ws {
			c.ws = nil
			c.cancel = nil
		}
		closing := c.closing
		c.mu.Unlock()
		if !restart || closing || ctx.Err() != nil {
			return runErr
		}
	}
	return nil
}

func (c *larkClient) forwardEvent(ctx context.Context, req *larkevent.EventReq) error {
	if req == nil || c.eventIngress.Publish == nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &raw); err != nil {
		return err
	}
	var header struct {
		EventID    string `json:"event_id"`
		EventType  string `json:"event_type"`
		CreateTime string `json:"create_time"`
	}
	_ = json.Unmarshal(raw["header"], &header)
	var data map[string]json.RawMessage
	_ = json.Unmarshal(raw["event"], &data)
	if header.EventType == "" {
		_ = json.Unmarshal(data["type"], &header.EventType)
		_ = json.Unmarshal(raw["uuid"], &header.EventID)
	}
	if header.EventType == "" || header.EventType == "app_ticket" {
		return nil
	}
	// Remove authentication metadata, preserving business tokens such as file_token.
	delete(raw, "token")
	delete(raw, "encrypt")
	if value, ok := raw["header"]; ok {
		var h map[string]json.RawMessage
		if json.Unmarshal(value, &h) == nil {
			delete(h, "token")
			raw["header"], _ = json.Marshal(h)
		}
	}
	if header.EventType == "card.action.trigger" {
		delete(data, "token")
		raw["event"], _ = json.Marshal(data)
	}
	attributes := map[string]string{"app_id": c.appID}
	for _, key := range []string{"chat_id", "file_token", "table_id"} {
		var value string
		if json.Unmarshal(data[key], &value) == nil && value != "" {
			attributes[key] = value
		}
	}
	var message struct {
		ChatID string `json:"chat_id"`
	}
	_ = json.Unmarshal(data["message"], &message)
	if message.ChatID != "" {
		attributes["chat_id"] = message.ChatID
	}
	if attributes["chat_id"] == "" {
		var cardContext struct {
			OpenChatID string `json:"open_chat_id"`
		}
		_ = json.Unmarshal(data["context"], &cardContext)
		if cardContext.OpenChatID != "" {
			attributes["chat_id"] = cardContext.OpenChatID
		}
	}
	occurred := time.Time{}
	if ms, err := strconv.ParseInt(header.CreateTime, 10, 64); err == nil {
		occurred = time.UnixMilli(ms).UTC()
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return c.eventIngress.Publish(bounded, core.RelayEvent{Source: c.platform, Type: header.EventType, SourceEventID: header.EventID, OccurredAt: occurred, Attributes: attributes, Data: body})
}
