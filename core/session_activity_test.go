package core

import (
	"io"
	"log/slog"
	"testing"
)

func TestSessionActivityCountsLiveSessionsByOperatorNeed(t *testing.T) {
	engine := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), NewHookRunner(nil, nil))
	waiting := &channelSessionBinding{active: 1}
	running := &channelSessionBinding{active: 1}
	draining := &channelSessionBinding{active: 1, retired: true}
	engine.channels = map[string]*channelRuntime{
		"feishu": {
			sessions: map[string]*channelSessionBinding{
				"conv-waiting": waiting,
				"conv-running": running,
				"conv-idle":    {},
				"conv-closed":  {closed: true},
			},
			retiredSessions: map[*channelSessionBinding]struct{}{draining: {}},
			controlTasks: map[string]*channelControlState{
				"key-waiting": {active: &runtimeChannelTask{task: ChannelTask{Status: ChannelTaskWaitingInput}}},
				"key-running": {active: &runtimeChannelTask{task: ChannelTask{Status: ChannelTaskRunning}}},
			},
			directTurns: map[string]*directChannelTurn{},
		},
		"telegram": {
			sessions: map[string]*channelSessionBinding{"conv-a": {}, "conv-b": {}},
			// A queued turn that has not acquired its session yet is still work.
			controlTasks: map[string]*channelControlState{
				"key-queued": {queue: []*runtimeChannelTask{{task: ChannelTask{Status: ChannelTaskQueued}}}},
			},
			directTurns: map[string]*directChannelTurn{},
		},
	}

	got := engine.SessionActivity()
	want := SessionActivity{NeedsInput: 1, Working: 3, Idle: 3}
	if got != want {
		t.Fatalf("SessionActivity() = %+v, want %+v", got, want)
	}
}

func TestSessionActivityDoesNotDoubleCountDirectTurns(t *testing.T) {
	rt := &channelRuntime{
		sessions: map[string]*channelSessionBinding{"conv": {active: 1}},
		controlTasks: map[string]*channelControlState{
			"chat": {active: &runtimeChannelTask{task: ChannelTask{Status: ChannelTaskRunning}}},
		},
		directTurns: map[string]*directChannelTurn{"chat": {}, "other": {}},
	}
	got := rt.sessionActivity()
	want := SessionActivity{Working: 2}
	if got != want {
		t.Fatalf("sessionActivity() = %+v, want %+v", got, want)
	}
}

func TestSessionActivityEmptyEngine(t *testing.T) {
	engine := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), NewHookRunner(nil, nil))
	if got := engine.SessionActivity(); got != (SessionActivity{}) {
		t.Fatalf("SessionActivity() = %+v, want zero", got)
	}
}
