package cliagents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wangning19940904/AgentMux/core"
)

// The test binary doubles as a fake `codex`: with fakeCodexEnv set it
// answers --version and runs `app-server --listen unix://PATH`, a server that,
// like the real one, keeps a turn running when its client disconnects and
// lets a new client follow it after thread/resume. Its state directory holds
// the version it reports, a line per server start, the turns interrupted, and
// the release-<turn> files that let a turn finish.
const fakeCodexEnv = "AGENTMUX_FAKE_CODEX_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeCodexEnv); dir != "" {
		os.Exit(runFakeCodex(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runFakeCodex(dir string, args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		version, err := os.ReadFile(filepath.Join(dir, "version"))
		if err != nil {
			version = []byte("codex-cli fake-1")
		}
		fmt.Println(strings.TrimSpace(string(version)))
		return 0
	}
	if len(args) != 3 || args[0] != "app-server" || args[1] != "--listen" || !strings.HasPrefix(args[2], "unix://") {
		fmt.Fprintf(os.Stderr, "fake codex: unexpected arguments %q\n", args)
		return 2
	}
	sock := strings.TrimPrefix(args[2], "unix://")
	// Like Codex, listen on a short path and link the requested one to it.
	sum := sha256.Sum256([]byte(sock))
	target := filepath.Join(os.TempDir(), "amux-fake-"+hex.EncodeToString(sum[:6])+".sock")
	_ = os.Remove(target)
	listener, err := net.Listen("unix", target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Remove(target)
	_ = os.Remove(sock)
	if err := os.Symlink(target, sock); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	appendLine(filepath.Join(dir, "starts"), fmt.Sprint(os.Getpid()))
	server := &fakeAppServer{dir: dir, threads: map[string]*fakeThread{}}
	// A safety net for a test that dies before stopping it.
	time.AfterFunc(2*time.Minute, func() { os.Exit(0) })
	_ = http.Serve(listener, server)
	return 0
}

func appendLine(path, line string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line + "\n")
}

type fakeAppServer struct {
	dir     string
	mu      sync.Mutex
	threads map[string]*fakeThread
	turns   int
}

type fakeThread struct {
	id          string
	subscribers map[*fakeConn]bool
	turns       []*fakeTurn
}

type fakeTurn struct {
	id, status, input string
	answer            string
}

type fakeConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *fakeConn) send(message map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	message["jsonrpc"] = "2.0"
	_ = c.conn.WriteJSON(message)
}

func (s *fakeAppServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := &fakeConn{conn: ws}
	defer func() {
		s.mu.Lock()
		for _, thread := range s.threads {
			delete(thread.subscribers, conn)
		}
		s.mu.Unlock()
		_ = ws.Close()
	}()
	for {
		var request map[string]any
		if ws.ReadJSON(&request) != nil {
			return
		}
		id, hasID := request["id"]
		method, _ := request["method"].(string)
		params, _ := request["params"].(map[string]any)
		if !hasID {
			continue
		}
		result := s.handle(conn, method, params)
		conn.send(map[string]any{"id": id, "result": result})
		if method == "turn/start" {
			turn := nestedMap(result, "turn")
			go s.runTurn(firstString(params, "threadId"), firstString(turn, "id"))
		}
	}
}

func (s *fakeAppServer) handle(conn *fakeConn, method string, params map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread := s.threads[firstString(params, "threadId")]
	switch method {
	case "model/list":
		return map[string]any{"data": []any{map[string]any{"model": "fake-model"}}}
	case "thread/start":
		id := fmt.Sprintf("thr_%d", len(s.threads)+1)
		s.threads[id] = &fakeThread{id: id, subscribers: map[*fakeConn]bool{conn: true}}
		return map[string]any{"thread": map[string]any{"id": id}}
	case "thread/resume":
		if thread == nil {
			return map[string]any{}
		}
		thread.subscribers[conn] = true
		turns := []any{}
		for _, turn := range thread.turns {
			items := []any{}
			if turn.answer != "" {
				items = append(items, map[string]any{"type": "agentMessage", "id": "msg_" + turn.id, "text": turn.answer})
			}
			turns = append(turns, map[string]any{"id": turn.id, "status": turn.status, "items": items})
		}
		return map[string]any{"thread": map[string]any{"id": thread.id, "turns": turns}}
	case "turn/start":
		s.turns++
		turn := &fakeTurn{id: fmt.Sprintf("turn_%d", s.turns), status: "inProgress"}
		if input, _ := params["input"].([]any); len(input) > 0 {
			turn.input = firstString(input[0].(map[string]any), "text")
		}
		thread.turns = append(thread.turns, turn)
		return map[string]any{"turn": map[string]any{"id": turn.id, "status": "inProgress"}}
	case "turn/interrupt":
		appendLine(filepath.Join(s.dir, "interrupted"), firstString(params, "turnId"))
		for _, turn := range thread.turns {
			if turn.id == firstString(params, "turnId") && turn.status == "inProgress" {
				turn.status = "interrupted"
			}
		}
		return map[string]any{}
	case "thread/loaded/list":
		ids := []any{}
		for id := range s.threads {
			ids = append(ids, id)
		}
		return map[string]any{"data": ids}
	case "thread/read":
		status := "idle"
		for _, turn := range thread.turns {
			if turn.status == "inProgress" {
				status = "active"
			}
		}
		return map[string]any{"thread": map[string]any{"id": thread.id, "status": map[string]any{"type": status}}}
	}
	return map[string]any{}
}

// runTurn works on the turn until its release file appears, telling whoever
// is subscribed to the thread at the time.
func (s *fakeAppServer) runTurn(threadID, turnID string) {
	s.notify(threadID, "turn/started", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID}})
	s.notify(threadID, "item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "msg_" + turnID, "delta": "working"})
	for {
		if _, err := os.Stat(filepath.Join(s.dir, "release-"+turnID)); err == nil {
			break
		}
		s.mu.Lock()
		var status, input string
		for _, turn := range s.threads[threadID].turns {
			if turn.id == turnID {
				status, input = turn.status, turn.input
			}
		}
		s.mu.Unlock()
		if status == "interrupted" {
			s.notify(threadID, "turn/completed", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "interrupted"}})
			return
		}
		_ = input
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	answer := ""
	for _, turn := range s.threads[threadID].turns {
		if turn.id == turnID {
			turn.status, turn.answer = "completed", "done: "+turn.input
			answer = turn.answer
		}
	}
	s.mu.Unlock()
	s.notify(threadID, "item/completed", map[string]any{"threadId": threadID, "turnId": turnID, "item": map[string]any{"id": "msg_" + turnID, "type": "agentMessage", "text": answer}})
	s.notify(threadID, "turn/completed", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "completed"}})
}

func (s *fakeAppServer) notify(threadID, method string, params map[string]any) {
	s.mu.Lock()
	var conns []*fakeConn
	for conn := range s.threads[threadID].subscribers {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		conn.send(map[string]any{"method": method, "params": params})
	}
}

// fakeCodex points detached app-servers at a fresh root and returns the fake
// Codex's state directory and the agent configuration that uses it.
func fakeCodex(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	previous := codexServersRoot
	codexServersRoot = func() (string, error) { return root, nil }
	codexVersions.Clear()
	t.Cleanup(func() {
		codexServersRoot = previous
		codexVersions.Clear()
		configs, _ := os.ReadDir(root)
		for _, config := range configs {
			generations, _ := os.ReadDir(filepath.Join(root, config.Name()))
			for _, generation := range generations {
				stopCodexServer(filepath.Join(root, config.Name(), generation.Name()))
			}
		}
	})
	dir := t.TempDir()
	return dir, map[string]any{core.AgentConfigKeepTurns: true, "env": map[string]string{fakeCodexEnv: dir}}
}

func newFakeCodexAgent(t *testing.T, cfg map[string]any) *codexAgent {
	t.Helper()
	agent := newCodexAgent(cfg)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent.binary = executable
	return agent
}

func release(t *testing.T, dir, turnID string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "release-"+turnID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readLines(dir, name string) []string {
	data, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.Fields(string(data))
}

// keptTurnEvent waits for the turn's first event, which names it.
func keptTurnEvent(t *testing.T, events <-chan *core.Event) *core.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("the turn ended before it was named")
			}
			if event.TurnID != "" {
				if event.Metadata[core.EventMetadataKeptTurn] != "true" {
					t.Fatalf("a turn on a detached app-server is marked kept: %+v", event)
				}
				return event
			}
		case <-timeout:
			t.Fatal("timed out waiting for the turn to start")
		}
	}
}

func finalAnswer(t *testing.T, events <-chan *core.Event) (string, error) {
	t.Helper()
	answer := ""
	var turnErr error
	timeout := time.After(10 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return answer, turnErr
			}
			if event.Type == core.EventFinal {
				answer = event.Text
			}
			if event.Type == core.EventError && turnErr == nil {
				turnErr = event.Err
			}
		case <-timeout:
			t.Fatal("timed out waiting for the turn to end")
		}
	}
}

// startKeptTurn starts a turn whose owner, the AgentMux process, can end.
func startKeptTurn(t *testing.T, agent *codexAgent, owner context.Context) (*codexSession, context.CancelFunc, <-chan *core.Event, *core.Event) {
	t.Helper()
	session, err := agent.StartSession(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithCancel(owner)
	events, err := session.(core.RichAgentSession).SendInput(core.WithTurnDetach(runCtx, owner), core.AgentTurnInput{Text: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	return session.(*codexSession), stopRun, events, keptTurnEvent(t, events)
}

func TestCodexTurnOutlivesAgentMuxAndIsRejoined(t *testing.T) {
	dir, cfg := fakeCodex(t)
	owner, stopOwner := context.WithCancel(context.Background())
	first := newFakeCodexAgent(t, cfg)
	session, _, events, started := startKeptTurn(t, first, owner)
	threadID, turnID := session.ID(), started.TurnID

	// AgentMux stops mid-turn: its context ends, then its sessions and agents
	// close.
	stopOwner()
	for range events {
	}
	_ = session.Close(context.Background())
	_ = first.Stop(context.Background())
	if interrupted := readLines(dir, "interrupted"); len(interrupted) != 0 {
		t.Fatalf("shutting down must not interrupt a kept turn: %q", interrupted)
	}

	// The next process finds the same app-server and follows the turn.
	second := newFakeCodexAgent(t, cfg)
	defer second.Stop(context.Background())
	rejoined, rest, err := second.RejoinTurn(context.Background(), t.TempDir(), core.AgentTurnRef{SessionID: threadID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	defer rejoined.Close(context.Background())
	if active := rejoined.(core.InteractiveAgentSession).ActiveTurnID(); active != turnID {
		t.Fatalf("a rejoined turn can be steered: active turn = %q", active)
	}
	release(t, dir, turnID)
	answer, err := finalAnswer(t, rest)
	if err != nil || answer != "done: build it" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if starts := readLines(dir, "starts"); len(starts) != 1 {
		t.Fatalf("one app-server serves both processes: starts = %q", starts)
	}
}

func TestStoppingAKeptCodexTurnInterruptsIt(t *testing.T) {
	dir, cfg := fakeCodex(t)
	owner, stopOwner := context.WithCancel(context.Background())
	defer stopOwner()
	agent := newFakeCodexAgent(t, cfg)
	defer agent.Stop(context.Background())
	session, stopRun, events, started := startKeptTurn(t, agent, owner)
	defer session.Close(context.Background())
	// A Ticket stop ends the turn's context while AgentMux runs on.
	stopRun()
	if _, err := finalAnswer(t, events); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if interrupted := readLines(dir, "interrupted"); len(interrupted) != 1 || interrupted[0] != started.TurnID {
		t.Fatalf("a stopped turn is interrupted: %q", interrupted)
	}
}

func TestRejoiningACodexTurnThatFinishedOrIsGone(t *testing.T) {
	dir, cfg := fakeCodex(t)
	owner, stopOwner := context.WithCancel(context.Background())
	first := newFakeCodexAgent(t, cfg)
	session, _, events, started := startKeptTurn(t, first, owner)
	threadID, turnID := session.ID(), started.TurnID
	stopOwner()
	for range events {
	}
	_ = first.Stop(context.Background())
	// The turn finishes while AgentMux is away.
	release(t, dir, turnID)
	time.Sleep(100 * time.Millisecond)

	second := newFakeCodexAgent(t, cfg)
	defer second.Stop(context.Background())
	rejoined, rest, err := second.RejoinTurn(context.Background(), "", core.AgentTurnRef{SessionID: threadID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	if answer, err := finalAnswer(t, rest); err != nil || answer != "done: build it" {
		t.Fatalf("a finished turn gives its answer: answer=%q err=%v", answer, err)
	}
	_ = rejoined.Close(context.Background())

	for _, ref := range []core.AgentTurnRef{{SessionID: threadID, TurnID: "turn_9"}, {SessionID: "thr_9", TurnID: turnID}} {
		if _, _, err := second.RejoinTurn(context.Background(), "", ref); !errors.Is(err, core.ErrInvocationTurnGone) {
			t.Fatalf("%+v: err = %v", ref, err)
		}
	}
	// An agent that runs its own app-server keeps no turns.
	own := newFakeCodexAgent(t, map[string]any{"env": cfg["env"]})
	if _, _, err := own.RejoinTurn(context.Background(), "", core.AgentTurnRef{SessionID: threadID, TurnID: turnID}); !errors.Is(err, core.ErrInvocationTurnGone) {
		t.Fatalf("err = %v", err)
	}
}

func TestACodexUpdateRetiresTheOldAppServerOnceIdle(t *testing.T) {
	dir, cfg := fakeCodex(t)
	owner, stopOwner := context.WithCancel(context.Background())
	first := newFakeCodexAgent(t, cfg)
	session, _, events, started := startKeptTurn(t, first, owner)
	threadID, turnID := session.ID(), started.TurnID
	stopOwner()
	for range events {
	}
	_ = first.Stop(context.Background())

	// Codex is updated while the turn runs.
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte("codex-cli fake-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	codexVersions.Clear()
	second := newFakeCodexAgent(t, cfg)
	defer second.Stop(context.Background())
	if _, err := second.appClient(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if starts := readLines(dir, "starts"); len(starts) != 2 {
		t.Fatalf("new turns get an app-server of the new version: starts = %q", starts)
	}
	oldPid := readLines(dir, "starts")[0]
	if !fakeServerRunning(oldPid) {
		t.Fatal("the old app-server keeps its running turn")
	}
	rejoined, rest, err := second.RejoinTurn(context.Background(), "", core.AgentTurnRef{SessionID: threadID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	release(t, dir, turnID)
	if answer, err := finalAnswer(t, rest); err != nil || answer != "done: build it" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	_ = rejoined.Close(context.Background())

	// The next client of the new version stops the old one, now idle.
	third := newFakeCodexAgent(t, cfg)
	defer third.Stop(context.Background())
	if _, err := third.appClient(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for fakeServerRunning(oldPid) {
		if time.Now().After(deadline) {
			t.Fatal("the idle old app-server must be stopped")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if starts := readLines(dir, "starts"); len(starts) != 2 {
		t.Fatalf("starts = %q", starts)
	}
}

func fakeServerRunning(pid string) bool {
	var value int
	_, _ = fmt.Sscan(pid, &value)
	return processAlive(value)
}

// The answer of an in-progress turn record, used to seed a rejoined turn.
func TestCodexTurnAnswerTakesTheLastAgentMessage(t *testing.T) {
	var turn map[string]any
	_ = json.Unmarshal([]byte(`{"items":[{"type":"agentMessage","text":"first"},{"type":"commandExecution"},{"type":"agentMessage","text":"second"}]}`), &turn)
	if got := codexTurnAnswer(turn); got != "second" {
		t.Fatalf("answer = %q", got)
	}
}
