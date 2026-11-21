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
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/liveturns"
	"github.com/wangning19940904/AgentMux/remote"
)

// remoteIdleTimeout detects a dead tunnel. The remote daemon sends an SSE
// keepalive every 15 seconds even while its Agent waits on a model or a tool.
const remoteIdleTimeout = 2 * time.Minute

// WorkerRunner controls workers wherever they run. Cancelling Run's context
// cancels the turn; repeating Run with the same conversation continues it.
type WorkerRunner interface {
	Run(ctx context.Context, worker core.SuperAgentWorker, conversationID, input string, options RunOptions) (string, error)
	Status(ctx context.Context, worker core.SuperAgentWorker) core.SuperAgentWorkerStatus
}

// RunOptions place a turn and report on it. A remote worker's directory is on
// its own machine, so it ignores Dir and BaseRef.
type RunOptions struct {
	// Dir is where a local worker's new conversation gets its directory
	// (core.InvocationRequest.ConversationDir).
	Dir string
	// BaseRef is the commit a new worktree starts from.
	BaseRef string
	// Progress, when set, receives the turn's events as they happen.
	Progress func(core.InvocationStreamEvent)
	// Started, when set, asks for a turn that outlives this process and
	// receives how to rejoin it as soon as that is known: a remote turn's
	// invocation id, or a local turn's session and turn ids when its
	// framework keeps turns. A restarted AgentMux rejoins it with Resume.
	Started func(turn core.SuperAgentTicketTurn)
	// Detach is the context of the process running the turn's caller. When
	// it has ended along with the turn's context, AgentMux is shutting down
	// and a kept turn is left running to be rejoined; otherwise ending the
	// turn's context stops it.
	Detach context.Context
}

// TurnResumer rejoins a turn a worker kept running while AgentMux was away.
type TurnResumer interface {
	Resume(ctx context.Context, worker core.SuperAgentWorker, conversationID string, turn core.SuperAgentTicketTurn, options RunOptions) (string, error)
}

// ErrTurnGone means the worker no longer has the turn: it ended long ago, its
// machine restarted, or it runs a daemon that cannot keep turns.
var ErrTurnGone = errors.New("the worker no longer has this turn")

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
	// Relay reaches machines linked to this machine's hub that this machine
	// cannot reach itself.
	Relay HubRelay
	relay relayCache
}

func (f *Fleet) Run(ctx context.Context, worker core.SuperAgentWorker, conversationID, input string, options RunOptions) (string, error) {
	if worker.HostID != "" {
		return f.runRemote(ctx, worker, conversationID, input, options)
	}
	request := core.InvocationRequest{
		AgentID: worker.AgentID, ConversationID: conversationID, Input: input, ConversationDir: options.Dir, WorktreeBaseRef: options.BaseRef,
		KeepTurn: options.Started != nil,
	}
	ctx = core.WithTurnDetach(ctx, options.Detach)
	var result core.InvocationResult
	var err error
	if streaming, ok := f.Invoker.(core.StreamingInvoker); ok && (options.Progress != nil || options.Started != nil) {
		recorded := false
		result, err = streaming.InvokeStream(ctx, request, func(event core.InvocationStreamEvent) error {
			if !recorded && options.Started != nil && event.Metadata[core.EventMetadataKeptTurn] == "true" && event.SessionID != "" && event.TurnID != "" {
				recorded = true
				options.Started(core.SuperAgentTicketTurn{SessionID: event.SessionID, TurnID: event.TurnID})
			}
			if options.Progress != nil {
				options.Progress(event)
			}
			return nil
		})
	} else {
		result, err = f.Invoker.Invoke(ctx, request)
	}
	if err != nil {
		return "", err
	}
	return result.Answer, nil
}

// Steer adds text to a worker's running turn. A remote worker's turn runs on
// another daemon, which is asked to do it; one that cannot leaves the input
// for the next turn.
func (f *Fleet) Steer(ctx context.Context, worker core.SuperAgentWorker, conversationID, text string) error {
	if worker.HostID != "" {
		return f.steerRemote(ctx, worker, conversationID, text)
	}
	steerer, ok := f.Invoker.(core.InvocationSteerer)
	if !ok {
		return core.ErrSteerUnsupported
	}
	return steerer.SteerInvocation(ctx, worker.AgentID, conversationID, text)
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
	if instance != nil {
		status.RuntimeID = instance.RuntimeID
	}
	return status
}

func (f *Fleet) remoteClient(hostID string) (remote.Host, *http.Client, error) {
	var uplink http.RoundTripper
	if f.Relay != nil {
		uplink = f.Relay.Uplink()
	}
	var host remote.Host
	known := false
	if f.Hosts != nil {
		host, known = f.Hosts.Get(hostID)
	}
	if !known {
		if uplink != nil {
			if name, ok := f.relayDevices()[hostID]; ok {
				return relayedHost(hostID, name), &http.Client{Transport: relayTransport{hostID: hostID, uplink: uplink}}, nil
			}
		}
		if f.Hosts == nil {
			return remote.Host{}, nil, errors.New("remote SSH control is unavailable")
		}
		return remote.Host{}, nil, fmt.Errorf("remote host %q not found", hostID)
	}
	var transport http.RoundTripper = f.directTransport(hostID)
	if uplink != nil && host.Kind == remote.KindLink {
		// A device this machine knows of but is not connected to, as on a
		// standby hub, is reached through the hub.
		transport = fallbackTransport{direct: transport, relay: relayTransport{hostID: hostID, uplink: uplink}}
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
// idle to the tunnel for the length of a turn. The remote daemon runs it
// detached: losing the stream does not stop it, so a broken stream is
// rejoined, and a restarted AgentMux rejoins it with Resume. Ending ctx stops
// the remote turn, unless options.Detach has ended too, which means this
// process is shutting down and leaves the turn for the next one.
func (f *Fleet) runRemote(ctx context.Context, worker core.SuperAgentWorker, conversationID, input string, options RunOptions) (string, error) {
	host, client, err := f.remoteClient(worker.HostID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(core.InvocationRequest{AgentID: worker.AgentID, ConversationID: conversationID, Input: input})
	if err != nil {
		return "", err
	}
	turn := &remoteTurn{host: host, client: client, options: options}
	answer, err := turn.follow(ctx, func(ctx context.Context) (*http.Request, error) {
		request, err := remoteRequest(ctx, host, http.MethodPost, "/api/v1/invocations/stream", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		// A daemon without detached turns ignores this.
		request.URL.RawQuery = "detach=1"
		return request, nil
	}, 0)
	turn.release(ctx)
	return answer, err
}

// Resume rejoins a turn this process, or one before it, started. The events
// already seen are not reported to options.Progress again.
func (f *Fleet) Resume(ctx context.Context, worker core.SuperAgentWorker, conversationID string, turn core.SuperAgentTicketTurn, options RunOptions) (string, error) {
	if worker.HostID == "" {
		return f.resumeLocal(ctx, worker, conversationID, turn, options)
	}
	if turn.InvocationID == "" {
		return "", ErrTurnGone
	}
	host, client, err := f.remoteClient(worker.HostID)
	if err != nil {
		return "", err
	}
	remote := &remoteTurn{host: host, client: client, options: options, id: turn.InvocationID}
	answer, err := remote.rejoin(ctx, time.Now())
	remote.release(ctx)
	return answer, err
}

// resumeLocal follows a local turn the worker's framework kept running, as a
// detached Codex app-server does. Ending ctx stops it unless options.Detach
// has ended too, as for a turn Run started.
func (f *Fleet) resumeLocal(ctx context.Context, worker core.SuperAgentWorker, conversationID string, turn core.SuperAgentTicketTurn, options RunOptions) (string, error) {
	rejoiner, ok := f.Invoker.(core.InvocationRejoiner)
	if !ok || turn.SessionID == "" || turn.TurnID == "" {
		return "", ErrTurnGone
	}
	result, err := rejoiner.RejoinInvocation(core.WithTurnDetach(ctx, options.Detach), core.InvocationRejoin{
		AgentID: worker.AgentID, ConversationID: conversationID,
		Turn: core.AgentTurnRef{SessionID: turn.SessionID, TurnID: turn.TurnID},
	}, func(event core.InvocationStreamEvent) error {
		if options.Progress != nil {
			options.Progress(event)
		}
		return nil
	})
	if errors.Is(err, core.ErrInvocationTurnGone) {
		return "", ErrTurnGone
	}
	if err != nil {
		return "", err
	}
	return result.Answer, nil
}

const (
	// remoteRejoinWindow is how long a broken stream is retried. A
	// restarted hub needs its machines to link again first.
	remoteRejoinWindow = 3 * time.Minute
	remoteRejoinDelay  = 2 * time.Second
	remoteCancelTime   = 10 * time.Second
)

// remoteTurn is one turn on a remote daemon, followed across broken streams.
type remoteTurn struct {
	host    remote.Host
	client  *http.Client
	options RunOptions
	// id is the remote invocation id, known from the first event; seq is the
	// last event seen.
	id  string
	seq int64
}

// follow opens the stream newRequest makes and reads it to the turn's
// outcome, rejoining by id when the stream breaks. Events up to quiet are
// not reported as progress, because they were seen before.
func (t *remoteTurn) follow(ctx context.Context, newRequest func(context.Context) (*http.Request, error), quiet int64) (string, error) {
	answer, err := t.stream(ctx, newRequest, quiet)
	var broken streamBroken
	if !errors.As(err, &broken) {
		return answer, err
	}
	if t.id == "" {
		return "", retryableError(broken)
	}
	answer, err = t.rejoin(ctx, time.Now())
	if errors.Is(err, ErrTurnGone) {
		// An older daemon stops the turn when its stream breaks.
		return "", retryableError(broken)
	}
	return answer, err
}

// rejoin attaches to the turn by id until it ends, retrying broken streams
// for remoteRejoinWindow from since.
func (t *remoteTurn) rejoin(ctx context.Context, since time.Time) (string, error) {
	var last error
	for {
		seen := t.seq
		answer, err := t.stream(ctx, func(ctx context.Context) (*http.Request, error) {
			path := "/api/v1/invocations/" + url.PathEscape(t.id) + "/events"
			request, err := remoteRequest(ctx, t.host, http.MethodGet, path, nil)
			if err != nil {
				return nil, err
			}
			request.URL.RawQuery = url.Values{"after": {strconv.FormatInt(t.seq, 10)}}.Encode()
			return request, nil
		}, -1)
		var broken streamBroken
		if !errors.As(err, &broken) {
			return answer, err
		}
		last = broken.err
		if time.Since(since) > remoteRejoinWindow {
			return "", retryableError{fmt.Errorf("remote %s: could not rejoin the turn: %w", t.host.Name, last)}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(remoteRejoinDelay):
		}
		if t.seq > seen {
			// The stream got somewhere before it broke: the window starts
			// again.
			since = time.Now()
		}
	}
}

// streamBroken is a stream that ended or failed without the turn's outcome.
type streamBroken struct{ err error }

func (e streamBroken) Error() string { return e.err.Error() }
func (e streamBroken) Unwrap() error { return e.err }

// stream makes one request and reads its events. quiet is the last sequence
// number not to report as progress; -1 takes it from the rejoin response.
func (t *remoteTurn) stream(ctx context.Context, newRequest func(context.Context) (*http.Request, error), quiet int64) (string, error) {
	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	request, err := newRequest(streamCtx)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := t.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", streamBroken{fmt.Errorf("remote %s: %w", t.host.Name, err)}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message := fmt.Errorf("remote %s: %s", t.host.Name, remoteErrorMessage(response))
		switch {
		case request.Method == http.MethodGet && response.StatusCode < http.StatusInternalServerError && response.StatusCode != http.StatusConflict:
			// The daemon does not have the turn, or this daemon or the
			// hub's relay predates rejoining.
			return "", ErrTurnGone
		case response.StatusCode == http.StatusConflict:
			// The remote is still finishing this conversation, typically
			// after a turn that could not be rejoined; a later attempt
			// continues it.
			return "", retryableError{message}
		case response.StatusCode >= http.StatusBadGateway:
			// The hub's relay or the tunnel failed, not the remote.
			if request.Method == http.MethodGet {
				return "", streamBroken{message}
			}
			return "", retryableError{message}
		}
		return "", message
	}
	if quiet < 0 {
		quiet, _ = strconv.ParseInt(response.Header.Get(liveturns.SeqHeader), 10, 64)
	}
	idle := time.AfterFunc(remoteIdleTimeout, func() { cancel(errors.New("no data from the remote daemon")) })
	defer idle.Stop()
	progress := func(seq int64, event core.InvocationStreamEvent) {
		if seq > t.seq {
			t.seq = seq
		}
		if t.id == "" && event.InvocationID != "" {
			t.id = event.InvocationID
			if t.options.Started != nil {
				t.options.Started(core.SuperAgentTicketTurn{InvocationID: t.id})
			}
		}
		if t.options.Progress != nil && (seq == 0 || seq > quiet) {
			t.options.Progress(event)
		}
	}
	return readInvocationStream(response.Body, func() { idle.Reset(remoteIdleTimeout) }, progress, func(cause error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if reason := context.Cause(streamCtx); reason != nil {
			cause = reason
		}
		return streamBroken{fmt.Errorf("remote %s: stream ended early: %w", t.host.Name, cause)}
	})
}

// release cancels the remote turn when ctx ended for a reason other than
// this process shutting down: a stop or the turn's deadline.
func (t *remoteTurn) release(ctx context.Context) {
	if ctx.Err() == nil || t.id == "" {
		return
	}
	if t.options.Detach != nil && t.options.Detach.Err() != nil {
		return
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remoteCancelTime)
	defer cancel()
	request, err := remoteRequest(cancelCtx, t.host, http.MethodPost, "/api/v1/invocations/"+url.PathEscape(t.id)+"/cancel", nil)
	if err != nil {
		return
	}
	response, err := t.client.Do(request)
	if err != nil {
		return
	}
	_ = response.Body.Close()
}

// readInvocationStream reads the invocation SSE contract. Only a terminal event
// counts as an outcome: a stream that ends without one means the path failed.
// Each event comes with its SSE id, the turn's sequence number, or 0 from a
// daemon that sends none.
func readInvocationStream(body io.Reader, activity func(), progress func(int64, core.InvocationStreamEvent), interrupted func(error) error) (string, error) {
	reader := bufio.NewReaderSize(body, 64*1024)
	var data strings.Builder
	var seq int64
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			activity()
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "id:"):
			seq, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(trimmed, "id:")), 10, 64)
		case strings.HasPrefix(trimmed, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
		case trimmed == "" && data.Len() > 0:
			var event core.InvocationStreamEvent
			if decodeErr := json.Unmarshal([]byte(data.String()), &event); decodeErr != nil {
				return "", fmt.Errorf("decode remote event: %w", decodeErr)
			}
			data.Reset()
			if progress != nil {
				progress(seq, event)
			}
			seq = 0
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
