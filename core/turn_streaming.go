package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// streamTurn submits text to a session and forwards output through reply
// (when non-nil). It returns the last answer text and the first error event.
func (e *Engine) streamTurn(ctx context.Context, sess AgentSession, text string, reply func(string), data map[string]string) (string, error) {
	events, err := e.observeSend(ctx, sess, text, data)
	if err != nil {
		e.log.Error("send to session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		if reply != nil {
			reply("failed: " + err.Error())
		}
		return "", err
	}

	return e.consumeTurn(ctx, sess, events, reply, data)
}

// consumeTurn drains a session event channel, forwarding textual output through
// reply (deduplicated) and surfacing errors. It returns the last answer text
// and the first error event.
func (e *Engine) consumeTurn(ctx context.Context, sess AgentSession, events <-chan *Event, reply func(string), data map[string]string) (string, error) {
	var lastText string
	var lastReply string
	var firstErr error
	for ev := range events {
		e.updateRemoteTaskFromEvent(data, ev)
		switch ev.Type {
		case EventPermission:
			if e.resolveGuardInteraction(ctx, sess, ev, data) {
				continue
			}
			if !e.dispatchAgentInteraction(ctx, ev, data) {
				e.declineAgentInteraction(ctx, sess, ev)
			}
		case EventToolUse:
			// This path posts a new message per event, so only surface the
			// tool invocation itself (not results) as a compact progress note.
			if reply != nil && ev.ToolName != "" {
				note := "🔧 " + ev.ToolName
				if ev.ToolInput != "" {
					note += " " + ev.ToolInput
				}
				reply(note)
			}
		case EventFinal, EventOutput:
			if ev.Text != "" && ev.Text != "NO_REPLY" {
				lastText = ev.Text
				if reply != nil && ev.Text != lastReply {
					reply(ev.Text)
					lastReply = ev.Text
				}
			}
		case EventError:
			e.emit(ctx, HookError, withError(data, ev.Err))
			if firstErr == nil {
				firstErr = fmt.Errorf("%s", errString(ev.Err))
			}
			if reply != nil {
				reply("error: " + errString(ev.Err))
			}
		}
	}
	return lastText, firstErr
}

// streamTurnMessage drives a single turn onto a StreamMessageReplier,
// rendering the whole answer as one in-place updating plain-text message.
func (e *Engine) streamTurnMessage(ctx context.Context, mr StreamMessageReplier, sess AgentSession, msg *Message, data map[string]string) {
	events, err := e.observeSend(ctx, sess, msg.Text, data)
	if err != nil {
		e.log.Error("send to session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		deliveryErr := e.emitMessageStreamOnce(ctx, mr, msg, "failed: "+err.Error(), true)
		e.recordChannelDeliveryAttempt(data, deliveryErr, deliveryErr != nil)
		return
	}

	stream, err := mr.BeginMessageReply(ctx, msg)
	if err != nil {
		e.log.Error("begin streaming message reply", "err", err)
		var reply func(string)
		var deliveryErr error
		if p, ok := mr.(Platform); ok {
			reply = func(text string) {
				if rerr := p.Reply(ctx, msg, text); rerr != nil {
					deliveryErr = rerr
					e.log.Error("channel reply", "err", rerr)
				}
			}
		}
		answer, _ := e.consumeTurn(ctx, sess, events, reply, data)
		if deliveryErr != nil {
			e.recordChannelDeliveryAttempt(data, deliveryErr, true)
		} else if answer != "" {
			e.recordChannelDeliveryAttempt(data, nil, false)
		}
		return
	}
	defer func() { _ = stream.Close(ctx) }()

	speech := e.beginSpeechReply(ctx, mr, msg)
	if speech != nil {
		defer func() { _ = speech.Close(ctx) }()
	}
	e.driveReplyStream(ctx, sess, stream, speech, events, data)
}

// streamTurnCard drives a single turn onto a StreamReplier, rendering the whole
// answer as one in-place updating message (a Feishu card). It updates the same
// message as the agent streams output and marks it done/failed at the end. On
// any streaming setup failure it degrades to a single final update.
func (e *Engine) streamTurnCard(ctx context.Context, sr StreamReplier, sess AgentSession, msg *Message, data map[string]string) {
	events, err := e.observeSend(ctx, sess, msg.Text, data)
	if err != nil {
		e.log.Error("send to session", "err", err)
		e.emit(ctx, HookError, withError(data, err))
		deliveryErr := e.emitCardOnce(ctx, sr, msg, "failed: "+err.Error(), true)
		e.recordChannelDeliveryAttempt(data, deliveryErr, deliveryErr != nil)
		return
	}

	var stream ReplyStream
	if task, found := e.activeChannelTask(data); found {
		if feedbackReplier, ok := sr.(FeedbackTaskStreamReplier); ok {
			stream, err = feedbackReplier.BeginFeedbackTaskReply(ctx, msg, task)
		} else if taskReplier, ok := sr.(TaskStreamReplier); ok {
			stream, err = taskReplier.BeginTaskReply(ctx, msg, task.ID)
		}
	}
	if stream == nil && err == nil {
		stream, err = sr.BeginReply(ctx, msg)
	}
	if err != nil {
		e.log.Error("begin streaming reply", "err", err)
		// Degrade to per-event replies using the platform's Reply (the
		// StreamReplier is always also a Platform).
		var reply func(string)
		var deliveryErr error
		if p, ok := sr.(Platform); ok {
			reply = func(text string) {
				if rerr := p.Reply(ctx, msg, text); rerr != nil {
					deliveryErr = rerr
					e.log.Error("channel reply", "err", rerr)
				}
			}
		}
		answer, _ := e.consumeTurn(ctx, sess, events, reply, data)
		if deliveryErr != nil {
			e.recordChannelDeliveryAttempt(data, deliveryErr, true)
		} else if answer != "" {
			e.recordChannelDeliveryAttempt(data, nil, false)
		}
		return
	}
	defer func() { _ = stream.Close(ctx) }()
	if strings.TrimSpace(data["task_id"]) != "" {
		if err := stream.Update(ctx, "正在处理…", false, false); err != nil {
			e.log.Error("initialize task stream", "err", err)
		}
	}

	speech := e.beginSpeechReply(ctx, sr, msg)
	if speech != nil {
		defer func() { _ = speech.Close(ctx) }()
	}
	e.driveReplyStream(ctx, sess, stream, speech, events, data)
}

func (e *Engine) beginSpeechReply(ctx context.Context, renderer any, msg *Message) SpeechReply {
	replier, ok := renderer.(SpeechReplier)
	if !ok {
		return nil
	}
	speech, err := replier.BeginSpeechReply(ctx, msg)
	if err != nil {
		e.log.Warn("begin speech reply", "err", err)
		return nil
	}
	return speech
}

func (e *Engine) driveReplyStream(ctx context.Context, sess AgentSession, stream ReplyStream, speech SpeechReply, events <-chan *Event, data map[string]string) {
	var answer, completedAnswer, thinking, rendered string
	var failed bool
	var answerAfterLastTool bool
	var modelRetrying bool
	clearModelRetry := func() {
		if modelRetrying {
			thinking = ""
		}
		modelRetrying = false
	}
	var tools toolProgress
	for ev := range events {
		e.updateRemoteTaskFromEvent(data, ev)
		switch ev.Type {
		case EventModelResponse:
			if ev.Err != nil && ev.Metadata["will_retry"] == "true" {
				modelRetrying = true
				thinking = "模型服务请求失败，正在重试…"
				body := tools.render(thinking, answer, false)
				if body != rendered {
					if err := stream.Update(ctx, body, false, false); err != nil {
						e.log.Error("stream update", "err", err)
					}
					rendered = body
				}
			} else if ev.Err == nil {
				clearModelRetry()
			}
		case EventPermission:
			if !e.dispatchAgentInteraction(ctx, ev, data) {
				e.declineAgentInteraction(ctx, sess, ev)
			} else {
				thinking = "等待审批或补充信息…"
				body := tools.render(thinking, answer, false)
				if body != rendered {
					if err := stream.Update(ctx, body, false, false); err != nil {
						e.log.Error("stream update", "err", err)
					}
					rendered = body
				}
			}
		case EventThinking:
			clearModelRetry()
			if ev.Text == "" {
				continue
			}
			thinking = ev.Text
			body := tools.render(thinking, answer, false)
			if body != rendered {
				if err := stream.Update(ctx, body, false, false); err != nil {
					e.log.Error("stream update", "err", err)
				}
				rendered = body
			}
		case EventToolUse:
			clearModelRetry()
			answerAfterLastTool = false
			completedAnswer = ""
			// Native adapters provide ToolCallID, allowing parallel and
			// out-of-order results to close the correct rendered step.
			if ev.ToolName != "" {
				tools.addWithID(ev.ToolCallID, ev.ToolName, ev.ToolInput)
			} else if ev.ToolResult != "" || ev.Err != nil {
				tools.attachResultForID(ev.ToolCallID, ev.ToolResult, ev.Err != nil)
			}
			body := tools.render(thinking, answer, false)
			if body != rendered {
				if err := stream.Update(ctx, body, false, false); err != nil {
					e.log.Error("stream update", "err", err)
				}
				rendered = body
			}
		case EventFinal, EventOutput:
			clearModelRetry()
			if ev.Text == "" || ev.Text == "NO_REPLY" {
				continue
			}
			answerAfterLastTool = true
			// Cursor sometimes reconnects after a complete answer and emits short
			// acknowledgements before the process hits WritableIterable-is-closed.
			// Retain the most informative post-tool answer for that narrow recovery
			// path while continuing to render the newest answer normally.
			if len(strings.TrimSpace(ev.Text)) > len(strings.TrimSpace(completedAnswer)) {
				completedAnswer = ev.Text
			}
			answer = ev.Text
			if speech != nil {
				if err := speech.Update(ctx, answer, false); err != nil {
					e.log.Warn("speech update", "err", err)
				}
			}
			body := tools.render(thinking, answer, false)
			if body != rendered {
				if err := stream.Update(ctx, body, false, false); err != nil {
					e.log.Error("stream update", "err", err)
				}
				rendered = body
			}
		case EventError:
			if ctx.Err() != nil && e.remoteTaskStopRequested(data) {
				thinking = ""
				answer = "任务已停止。"
				continue
			}
			if shouldPreserveCompletedCursorAnswer(ev, completedAnswer, answerAfterLastTool, tools) {
				// Cursor can emit WriteIterableClosedError after the assistant has
				// already completed every tool and produced its final user-facing
				// answer. Preserve that answer instead of replacing a successful
				// operation with a red transport-error card. The original EventError
				// still passes through the observation wrapper before it reaches this
				// renderer, and this warning keeps the local runtime symptom visible.
				answer = strings.TrimSpace(completedAnswer)
				e.log.Warn("ignored cursor stream close after completed answer", "err", ev.Err)
				continue
			}
			e.emit(ctx, HookError, withError(data, ev.Err))
			failed = true
			answer = "error: " + errString(ev.Err)
		}
	}

	if ctx.Err() != nil && e.remoteTaskStopRequested(data) {
		answer = "任务已停止。"
		thinking = ""
		tools = toolProgress{}
		failed = false
	} else if ctx.Err() == context.DeadlineExceeded {
		answer = "任务执行超时，已自动终止。可能是模型服务连接异常或任务耗时过长，请稍后重试；如反复出现，请检查该机器的网络、代理配置和运行日志。"
		if modelRetrying {
			answer = "任务执行超时，已自动终止。模型服务请求持续失败，请检查该机器的网络、代理配置或模型服务状态后重试。"
		}
		thinking = ""
		failed = true
	}
	if answer == "" && tools.empty() && thinking == "" {
		answer = "(no reply)"
	}
	finalCtx := ctx
	cancelFinal := func() {}
	if ctx.Err() != nil {
		finalCtx, cancelFinal = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	}
	defer cancelFinal()
	if err := e.finalizeReplyStream(finalCtx, stream, tools.render(thinking, answer, true), failed, data); err != nil {
		e.log.Error("stream finalize", "err", err)
	}
	if speech != nil && !failed && answer != "" && answer != "NO_REPLY" {
		if err := speech.Update(ctx, answer, true); err != nil {
			e.log.Warn("speech finalize", "err", err)
		}
	}
}

func (e *Engine) finalizeReplyStream(ctx context.Context, stream ReplyStream, body string, failed bool, data map[string]string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = stream.Update(ctx, body, true, failed)
		if lastErr == nil {
			e.recordChannelDeliveryAttempt(data, nil, false)
			return nil
		}
		final := attempt == 2
		e.recordChannelDeliveryAttempt(data, lastErr, final)
		if final {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			e.recordChannelDeliveryAttempt(data, ctx.Err(), true)
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (e *Engine) remoteTaskStopRequested(data map[string]string) bool {
	if data == nil || data["channel_id"] == "" || data["conversation_key"] == "" || data["task_id"] == "" {
		return false
	}
	rt := e.channelRuntime(data["channel_id"])
	if rt == nil {
		return false
	}
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	state := rt.controlStateLocked(data["conversation_key"])
	if state.active != nil && state.active.task.ID == data["task_id"] && state.active.stopRequested {
		return true
	}
	direct := rt.directTurns[data["conversation_key"]]
	return direct != nil && direct.task != nil && direct.task.ID == data["task_id"] && direct.stopRequested
}

func shouldPreserveCompletedCursorAnswer(ev *Event, answer string, answerAfterLastTool bool, tools toolProgress) bool {
	if ev == nil || ev.Err == nil || ev.Metadata["runtime"] != "cursor" {
		return false
	}
	if strings.TrimSpace(answer) == "" || !answerAfterLastTool || !tools.settledSuccessfully() {
		return false
	}
	return strings.Contains(strings.ToLower(ev.Err.Error()), "writableiterable is closed")
}

func (e *Engine) emitMessageStreamOnce(ctx context.Context, mr StreamMessageReplier, msg *Message, text string, failed bool) error {
	stream, err := mr.BeginMessageReply(ctx, msg)
	if err != nil {
		e.log.Error("begin streaming message reply", "err", err)
		return err
	}
	defer func() { _ = stream.Close(ctx) }()
	if err := stream.Update(ctx, text, true, failed); err != nil {
		e.log.Error("stream finalize", "err", err)
		return err
	}
	return nil
}

// emitCardOnce sends a single terminal streaming message, used when the turn
// fails before any events could be produced.
func (e *Engine) emitCardOnce(ctx context.Context, sr StreamReplier, msg *Message, text string, failed bool) error {
	stream, err := sr.BeginReply(ctx, msg)
	if err != nil {
		e.log.Error("begin streaming reply", "err", err)
		return err
	}
	defer func() { _ = stream.Close(ctx) }()
	if err := stream.Update(ctx, text, true, failed); err != nil {
		e.log.Error("stream finalize", "err", err)
		return err
	}
	return nil
}
