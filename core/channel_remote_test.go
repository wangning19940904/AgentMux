package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type remoteControlTestAgent struct {
	openedThread string
	opened       bool
	fallback     string
	openErr      error
	starts       int
	session      AgentSession
}

func (a *remoteControlTestAgent) Name() string { return "codex" }
func (a *remoteControlTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.starts++
	if a.session != nil {
		return a.session, nil
	}
	return &fakeSession{id: "remote-test"}, nil
}
func (a *remoteControlTestAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *remoteControlTestAgent) Stop(context.Context) error                     { return nil }
func (a *remoteControlTestAgent) OpenNativeThread(_ context.Context, threadID string) (bool, string, error) {
	a.openedThread = threadID
	return a.opened, a.fallback, a.openErr
}

type remoteInteractiveTestSession struct {
	*fakeSession
	steerErr error
}

func (s *remoteInteractiveTestSession) Steer(context.Context, string) error { return s.steerErr }
func (s *remoteInteractiveTestSession) Interrupt(context.Context) error     { return nil }
func (s *remoteInteractiveTestSession) ResolveInteraction(context.Context, string, AgentInteractionResponse) error {
	return nil
}
func (s *remoteInteractiveTestSession) ActiveTurnID() string { return "turn-test" }

type remoteBlockingTestSession struct {
	started chan string
	release chan struct{}
}

func (s *remoteBlockingTestSession) ID() string { return "blocking-session" }
func (s *remoteBlockingTestSession) Send(ctx context.Context, text string) (<-chan *Event, error) {
	s.started <- text
	out := make(chan *Event, 1)
	go func() {
		defer close(out)
		select {
		case <-s.release:
			out <- &Event{Type: EventFinal, TurnID: "turn-blocking", Text: "done", Final: true}
		case <-ctx.Done():
			out <- &Event{Type: EventError, TurnID: "turn-blocking", Err: ctx.Err()}
		}
	}()
	return out, nil
}
func (s *remoteBlockingTestSession) RespondPermission(context.Context, bool) error { return nil }
func (s *remoteBlockingTestSession) Close(context.Context) error                   { return nil }

func newRemoteControlTestRuntime(engine *Engine) (*channelRuntime, *fakePlatform) {
	platform := newFakePlatform("feishu")
	runtime := &channelRuntime{
		owner: engine,
		channel: Channel{
			ID: "channel-1", Type: "feishu",
			Config: map[string]string{
				ChannelConfigCodexControlEnabled: "true",
			},
		},
		platform:     platform,
		agent:        &remoteControlTestAgent{},
		sessions:     map[string]*channelSessionBinding{},
		seen:         map[string]time.Time{},
		controlTasks: map[string]*channelControlState{},
		clearConfirm: map[string]time.Time{},
		threadLists:  map[string][]NativeThread{},
	}
	engine.channels[runtime.channel.ID] = runtime
	return runtime, platform
}

func TestRemoteQueueClearOnlyCancelsControllersOwnTasks(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	state := runtime.controlStateLocked("chat:one")
	state.queue = []*runtimeChannelTask{
		{task: ChannelTask{ID: "own", ControllerID: "member", Status: ChannelTaskQueued}},
		{task: ChannelTask{ID: "other", ControllerID: "other", Status: ChannelTaskQueued}},
		{task: ChannelTask{ID: "admin", ControllerID: "admin", Status: ChannelTaskQueued}},
	}
	msg := &Message{ChatID: "one", ConversationKey: "chat:one", UserID: "member"}

	engine.clearRemoteQueue(context.Background(), runtime, msg, map[string]string{}, false)
	engine.clearRemoteQueue(context.Background(), runtime, msg, map[string]string{}, true)

	if len(state.queue) != 2 {
		t.Fatalf("queue length = %d, want 2", len(state.queue))
	}
	for _, task := range state.queue {
		if task.task.ID == "own" {
			t.Fatal("member's own queued task was not cancelled")
		}
	}
}

func TestRemoteTaskTakeoverRequiresAdminAndIsAudited(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	state := runtime.controlStateLocked("chat:one")
	state.active = &runtimeChannelTask{task: ChannelTask{
		ID: "task-1", ConversationKey: "chat:one", ControllerID: "member", Status: ChannelTaskRunning,
	}}
	var events []HookEvent
	engine.SetEventSink(func(event HookEvent, _ map[string]string) { events = append(events, event) })

	engine.takeOverRemoteTask(context.Background(), runtime,
		&Message{ChatID: "one", ConversationKey: "chat:one", UserID: "other"}, map[string]string{})
	if state.active.task.ControllerID != "member" {
		t.Fatalf("non-admin changed controller to %q", state.active.task.ControllerID)
	}

	runtime.platform = &modeTestPlatform{fakePlatform: runtime.platform.(*fakePlatform), admin: true}
	engine.takeOverRemoteTask(context.Background(), runtime,
		&Message{ChatID: "one", ChatType: "group", ConversationKey: "chat:one", UserID: "admin"}, map[string]string{})
	if state.active.task.ControllerID != "admin" {
		t.Fatalf("controller = %q, want admin", state.active.task.ControllerID)
	}
	found := false
	for _, event := range events {
		if event == HookTaskTakenOver {
			found = true
		}
	}
	if !found {
		t.Fatal("task takeover lifecycle event was not emitted")
	}
}

func TestRemoteMessageUsesNextTurnAfterAgentGenerationChanges(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, platform := newRemoteControlTestRuntime(engine)
	generation := &channelAgentGeneration{}
	generation.retired.Store(true)
	state := runtime.controlStateLocked("chat:one")
	state.active = &runtimeChannelTask{
		task: ChannelTask{
			ID: "task-old-generation", ConversationKey: "chat:one", ControllerID: "member", Status: ChannelTaskRunning,
		},
		session:    &remoteInteractiveTestSession{fakeSession: &fakeSession{id: "old-session"}},
		generation: generation,
	}

	engine.handleChannelMessage(context.Background(), &Message{
		ID: "message-new-generation", ChannelID: runtime.channel.ID, ChatID: "one",
		ConversationKey: "chat:one", UserID: "member", Text: "continue with new skills",
	}, map[string]string{"channel_id": runtime.channel.ID, "conversation_key": "chat:one"})

	runtime.controlMu.Lock()
	queued := append([]*runtimeChannelTask(nil), state.queue...)
	runtime.controlMu.Unlock()
	if len(queued) != 1 || queued[0].msg.Text != "continue with new skills" {
		t.Fatalf("queued tasks = %#v", queued)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.replies) != 1 || !strings.Contains(platform.replies[0], "已排队") {
		t.Fatalf("replies = %#v", platform.replies)
	}
}

func TestRemoteTaskCardStopIsScopedToItsTaskID(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, platform := newRemoteControlTestRuntime(engine)
	state := runtime.controlStateLocked("chat:one")
	taskCtx, cancelTask := context.WithCancel(context.Background())
	defer cancelTask()
	state.active = &runtimeChannelTask{
		task: ChannelTask{
			ID: "current-task", ConversationKey: "chat:one", ControllerID: "member", Status: ChannelTaskRunning,
		},
		cancel: cancelTask,
	}
	data := map[string]string{"channel_id": "channel-1", "conversation_key": "chat:one"}

	engine.handleChannelMessage(context.Background(), &Message{
		ChannelID: "channel-1", ChatID: "one", ConversationKey: "chat:one", UserID: "other",
		ChannelTaskAction: &ChannelTaskAction{TaskID: "current-task", Action: ChannelTaskActionStop},
	}, data)
	select {
	case <-taskCtx.Done():
		t.Fatal("a non-controller stopped the current task")
	default:
	}
	if state.active.stopRequested {
		t.Fatal("a non-controller marked the current task for stopping")
	}

	engine.handleChannelMessage(context.Background(), &Message{
		ChannelID: "channel-1", ChatID: "one", ConversationKey: "chat:one", UserID: "member",
		ChannelTaskAction: &ChannelTaskAction{TaskID: "old-task", Action: ChannelTaskActionStop},
	}, data)
	select {
	case <-taskCtx.Done():
		t.Fatal("a stale card stopped the current task")
	default:
	}
	if state.active.stopRequested {
		t.Fatal("a stale card marked the current task for stopping")
	}

	engine.handleChannelMessage(context.Background(), &Message{
		ChannelID: "channel-1", ChatID: "one", ConversationKey: "chat:one", UserID: "member",
		ChannelTaskAction: &ChannelTaskAction{TaskID: "current-task", Action: ChannelTaskActionStop},
	}, data)
	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("the matching task card did not cancel the task context")
	}
	if !state.active.stopRequested {
		t.Fatal("the matching task was not marked for stopping")
	}

	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.replies) != 3 || !strings.Contains(platform.replies[0], "控制人或管理员") ||
		!strings.Contains(platform.replies[1], "已结束") || !strings.Contains(platform.replies[2], "已请求停止") {
		t.Fatalf("stop replies = %#v", platform.replies)
	}
}

type finalContextReplyStream struct {
	text      string
	done      bool
	failed    bool
	ctxErr    error
	callCount int
}

func (s *finalContextReplyStream) Update(ctx context.Context, text string, done, failed bool) error {
	s.callCount++
	if done {
		s.text = text
		s.done = done
		s.failed = failed
		s.ctxErr = ctx.Err()
	}
	return nil
}

func (s *finalContextReplyStream) Close(context.Context) error { return nil }

func TestStoppedRemoteTaskFinalizesCardAfterTaskContextCancellation(t *testing.T) {
	for _, withErrorEvent := range []bool{false, true} {
		t.Run(fmt.Sprintf("error_event_%t", withErrorEvent), func(t *testing.T) {
			engine := NewEngine(nil, NewHookRunner(nil, nil))
			runtime, _ := newRemoteControlTestRuntime(engine)
			runtime.controlStateLocked("chat:one").active = &runtimeChannelTask{
				task: ChannelTask{ID: "task-1", ConversationKey: "chat:one"}, stopRequested: true,
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			events := make(chan *Event, 1)
			if withErrorEvent {
				events <- &Event{Type: EventError, Err: context.Canceled}
			}
			close(events)
			stream := &finalContextReplyStream{}

			engine.driveReplyStream(ctx, nil, stream, nil, events, map[string]string{
				"channel_id": "channel-1", "conversation_key": "chat:one", "task_id": "task-1",
			})

			if !stream.done || stream.failed || stream.ctxErr != nil || stream.text != "任务已停止。" {
				t.Fatalf("final stopped card = %+v", stream)
			}
		})
	}
}

func TestRemoteTaskErrorMarksActiveTaskFailedCandidate(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	state := runtime.controlStateLocked("chat:one")
	state.active = &runtimeChannelTask{task: ChannelTask{
		ID: "task-1", ConversationKey: "chat:one", Status: ChannelTaskRunning,
	}}

	engine.emit(context.Background(), HookError, map[string]string{
		"channel_id": "channel-1", "conversation_key": "chat:one",
		"task_id": "task-1", "error": "native turn failed",
	})

	if state.active.runErr != "native turn failed" {
		t.Fatalf("runErr = %q", state.active.runErr)
	}
}

func TestOpenRemoteThreadUsesNativeOpener(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, platform := newRemoteControlTestRuntime(engine)
	opener := runtime.agent.(*remoteControlTestAgent)
	opener.opened = true
	state := runtime.controlStateLocked("chat:one")
	state.active = &runtimeChannelTask{task: ChannelTask{
		ID: "task-1", ConversationKey: "chat:one", NativeThreadID: "thread-123",
	}}

	engine.openRemoteThread(context.Background(), runtime,
		&Message{ChatID: "one", ConversationKey: "chat:one", UserID: "member"}, map[string]string{})

	if opener.openedThread != "thread-123" {
		t.Fatalf("opened thread = %q", opener.openedThread)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.replies) != 1 || platform.replies[0] != "已在本机 Codex App 中打开当前 thread。" {
		t.Fatalf("replies = %#v", platform.replies)
	}
}

func TestChannelMessagesRespectReplyScopeWithoutAccessLists(t *testing.T) {
	for _, tc := range []struct {
		name, scope, chatType            string
		mentioned, mentionAll, wantReply bool
	}{
		{name: "ordinary group", chatType: "group"},
		{name: "ordinary topic group", chatType: "topic_group"},
		{name: "mention everyone", chatType: "group", mentionAll: true},
		{name: "bot mention", chatType: "group", mentioned: true, wantReply: true},
		{name: "private chat", chatType: "p2p", wantReply: true},
		{name: "all group messages", scope: ReplyScopeAll, chatType: "group", wantReply: true},
		{name: "mentions only group", scope: ReplyScopeMentionsOnly, chatType: "group"},
		{name: "mentions only private", scope: ReplyScopeMentionsOnly, chatType: "p2p"},
		{name: "mentions only bot mention", scope: ReplyScopeMentionsOnly, chatType: "group", mentioned: true, wantReply: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine(nil, NewHookRunner(nil, nil))
			runtime, platform := newRemoteControlTestRuntime(engine)
			runtime.channel.Config[ChannelConfigReplyScope] = tc.scope
			// Legacy values cannot restrict users delivered by the platform.
			runtime.channel.Config["allowed_user_ids"] = "old-owner"
			runtime.channel.Config["admin_user_ids"] = "old-owner"
			msg := &Message{
				ID: "message", ChannelID: runtime.channel.ID, Platform: "feishu",
				ChatID: "chat", ChatType: tc.chatType, UserID: "new-user", Text: "/status",
				MentionedBot: tc.mentioned, MentionAll: tc.mentionAll,
			}

			engine.handle(context.Background(), msg)

			wantReplies := 0
			if tc.wantReply {
				wantReplies = 1
			}
			if len(platform.replies) != wantReplies {
				t.Fatalf("replies = %#v, want %d", platform.replies, wantReplies)
			}
			if tc.wantReply && !strings.Contains(platform.replies[0], "任务状态：空闲") {
				t.Fatalf("reply = %q", platform.replies[0])
			}

		})
	}
}

func TestNewChannelUserCanQueueTaskWithLegacyAccessLists(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	runtime.channel.Config["allowed_user_ids"] = "old-owner"
	runtime.channel.Config["admin_user_ids"] = "old-owner"
	state := runtime.controlStateLocked("chat:one")
	state.active = &runtimeChannelTask{task: ChannelTask{
		ID: "active", ConversationKey: "chat:one", ControllerID: "old-owner", Status: ChannelTaskRunning,
	}}
	engine.handle(context.Background(), &Message{
		ID: "new-message", ChannelID: runtime.channel.ID, ChatID: "one", ChatType: "group",
		UserID: "new-user", Text: "help with this task", MentionedBot: true,
	})
	if len(state.queue) != 1 || state.queue[0].task.UserID != "new-user" || state.queue[0].task.Prompt != "help with this task" {
		t.Fatalf("new user's task was not admitted: %+v", state.queue)
	}
}

func TestRemoteFollowupQueuesWithoutAutomaticSteer(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	state := runtime.controlStateLocked("chat:one")
	state.active = &runtimeChannelTask{
		task: ChannelTask{
			ID: "active", ConversationKey: "chat:one", ControllerID: "member", Status: ChannelTaskRunning,
		},
		session: &remoteInteractiveTestSession{
			fakeSession: &fakeSession{id: "active-session"},
			steerErr:    errors.New("steer unavailable"),
		},
	}

	engine.handleChannelMessage(context.Background(), &Message{
		ChannelID: "channel-1", ChatID: "one", ConversationKey: "chat:one",
		UserID: "member", Text: "follow-up",
	}, map[string]string{"channel_id": "channel-1", "conversation_key": "chat:one"})

	if len(state.queue) != 1 || state.queue[0].task.ControllerID != "member" {
		t.Fatalf("queued tasks = %+v", state.queue)
	}
}

func TestRemoteQueueLimitIsEnforced(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	runtime.channel.Config[ChannelConfigCodexMaxQueue] = "1"
	msg := &Message{ChatID: "one", ConversationKey: "chat:one", UserID: "member"}
	first, err := engine.enqueueRemoteTask(context.Background(), runtime, msg, "first")
	if err != nil {
		t.Fatal(err)
	}
	if first.msg.Text != "first" {
		t.Fatalf("queued message text = %q", first.msg.Text)
	}
	if _, err := engine.enqueueRemoteTask(context.Background(), runtime, msg, "second"); err == nil {
		t.Fatal("queue accepted a task beyond its configured limit")
	}
}

func TestQueueClearCommandIsNotEnqueuedAsPrompt(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	state := runtime.controlStateLocked("chat:one")
	state.queue = []*runtimeChannelTask{{task: ChannelTask{
		ID: "queued", ConversationKey: "chat:one", ControllerID: "member", Status: ChannelTaskQueued,
	}}}
	msg := &Message{ChatID: "one", ConversationKey: "chat:one", UserID: "member"}

	if !engine.handleRemoteCommand(context.Background(), runtime, msg, map[string]string{}, "/queue clear") {
		t.Fatal("/queue clear was not handled")
	}
	if len(state.queue) != 1 || state.queue[0].task.ID != "queued" {
		t.Fatalf("queue clear command changed queue before confirmation: %+v", state.queue)
	}
	if runtime.clearConfirm["chat:one:member"].IsZero() {
		t.Fatal("queue clear confirmation was not recorded")
	}
}

func TestQueueCommandStartsImmediatelyWhenConversationIsIdle(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	runtime, _ := newRemoteControlTestRuntime(engine)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.runCtx = ctx
	runtime.workDir = t.TempDir()
	blocking := &remoteBlockingTestSession{started: make(chan string, 1), release: make(chan struct{})}
	runtime.agent.(*remoteControlTestAgent).session = blocking
	msg := &Message{
		ID: "om_queue", ChannelID: "channel-1", ChatID: "one", ChatType: "p2p",
		ConversationKey: "chat:one", UserID: "member", Text: "/queue do work",
	}

	if !engine.handleRemoteCommand(ctx, runtime, msg,
		map[string]string{"channel_id": "channel-1", "conversation_key": "chat:one"}, msg.Text) {
		t.Fatal("/queue command was not handled")
	}
	select {
	case prompt := <-blocking.started:
		if !strings.Contains(prompt, `"text":"do work"`) || strings.Contains(prompt, "/queue do work") {
			t.Fatalf("queued prompt = %q", prompt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle queued task did not start")
	}
	close(blocking.release)
}
