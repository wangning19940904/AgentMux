// Package sdkagent hosts SDK-based agent frameworks (claude-agent-sdk,
// openai-agents) by driving the Node sidecar worker over a line-delimited JSON
// protocol. Frameworks that are installed (detected in the sidecar's
// node_modules) register themselves with core so they appear as routable agent
// runtimes; uninstalled frameworks are not registered.
package sdkagent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

// request is a message sent to the sidecar worker.
type request struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Kind         string            `json:"kind,omitempty"`
	Prompt       string            `json:"prompt,omitempty"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	WorkDir      string            `json:"work_dir,omitempty"`
	Model        string            `json:"model,omitempty"`
	Name         string            `json:"name,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

// response is a message received from the sidecar worker.
type response struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text"`
	Final bool   `json:"final"`
	Error string `json:"error"`
	Usage *struct {
		Model            string `json:"model"`
		InputTokens      int64  `json:"input_tokens"`
		OutputTokens     int64  `json:"output_tokens"`
		CacheReadTokens  int64  `json:"cache_read_tokens"`
		CacheWriteTokens int64  `json:"cache_write_tokens"`
	} `json:"usage"`
}

// client is a singleton wrapper around one long-lived sidecar process. Runs are
// serialized so per-run env overlays inside the worker never clobber each other.
type client struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	dec   *json.Decoder
	runMu sync.Mutex
}

var (
	shared     *client
	sharedOnce sync.Mutex
)

// getClient returns the process-wide sidecar client, spawning the worker on
// first use.
func getClient() (*client, error) {
	sharedOnce.Lock()
	defer sharedOnce.Unlock()
	if shared != nil && shared.alive() {
		return shared, nil
	}
	c, err := spawn()
	if err != nil {
		return nil, err
	}
	shared = c
	return shared, nil
}

func spawn() (*client, error) {
	if err := framework.EnsureSidecar(); err != nil {
		return nil, fmt.Errorf("prepare sidecar: %w", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("node not found on PATH: %w", err)
	}
	cmd := exec.Command(node, framework.WorkerPath())
	cmd.Dir = framework.SidecarDir()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &client{cmd: cmd, stdin: stdin, dec: json.NewDecoder(bufio.NewReader(stdout))}
	// Drain the initial "ready" line so it does not confuse the first run.
	var ready response
	if err := c.dec.Decode(&ready); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("sidecar handshake: %w", err)
	}
	return c, nil
}

func (c *client) alive() bool {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return false
	}
	return c.cmd.ProcessState == nil
}

// run submits one turn to the sidecar and streams mapped events. Runs are
// serialized on runMu.
func (c *client) run(ctx context.Context, req request, out chan<- *core.Event) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	defer close(out)

	req.Type = "run"
	req.ID = newID()

	payload, err := json.Marshal(req)
	if err != nil {
		out <- &core.Event{Type: core.EventError, Err: err}
		return
	}

	c.mu.Lock()
	_, werr := c.stdin.Write(append(payload, '\n'))
	c.mu.Unlock()
	if werr != nil {
		out <- &core.Event{Type: core.EventError, Err: fmt.Errorf("sidecar write: %w", werr)}
		return
	}

	for {
		if err := ctx.Err(); err != nil {
			out <- &core.Event{Type: core.EventError, Err: err}
			return
		}
		var resp response
		if err := c.dec.Decode(&resp); err != nil {
			out <- &core.Event{Type: core.EventError, Err: fmt.Errorf("sidecar read: %w", err)}
			return
		}
		if resp.ID != req.ID {
			continue // stray frame from another request; skip
		}
		if ev := mapResponse(resp); ev != nil {
			out <- ev
		}
		if resp.Final || resp.Type == "error" {
			return
		}
	}
}

func mapResponse(resp response) *core.Event {
	switch resp.Type {
	case "output":
		ev := &core.Event{Type: core.EventOutput, Text: resp.Text}
		if resp.Usage != nil {
			ev.Usage = &core.TurnUsage{
				Model:            resp.Usage.Model,
				InputTokens:      resp.Usage.InputTokens,
				OutputTokens:     resp.Usage.OutputTokens,
				CacheReadTokens:  resp.Usage.CacheReadTokens,
				CacheWriteTokens: resp.Usage.CacheWriteTokens,
			}
		}
		return ev
	case "tool_use":
		return &core.Event{Type: core.EventToolUse, ToolUse: resp.Text}
	case "final":
		return &core.Event{Type: core.EventFinal, Text: resp.Text, Final: true}
	case "error":
		return &core.Event{Type: core.EventError, Err: fmt.Errorf("%s", resp.Error)}
	default:
		return nil
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
