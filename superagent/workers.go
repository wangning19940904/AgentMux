package superagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/remote"
)

// remoteIdleTimeout detects a dead tunnel. The remote daemon sends an SSE
// keepalive every 15 seconds even while its Agent waits on a model or a tool.
const remoteIdleTimeout = 2 * time.Minute

// WorkerRunner controls workers wherever they run. Cancelling Run's context
// cancels the turn; repeating Run with the same conversation continues it.
// dir, when set, is where a local worker's new conversation gets its
// directory (core.InvocationRequest.ConversationDir); a remote worker's
// directory is on its own machine, so it ignores dir.
type WorkerRunner interface {
	Run(ctx context.Context, worker core.SuperAgentWorker, conversationID, input, dir string) (string, error)
	Status(ctx context.Context, worker core.SuperAgentWorker) core.SuperAgentWorkerStatus
}

// retryableError marks a failure of the path to a worker, not of its work.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

// IsRetryable reports whether a turn can safely run again on its conversation.
func IsRetryable(err error) bool {
	var retryable retryableError
	return errors.As(err, &retryable) || errors.Is(err, core.ErrInvocationBusy)
}

// HostDialer is the part of remote.Manager the router needs: resolving a host
// and opening a stream through its verified SSH tunnel.
type HostDialer interface {
	Get(id string) (remote.Host, bool)
	DialContext(ctx context.Context, id, network string) (net.Conn, error)
}

// AgentLookup resolves local Agents for status checks.
type AgentLookup interface {
	GetAgentInstance(context.Context, string) (*core.AgentInstance, error)
}

// Fleet runs local workers through the in-process Invoker and remote workers
// through the same invocation API on the remote daemon.
type Fleet struct {
	Invoker core.Invoker
	Agents  AgentLookup
	Hosts   HostDialer
}

func (f *Fleet) Run(ctx context.Context, worker core.SuperAgentWorker, conversationID, input, dir string) (string, error) {
	if worker.HostID != "" {
		return f.runRemote(ctx, worker, conversationID, input)
	}
	result, err := f.Invoker.Invoke(ctx, core.InvocationRequest{AgentID: worker.AgentID, ConversationID: conversationID, Input: input, ConversationDir: dir})
	if err != nil {
		return "", err
	}
	return result.Answer, nil
}

func (f *Fleet) Status(ctx context.Context, worker core.SuperAgentWorker) core.SuperAgentWorkerStatus {
	status := core.SuperAgentWorkerStatus{WorkerID: worker.ID}
	var instance *core.AgentInstance
	if worker.HostID == "" {
		found, err := f.Agents.GetAgentInstance(ctx, worker.AgentID)
		if err != nil {
			status.Detail = err.Error()
			return status
		}
		instance = found
	} else {
		found, err := f.remoteAgent(ctx, worker)
		if err != nil {
			status.Detail = err.Error()
			return status
		}
		instance = found
	}
	switch {
	case instance == nil:
		status.Detail = "agent not found"
	case !instance.Enabled:
		status.Detail = "agent is disabled"
	default:
		status.Available = true
		status.Detail = instance.Name
	}
	return status
}

func (f *Fleet) remoteClient(hostID string) (remote.Host, *http.Client, error) {
	if f.Hosts == nil {
		return remote.Host{}, nil, errors.New("remote SSH control is unavailable")
	}
	host, ok := f.Hosts.Get(hostID)
	if !ok {
		return remote.Host{}, nil, fmt.Errorf("remote host %q not found", hostID)
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return f.Hosts.DialContext(ctx, hostID, network)
		},
		DisableKeepAlives: true,
	}
	return host, &http.Client{Transport: transport}, nil
}

func remoteRequest(ctx context.Context, host remote.Host, method, path string, body io.Reader) (*http.Request, error) {
	target := url.URL{Scheme: "http", Host: host.RemoteAddr, Path: path}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	if host.APIToken != "" {
		request.Header.Set("Authorization", "Bearer "+host.APIToken)
	}
	return request, nil
}

func (f *Fleet) remoteAgent(ctx context.Context, worker core.SuperAgentWorker) (*core.AgentInstance, error) {
	host, client, err := f.remoteClient(worker.HostID)
	if err != nil {
		return nil, err
	}
	request, err := remoteRequest(ctx, host, http.MethodGet, "/api/v1/agent-instances", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("remote %s: %w", host.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote %s: %s", host.Name, remoteErrorMessage(response))
	}
	var instances []core.AgentInstance
	if err := json.NewDecoder(response.Body).Decode(&instances); err != nil {
		return nil, fmt.Errorf("remote %s: %w", host.Name, err)
	}
	for index := range instances {
		if instances[index].ID == worker.AgentID {
			return &instances[index], nil
		}
	}
	return nil, nil
}

// runRemote streams the invocation because a synchronous request would look
// idle to the tunnel for the length of a turn. Closing the stream on cancel is
// what stops the remote turn: the remote handler's context ends with it.
func (f *Fleet) runRemote(ctx context.Context, worker core.SuperAgentWorker, conversationID, input string) (string, error) {
	host, client, err := f.remoteClient(worker.HostID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(core.InvocationRequest{AgentID: worker.AgentID, ConversationID: conversationID, Input: input})
	if err != nil {
		return "", err
	}
	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	request, err := remoteRequest(streamCtx, host, http.MethodPost, "/api/v1/invocations/stream", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", retryableError{fmt.Errorf("remote %s: %w", host.Name, err)}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message := fmt.Errorf("remote %s: %s", host.Name, remoteErrorMessage(response))
		// A conflict means the remote is still finishing this conversation,
		// typically after our side lost the tunnel; a later attempt continues it.
		if response.StatusCode == http.StatusConflict || response.StatusCode >= http.StatusBadGateway {
			return "", retryableError{message}
		}
		return "", message
	}
	idle := time.AfterFunc(remoteIdleTimeout, func() { cancel(errors.New("no data from the remote daemon")) })
	defer idle.Stop()
	return readInvocationStream(response.Body, func() { idle.Reset(remoteIdleTimeout) }, func(cause error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if reason := context.Cause(streamCtx); reason != nil {
			cause = reason
		}
		return retryableError{fmt.Errorf("remote %s: stream ended early: %w", host.Name, cause)}
	})
}

// readInvocationStream reads the invocation SSE contract. Only a terminal event
// counts as an outcome: a stream that ends without one means the path failed.
func readInvocationStream(body io.Reader, activity func(), interrupted func(error) error) (string, error) {
	reader := bufio.NewReaderSize(body, 64*1024)
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			activity()
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
		case trimmed == "" && data.Len() > 0:
			var event core.InvocationStreamEvent
			if decodeErr := json.Unmarshal([]byte(data.String()), &event); decodeErr != nil {
				return "", fmt.Errorf("decode remote event: %w", decodeErr)
			}
			data.Reset()
			switch event.Type {
			case "completed":
				if event.Result != nil {
					return event.Result.Answer, nil
				}
				return event.Text, nil
			case "error":
				return "", errors.New(firstNonEmpty(event.Error, "remote invocation failed"))
			}
		}
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", interrupted(err)
		}
	}
}

func remoteErrorMessage(response *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
		return parsed.Error
	}
	return firstNonEmpty(strings.TrimSpace(string(body)), response.Status)
}
