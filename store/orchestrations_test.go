package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestOrchestrationPersistence(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "orchestrations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	orchestration := core.Orchestration{
		ID: "orch-one", Name: "Review", Status: core.OrchestrationQueued, MaxConcurrency: 2,
		CreatedAt: now, UpdatedAt: now,
		Tasks: []core.OrchestrationTask{
			{ID: "research", AgentID: "agent-one", Input: "research", Status: core.OrchestrationQueued, CreatedAt: now, UpdatedAt: now},
			{ID: "review", AgentID: "reviewer", Input: "review", DependsOn: []string{"research"}, Status: core.OrchestrationQueued, CreatedAt: now, UpdatedAt: now},
		},
	}
	if err := st.CreateOrchestration(context.Background(), orchestration); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetOrchestration(context.Background(), orchestration.ID)
	if err != nil || loaded == nil || len(loaded.Tasks) != 2 || loaded.Tasks[1].DependsOn[0] != "research" {
		t.Fatalf("loaded orchestration = %+v err=%v", loaded, err)
	}
	loaded.Status = core.OrchestrationRunning
	loaded.StartedAt, loaded.UpdatedAt = now, now.Add(time.Second)
	if err := st.UpdateOrchestration(context.Background(), *loaded); err != nil {
		t.Fatal(err)
	}
	task := loaded.Tasks[0]
	task.OrchestrationID = loaded.ID
	task.Status = core.OrchestrationSucceeded
	task.Output = "result"
	task.InvocationID = "inv-one"
	task.ConversationID = "conv-one"
	task.FinishedAt, task.UpdatedAt = now.Add(time.Second), now.Add(time.Second)
	if err := st.UpdateOrchestrationTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.GetOrchestration(context.Background(), orchestration.ID)
	if err != nil || loaded.Tasks[0].Status != core.OrchestrationSucceeded || loaded.Tasks[0].Output != "result" || loaded.Tasks[0].InvocationID != "inv-one" {
		t.Fatalf("updated orchestration = %+v err=%v", loaded, err)
	}
	active, err := st.ListOrchestrations(context.Background(), true, 10)
	if err != nil || len(active) != 1 || active[0].ID != orchestration.ID {
		t.Fatalf("active orchestrations = %+v err=%v", active, err)
	}
}
