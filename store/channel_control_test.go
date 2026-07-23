package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

func TestChannelTaskRecoveryDoesNotReplayStartedTasks(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "control.db"))
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
	st, err := Open(filepath.Join(t.TempDir(), "interaction.db"))
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
	st, err := Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task := core.ChannelTask{
		ID: "task-controller", ChannelID: "c1", ConversationKey: "chat:one",
		ControllerID: "member", Status: core.ChannelTaskRunning,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateChannelTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	task.ControllerID = "admin"
	if err := st.UpdateChannelTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.ListChannelTasks(ctx, "c1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ControllerID != "admin" {
		t.Fatalf("tasks = %+v", tasks)
	}
}
