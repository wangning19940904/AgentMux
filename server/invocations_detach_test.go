package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// waitingInvoker runs turns that wait for an answer or cancellation.
type waitingInvoker struct {
	answers chan string
	ended   chan error
	started chan bool
}

func (w *waitingInvoker) Invoke(context.Context, core.InvocationRequest) (core.InvocationResult, error) {
	return core.InvocationResult{}, core.ErrInvocationRuntime
}

func (w *waitingInvoker) InvokeStream(ctx context.Context, _ core.InvocationRequest, sink core.InvocationEventSink) (core.InvocationResult, error) {
	w.started <- true
	_ = sink(core.InvocationStreamEvent{Type: "started", InvocationID: "inv_7", ConversationID: "conv_7"})
	select {
	case answer := <-w.answers:
		result := core.InvocationResult{ID: "inv_7", ConversationID: "conv_7", Answer: answer}
		_ = sink(core.InvocationStreamEvent{Type: "completed", InvocationID: "inv_7", Final: true, Result: &result})
		w.ended <- nil
		return result, nil
	case <-ctx.Done():
		w.ended <- ctx.Err()
		return core.InvocationResult{ID: "inv_7"}, ctx.Err()
	}
}

func TestDetachedInvocationSurvivesItsClientAndIsRejoined(t *testing.T) {
	s, _ := newTestServer(t)
	defer s.liveTurns.Close()
	invoker := &waitingInvoker{answers: make(chan string), ended: make(chan error, 1), started: make(chan bool, 1)}
	s.SetInvoker(invoker)
	server := httptest.NewServer(s.mux)
	defer server.Close()

	ctx, leave := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/invocations/stream?detach=1",
		strings.NewReader(`{"agent_id":"agent-1","input":"work"}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := bufio.NewReader(response.Body).ReadString('\n')
	if first != "id: 1\n" || !<-invoker.started {
		t.Fatalf("first line = %q", first)
	}
	leave()
	response.Body.Close()
	select {
	case err := <-invoker.ended:
		t.Fatalf("the turn ended with its client: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	rejoined, err := http.Get(server.URL + "/api/v1/invocations/inv_7/events?after=1")
	if err != nil {
		t.Fatal(err)
	}
	defer rejoined.Body.Close()
	invoker.answers <- "finished"
	body, _ := io.ReadAll(rejoined.Body)
	if !strings.Contains(string(body), `"answer":"finished"`) || strings.Contains(string(body), "event: started") {
		t.Fatalf("rejoined body = %s", body)
	}

	if recorder := doJSON(t, s, http.MethodGet, "/api/v1/invocations/inv_7/events?after=x", nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad after: %d", recorder.Code)
	}
	if recorder := doJSON(t, s, http.MethodPost, "/api/v1/invocations/inv_missing/cancel", nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown: %d", recorder.Code)
	}
}

func TestDetachedInvocationIsCancelledByID(t *testing.T) {
	s, _ := newTestServer(t)
	defer s.liveTurns.Close()
	invoker := &waitingInvoker{answers: make(chan string), ended: make(chan error, 1), started: make(chan bool, 1)}
	s.SetInvoker(invoker)
	server := httptest.NewServer(s.mux)
	defer server.Close()
	ctx, leave := context.WithCancel(context.Background())
	defer leave()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/invocations/stream?detach=1",
		strings.NewReader(`{"agent_id":"agent-1","input":"work"}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	<-invoker.started
	if recorder := doJSON(t, s, http.MethodPost, "/api/v1/invocations/inv_7/cancel", nil); recorder.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", recorder.Code, recorder.Body.String())
	}
	select {
	case err := <-invoker.ended:
		if err == nil {
			t.Fatal("the turn finished instead of being cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach the turn")
	}
}
