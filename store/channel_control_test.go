package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestChannelTaskRecoveryDoesNotReplayStartedTasks(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, task := range []core.ChannelTask{
		{
			ID: "queued", ChannelID: "c1", ConversationKey: "thread:one",
			ChatID: "oc_group", MessageID: "om_reply", ChatType: "group",
			RootID: "om_root", ThreadID: "omt_thread",
			Status: core.ChannelTaskQueued, Prompt: "safe to replay", CreatedAt: now, UpdatedAt: now,
		},
		{ID: "running", ChannelID: "c1", ConversationKey: "root:one", Status: core.ChannelTaskRunning, Prompt: "must not replay", CreatedAt: now, UpdatedAt: now},
		{ID: "waiting", ChannelID: "c1", ConversationKey: "root:one", Status: core.ChannelTaskWaitingInput, Prompt: "must not replay", CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.CreateChannelTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateChannelInteraction(ctx, core.ChannelInteraction{
		ID: "pending-on-restart", TaskID: "waiting", ChannelID: "c1",
		ConversationKey: "root:one", Nonce: "restart-nonce",
		Status:    core.ChannelInteractionPending,
		Request:   core.AgentInteraction{ID: "pending-on-restart", Kind: core.AgentInteractionCommandApproval},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.RecoverChannelTasks(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].ID != "queued" || recovered[0].Prompt != "safe to replay" {
		t.Fatalf("recovered = %+v", recovered)
	}
	if recovered[0].MessageID != "om_reply" || recovered[0].ThreadID != "omt_thread" || recovered[0].ChatType != "group" {
		t.Fatalf("recovered reply anchor = %+v", recovered[0])
	}
	all, err := st.ListChannelTasks(ctx, "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]core.ChannelTaskStatus{}
	for _, task := range all {
		status[task.ID] = task.Status
		if task.ID != "queued" && task.Prompt != "" {
			t.Fatalf("started task retained prompt: %+v", task)
		}
	}
	if status["running"] != core.ChannelTaskInterrupted || status["waiting"] != core.ChannelTaskInterrupted {
		t.Fatalf("recovered statuses = %+v", status)
	}
	interaction, err := st.GetChannelInteraction(ctx, "pending-on-restart")
	if err != nil {
		t.Fatal(err)
	}
	if interaction == nil || interaction.Status != core.ChannelInteractionExpired {
		t.Fatalf("recovered interaction = %+v, want expired", interaction)
	}
}

func TestChannelInteractionNonceIsSingleUse(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "interaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	record := core.ChannelInteraction{
		ID: "i1", TaskID: "t1", ChannelID: "c1", ConversationKey: "root:one",
		Nonce: "nonce-one", Status: core.ChannelInteractionPending,
		Request:   core.AgentInteraction{ID: "i1", Kind: core.AgentInteractionCommandApproval},
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateChannelInteraction(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateChannelInteractionMessage(ctx, "i1", "om_card_1"); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetChannelInteraction(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.MessageID != "om_card_1" {
		t.Fatalf("stored interaction = %+v", stored)
	}
	if ok, err := st.ResolveChannelInteraction(ctx, "i1", "wrong", "u1", core.ChannelInteractionResolved); err != nil || ok {
		t.Fatalf("wrong nonce resolved: ok=%t err=%v", ok, err)
	}
	if ok, err := st.ResolveChannelInteraction(ctx, "i1", "nonce-one", "u1", core.ChannelInteractionResolved); err != nil || !ok {
		t.Fatalf("first resolve: ok=%t err=%v", ok, err)
	}
	if ok, err := st.ResolveChannelInteraction(ctx, "i1", "nonce-one", "u1", core.ChannelInteractionResolved); err != nil || ok {
		t.Fatalf("replay resolved: ok=%t err=%v", ok, err)
	}
}

func TestChannelTaskControllerUpdateIsPersistent(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task := core.ChannelTask{
		ID: "task-controller", ChannelID: "c1", ConversationKey: "chat:one",
		ControllerID: "member", Status: core.ChannelTaskRunning,
		DeliveryKey: "turn:task-controller", DeliveryStatus: core.ChannelDeliveryPending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateChannelTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	task.ControllerID = "admin"
	task.DeliveryAttempts = 2
	task.DeliveryStatus = core.ChannelDeliverySent
	task.DeliveredAt = time.Now().UTC()
	if err := st.UpdateChannelTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.ListChannelTasks(ctx, "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ControllerID != "admin" || tasks[0].DeliveryKey != "turn:task-controller" || tasks[0].DeliveryStatus != core.ChannelDeliverySent || tasks[0].DeliveryAttempts != 2 || tasks[0].DeliveredAt.IsZero() {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestListLatestChannelTasksReturnsOneCompactRowPerConversation(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "latest-tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	tasks := []core.ChannelTask{
		{ID: "old", ChannelID: "c1", ConversationID: "conversation-1", ConversationKey: "root:one", Status: core.ChannelTaskRunning, Prompt: "large old prompt", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
		{ID: "new", ChannelID: "c1", ConversationID: "conversation-1", ConversationKey: "root:one", Status: core.ChannelTaskSucceeded, Prompt: "large new prompt", CreatedAt: now, UpdatedAt: now},
		{ID: "fallback", ChannelID: "c1", ConversationKey: "root:two", Status: core.ChannelTaskQueued, Prompt: "another prompt", CreatedAt: now.Add(-time.Second), UpdatedAt: now.Add(-time.Second)},
	}
	for _, task := range tasks {
		if err := st.CreateChannelTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := st.ListLatestChannelTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 || latest[0].ID != "new" || latest[1].ID != "fallback" {
		t.Fatalf("latest tasks = %+v", latest)
	}
	if latest[0].Prompt != "" || latest[0].Status != core.ChannelTaskSucceeded {
		t.Fatalf("latest task should contain only list status metadata: %+v", latest[0])
	}
}

func TestChannelFeedbackRequiresSuccessfulDeliveredTaskAndOwner(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "feedback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task := core.ChannelTask{
		ID: "task-feedback", ChannelID: "channel-one", ConversationID: "conversation-one",
		ConversationKey: "root:one", UserID: "owner-one", Status: core.ChannelTaskSucceeded,
		DeliveryKey: "turn:task-feedback", DeliveryStatus: core.ChannelDeliverySent,
		FeedbackNonce: "feedback-nonce", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateChannelTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		user  string
		nonce string
		want  bool
	}{
		"wrong user":  {user: "intruder", nonce: "feedback-nonce", want: false},
		"wrong nonce": {user: "owner-one", nonce: "wrong", want: false},
		"owner":       {user: "owner-one", nonce: "feedback-nonce", want: true},
	} {
		t.Run(name, func(t *testing.T) {
			feedback := core.ChannelFeedback{ID: "feedback-" + name, TaskID: task.ID, UserID: tc.user, Semantic: core.FeedbackPositive}
			recorded, err := st.SubmitChannelFeedback(ctx, feedback, tc.nonce)
			if err != nil || recorded != tc.want {
				t.Fatalf("recorded=%t err=%v want=%t", recorded, err, tc.want)
			}
		})
	}
	feedback := core.ChannelFeedback{ID: "feedback-revision", TaskID: task.ID, UserID: "owner-one", Semantic: core.FeedbackNegative}
	if recorded, err := st.SubmitChannelFeedback(ctx, feedback, "feedback-nonce"); err != nil || !recorded {
		t.Fatalf("revision recorded=%t err=%v", recorded, err)
	}
	items, err := st.ListChannelFeedback(ctx, "channel-one", task.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Semantic != core.FeedbackNegative || items[0].ConversationID != "conversation-one" {
		t.Fatalf("feedback items = %+v", items)
	}
	if updated, err := st.UpdateChannelFeedbackDetail(ctx, items[0].ID, "wrong_result", "Details"); err != nil || !updated {
		t.Fatalf("update detail=%t err=%v", updated, err)
	}
	items, _ = st.ListChannelFeedback(ctx, "channel-one", task.ID, 10)
	if items[0].Reason != "wrong_result" || items[0].Comment != "Details" {
		t.Fatalf("feedback detail = %+v", items[0])
	}
}
