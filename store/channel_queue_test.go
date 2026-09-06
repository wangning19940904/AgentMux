package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestChannelQueueControlsAndModesSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	st, err := OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetChannelChatState(ctx, "channel", "mode:dm", "thread"); err != nil {
		t.Fatal(err)
	}
	task := core.ChannelTask{ID: "queued", ChannelID: "channel", ConversationKey: "root:seed", ChatID: "dm", SourceMessageID: "original", MessageID: "seed", Status: core.ChannelTaskQueued, Prompt: "next", ControlCardID: "card", ControlNonce: "nonce", TargetTaskID: "active", ChatMode: "chat", ReplyInThread: true, CreatedAt: time.Now().UTC()}
	if err = st.CreateChannelTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	task.ID = "uncertain"
	task.Status = core.ChannelTaskSteering
	if err = st.CreateChannelTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = OpenLegacySQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	value, err := st.GetChannelChatState(ctx, "channel", "mode:dm")
	if err != nil || value != "thread" {
		t.Fatalf("mode=%s err=%v", value, err)
	}
	if exists, err := st.HasChannelSourceTask(ctx, "channel", "original"); err != nil || !exists {
		t.Fatal("lost source dedup identity", err)
	}
	tasks, err := st.RecoverChannelTasks(ctx, "channel")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("recovered=%+v err=%v", tasks, err)
	}
	got := tasks[0]
	if got.ControlCardID != "card" || got.ControlNonce != "nonce" || got.TargetTaskID != "active" || !got.ReplyInThread || got.Prompt != "next" {
		t.Fatalf("lost control metadata: %+v", got)
	}
	unknown, err := st.GetChannelTask(ctx, "uncertain")
	if err != nil || unknown.Status != core.ChannelTaskSteerUnknown || unknown.Prompt != "" {
		t.Fatalf("uncertain input replayable: %+v err=%v", unknown, err)
	}
}
