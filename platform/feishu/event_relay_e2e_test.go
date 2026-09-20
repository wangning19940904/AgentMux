package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/eventrelay"
	"github.com/wangning19940904/AgentMux/store"
)

// Optional cross-language integration test. The supplied Python must have the
// local SDK, FastAPI and uvicorn installed. All processes and databases are local
// disposable fixtures; no actual Feishu app is contacted.
func TestRelayFastAPIEndToEnd(t *testing.T) {
	python := os.Getenv("AGENTMUX_EVENT_TEST_PYTHON")
	if python == "" {
		t.Skip("set AGENTMUX_EVENT_TEST_PYTHON for the FastAPI receiver integration")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(fixture, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if err := st.UpsertChannel(ctx, &core.Channel{ID: "shared", Name: "shared", Type: "feishu", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ports := make([]string, 2)
	for i := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
		listener.Close()
	}
	start := func(i int) func() {
		logFile, err := os.Create(filepath.Join(fixture, fmt.Sprintf("receiver-%d.log", i)))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(python, "-m", "uvicorn", "receiver:app", "--host", "127.0.0.1", "--port", ports[i], "--log-level", "warning")
		command.Dir = filepath.Join(root, "integrations/homebook-events")
		command.Env = append(os.Environ(), "AGENTMUX_EVENT_SECRET=secret", "EVENT_INBOX_PATH="+filepath.Join(fixture, fmt.Sprintf("inbox-%d.db", i)), "PYTHONPATH="+filepath.Join(root, "sdk/python/src"))
		command.Stdout = logFile
		command.Stderr = logFile
		if err := command.Start(); err != nil {
			logFile.Close()
			t.Fatal(err)
		}
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			command.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				command.Process.Kill()
				<-done
			}
			logFile.Close()
		}
		t.Cleanup(stop)
		ready := false
		client := &http.Client{Timeout: 200 * time.Millisecond}
		for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
			response, err := client.Get("http://127.0.0.1:" + ports[i] + "/openapi.json")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					ready = true
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !ready {
			stop()
			body, _ := os.ReadFile(logFile.Name())
			t.Fatalf("receiver failed: %s", body)
		}
		return stop
	}
	stopFirst := start(0)
	start(1)
	for i := range ports {
		sub := core.EventSubscription{Key: fmt.Sprintf("consumer-%d", i), Name: "consumer", Source: "feishu", SourceRefs: []string{"channel:shared"}, EventTypes: []string{"drive.file.bitable_record_changed_v1"}, CallbackURL: "http://127.0.0.1:" + ports[i] + "/api/v1/integrations/agentmux/events", Secret: "secret"}
		if _, err := st.SaveEventSubscription(ctx, &sub); err != nil {
			t.Fatal(err)
		}
	}
	relay := eventrelay.New(st, nil, nil)
	if err := relay.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer relay.Stop()
	client := &larkClient{platform: "feishu", appID: "app", botOpenID: "bot", eventIngress: core.PlatformEventIngress{Publish: func(ctx context.Context, event core.RelayEvent) error {
		event.SourceRef = "channel:shared"
		return relay.Publish(ctx, event)
	}}}
	handler := client.eventDispatcher(ctx, "unused", make(chan *core.Message, 1), []string{"drive.file.bitable_record_changed_v1"})
	emit := func(id string) {
		body, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]string{"event_type": "drive.file.bitable_record_changed_v1", "event_id": id}, "event": map[string]any{"file_token": "file", "table_id": "table", "revision": 1}})
		if _, err := handler.Do(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	await := func(sent, retry int) {
		for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
			items, err := st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			a, b := 0, 0
			for _, item := range items {
				if item.Status == "sent" {
					a++
				}
				if item.Status == "retry" {
					b++
				}
			}
			if a == sent && b == retry {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		items, _ := st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
		t.Fatalf("expected sent=%d retry=%d; deliveries=%+v", sent, retry, items)
	}
	emit("first")
	await(2, 0)
	stopFirst()
	emit("second")
	await(3, 1)
	start(0)
	await(4, 0)
	emit("second")
	items, _ := st.ListEventDeliveries(ctx, nil, "", "", 100, 0)
	if len(items) != 4 {
		t.Fatal("duplicate upstream event was fanned out again")
	}
	for i := range ports {
		command := exec.Command(python, "-c", "import sqlite3,sys; print(sqlite3.connect(sys.argv[1]).execute('SELECT COUNT(*) FROM inbox').fetchone()[0])", filepath.Join(fixture, fmt.Sprintf("inbox-%d.db", i)))
		output, err := command.Output()
		if err != nil || strings.TrimSpace(string(output)) != "2" {
			t.Fatalf("durable inbox %d: %s %v", i, output, err)
		}
	}
}
