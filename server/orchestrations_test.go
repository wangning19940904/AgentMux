package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestOrchestrationRunsDependenciesAndPassesOutputs(t *testing.T) {
	server, st := newTestServer(t)
	invoker := &orchestrationTestInvoker{}
	server.SetInvoker(invoker)
	recorder := doJSON(t, server, http.MethodPost, "/api/v1/orchestrations", map[string]any{
		"name": "parallel review", "max_concurrency": 2,
		"tasks": []map[string]any{
			{"id": "research", "agent_id": "agent-a", "input": "research code"},
			{"id": "test", "agent_id": "agent-b", "input": "run tests"},
			{"id": "synthesize", "project": "lead", "input": "make decision", "depends_on": []string{"research", "test"}},
		},
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("create code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created core.Orchestration
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	result := waitForOrchestration(t, st, created.ID)
	if result.Status != core.OrchestrationSucceeded || len(result.Tasks) != 3 {
		t.Fatalf("orchestration = %+v", result)
	}
	input := invoker.inputFor("orchestration:" + created.ID + ":synthesize")
	for _, want := range []string{`<dependency id="research">`, "result:research code", `<dependency id="test">`, "result:run tests", "Current task:\nmake decision"} {
		if !strings.Contains(input, want) {
			t.Fatalf("synthesis input missing %q: %s", want, input)
		}
	}
	if invoker.maxRunning > 2 {
		t.Fatalf("max running = %d, want <=2", invoker.maxRunning)
	}
}

func TestOrchestrationRejectsCycle(t *testing.T) {
	server, _ := newTestServer(t)
	server.SetInvoker(&orchestrationTestInvoker{})
	recorder := doJSON(t, server, http.MethodPost, "/api/v1/orchestrations", map[string]any{
		"tasks": []map[string]any{
			{"id": "a", "agent_id": "agent-a", "input": "a", "depends_on": []string{"b"}},
			{"id": "b", "agent_id": "agent-b", "input": "b", "depends_on": []string{"a"}},
		},
	})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "cycle") {
		t.Fatalf("cycle code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOrchestrationFailureBlocksDependentTask(t *testing.T) {
	server, st := newTestServer(t)
	server.SetInvoker(&orchestrationTestInvoker{failInput: "fail root"})
	recorder := doJSON(t, server, http.MethodPost, "/api/v1/orchestrations", map[string]any{
		"tasks": []map[string]any{
			{"id": "root", "agent_id": "agent-a", "input": "fail root"},
			{"id": "dependent", "agent_id": "agent-b", "input": "must not run", "depends_on": []string{"root"}},
		},
	})
	var created core.Orchestration
	_ = json.Unmarshal(recorder.Body.Bytes(), &created)
	result := waitForOrchestration(t, st, created.ID)
	statuses := map[string]core.OrchestrationStatus{}
	for _, task := range result.Tasks {
		statuses[task.ID] = task.Status
	}
	if result.Status != core.OrchestrationFailed || statuses["root"] != core.OrchestrationFailed || statuses["dependent"] != core.OrchestrationCancelled {
		t.Fatalf("failed orchestration = %+v", result)
	}
}

type orchestrationTestInvoker struct {
	mu         sync.Mutex
	inputs     map[string]string
	running    int
	maxRunning int
	failInput  string
}

func (i *orchestrationTestInvoker) Invoke(ctx context.Context, req core.InvocationRequest) (core.InvocationResult, error) {
	i.mu.Lock()
	if i.inputs == nil {
		i.inputs = map[string]string{}
	}
	i.inputs[req.ConversationID] = req.Input
	i.running++
	if i.running > i.maxRunning {
		i.maxRunning = i.running
	}
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		i.running--
		i.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return core.InvocationResult{}, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}
	if req.Input == i.failInput {
		return core.InvocationResult{}, errors.New("planned failure")
	}
	return core.InvocationResult{ID: "inv-" + req.ConversationID, ConversationID: req.ConversationID, Answer: "result:" + req.Input}, nil
}

func (i *orchestrationTestInvoker) inputFor(id string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.inputs[id]
}

type orchestrationGetter interface {
	GetOrchestration(context.Context, string) (*core.Orchestration, error)
}

func waitForOrchestration(t *testing.T, store orchestrationGetter, id string) *core.Orchestration {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		item, err := store.GetOrchestration(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if item != nil && item.Status != core.OrchestrationQueued && item.Status != core.OrchestrationRunning {
			return item
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("orchestration did not finish")
	return nil
}
