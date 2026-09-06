package cliagents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/agent/internal/runner"
)

// codexAppClient owns one long-lived app-server process for a Codex Agent.
// Calls are multiplexed by JSON-RPC id; notifications and server requests are
// routed to sessions by native thread id.
type codexAppClient struct {
	mu      sync.Mutex
	writeMu sync.Mutex

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	stderr bytes.Buffer

	nextID    int
	pending   map[int]chan codexRPCResponse
	sessions  map[string]*codexSession
	done      chan struct{}
	closed    bool
	closeOnce sync.Once
}

type codexRPCResponse struct {
	result map[string]any
	err    error
}

func newCodexAppClient(ctx context.Context, agent *codexAgent, workDir string) (*codexAppClient, error) {
	name := agent.binary
	if name == "" {
		name = "codex"
	}
	if path, err := exec.LookPath(name); err == nil {
		name = path
	}
	args := codexAppServerArgs(ctx)
	if agent.Name() == "traecli" {
		args = []string{"app-server", "--listen", "stdio://"}
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	cmd.Env = runner.BuildEnv(agent.env)
	client := &codexAppClient{
		cmd: cmd, nextID: 1, pending: map[int]chan codexRPCResponse{},
		sessions: map[string]*codexSession{}, done: make(chan struct{}),
	}
	cmd.Stderr = &client.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	client.stdin = stdin
	client.reader = bufio.NewReader(stdout)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go client.readLoop()
	if _, err := client.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "AgentMux", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		_ = client.close()
		return nil, client.withStderr(err)
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		_ = client.close()
		return nil, err
	}
	return client, nil
}

func (c *codexAppClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *codexAppClient) register(threadID string, session *codexSession) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("codex app-server is closed")
	}
	if existing := c.sessions[threadID]; existing != nil && existing != session {
		return fmt.Errorf("Codex thread %q is already active in another channel conversation", threadID)
	}
	c.sessions[threadID] = session
	return nil
}

func (c *codexAppClient) unregister(threadID string, session *codexSession) {
	c.mu.Lock()
	if c.sessions[threadID] == session {
		delete(c.sessions, threadID)
	}
	c.mu.Unlock()
}

func (c *codexAppClient) call(ctx context.Context, method string, params any) (map[string]any, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("codex app-server is closed")
	}
	id := c.nextID
	c.nextID++
	wait := make(chan codexRPCResponse, 1)
	c.pending[id] = wait
	c.mu.Unlock()
	if err := c.request(id, method, params); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case response := <-wait:
		return response.result, response.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, c.withStderr(fmt.Errorf("codex app-server stopped"))
	}
}

func (c *codexAppClient) request(id int, method string, params any) error {
	return c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (c *codexAppClient) notify(method string, params any) error {
	return c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *codexAppClient) writeMessage(message map[string]any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	stdin := c.stdin
	c.mu.Unlock()
	if closed || stdin == nil {
		return fmt.Errorf("codex app-server is closed")
	}
	_, err = stdin.Write(append(data, '\n'))
	return err
}

func (c *codexAppClient) readLoop() {
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			c.fail(err)
			return
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &message); err != nil {
			c.fail(err)
			return
		}
		method, _ := message["method"].(string)
		if method != "" {
			c.routeServerMessage(message)
			continue
		}
		id, ok := rpcID(message)
		if !ok {
			continue
		}
		c.mu.Lock()
		wait := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if wait != nil {
			result, _ := message["result"].(map[string]any)
			wait <- codexRPCResponse{result: result, err: rpcError(message)}
		}
	}
}

func (c *codexAppClient) routeServerMessage(message map[string]any) {
	params, _ := message["params"].(map[string]any)
	threadID := firstString(params, "threadId")
	if threadID == "" {
		threadID = firstString(nestedMap(params, "thread"), "id", "threadId")
	}
	c.mu.Lock()
	session := c.sessions[threadID]
	c.mu.Unlock()
	if session != nil {
		select {
		case session.inbox <- message:
		case <-c.done:
		}
		return
	}
	if id, ok := rpcID(message); ok {
		method, _ := message["method"].(string)
		result := any(map[string]string{"decision": "decline"})
		switch method {
		case "item/permissions/requestApproval", "permissions/requestApproval":
			result = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
		case "item/tool/requestUserInput":
			result = map[string]any{"answers": map[string]any{}}
		}
		_ = c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
}

func (c *codexAppClient) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[int]chan codexRPCResponse{}
	c.mu.Unlock()
	for _, wait := range pending {
		wait <- codexRPCResponse{err: c.withStderr(err)}
	}
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *codexAppClient) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.closeOnce.Do(func() { close(c.done) })
		return nil
	}
	c.closed = true
	stdin, cmd := c.stdin, c.cmd
	c.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}

func (c *codexAppClient) withStderr(err error) error {
	detail := strings.TrimSpace(c.stderr.String())
	if detail == "" {
		return err
	}
	if len(detail) > 16*1024 {
		detail = detail[len(detail)-16*1024:]
	}
	return fmt.Errorf("%s (%w)", detail, err)
}
