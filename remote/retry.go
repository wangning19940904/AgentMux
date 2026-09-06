package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshRetryAttempts = 3

type sshRetryError struct {
	attempts int
	cause    error
}

func (e *sshRetryError) Error() string {
	if e.attempts == 1 {
		return e.cause.Error()
	}
	return fmt.Sprintf("%v (after %d SSH attempts)", e.cause, e.attempts)
}
func (e *sshRetryError) Unwrap() error { return e.cause }

func retrySSH[T any](ctx context.Context, timeout time.Duration, operation func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 0; attempt < sshRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if attempt > 0 {
			delay := sshRetryDelay(attempt)
			if deadline, ok := ctx.Deadline(); ok {
				delay = min(delay, time.Until(deadline)/4)
			}
			if err := waitSSHRetry(ctx, delay); err != nil {
				return zero, err
			}
		}
		budget := timeout
		if deadline, ok := ctx.Deadline(); ok {
			// Leave time for subsequent attempts instead of spending the entire
			// request deadline on one stale connection.
			budget = min(budget, time.Until(deadline)/time.Duration(sshRetryAttempts-attempt))
		}
		if budget <= 0 {
			return zero, context.DeadlineExceeded
		}
		attemptCtx, cancel := context.WithTimeout(ctx, budget)
		value, err := operation(attemptCtx)
		err = sshAttemptError(attemptCtx, err)
		cancel()
		if err == nil {
			return value, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if !retryableSSHError(err) || attempt == sshRetryAttempts-1 {
			return zero, &sshRetryError{attempts: attempt + 1, cause: err}
		}
	}
	panic("unreachable SSH retry")
}

func sshRetryDelay(failures int) time.Duration {
	base := min(200*time.Millisecond*time.Duration(1<<min(max(failures-1, 0), 4)), 2*time.Second)
	return min(time.Duration(float64(base)*(0.8+rand.Float64()*0.4)), 2*time.Second)
}

func waitSSHRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Authentication, trust, configuration and authorization failures require a
// correction, not another attempt. Only transient transport failures qualify.
func retryableSSHError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || permanentSSHError(err) {
		return false
	}
	var status *remoteHTTPError
	if errors.As(err, &status) {
		return true
	}
	var channel *ssh.OpenChannelError
	if errors.As(err, &channel) {
		return channel.Reason == ssh.ConnectionFailed || channel.Reason == ssh.ResourceShortage
	}
	for _, transient := range []error{context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.EPIPE, syscall.ETIMEDOUT} {
		if errors.Is(err, transient) {
			return true
		}
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsTemporary {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, transient := range []string{"timed out", "connection timeout", "connection reset", "connection refused", "connection closed", "broken pipe", "network is unreachable", "no route to host", "temporary failure in name resolution"} {
		if strings.Contains(message, transient) {
			return true
		}
	}
	return false
}

func permanentSSHError(err error) bool {
	var fallback *sshFallbackError
	if errors.As(err, &fallback) {
		return permanentSSHError(fallback.fallback)
	}
	var unknown *UnknownHostKeyError
	if errors.As(err, &unknown) {
		return true
	}
	var status *remoteHTTPError
	if errors.As(err, &status) {
		return status.status != 408 && status.status != 429 && status.status != 502 && status.status != 503 && status.status != 504
	}
	var channel *ssh.OpenChannelError
	if errors.As(err, &channel) {
		return channel.Reason != ssh.ConnectionFailed && channel.Reason != ssh.ResourceShortage
	}
	message := strings.ToLower(err.Error())
	for _, permanent := range []string{"host key", "hostkey", "permission denied", "unable to authenticate", "no ssh key", "authentication failed", "no supported methods", "not found", "client is closed", "unsupported", "does not support"} {
		if strings.Contains(message, permanent) {
			return true
		}
	}
	return false
}

func sshAttemptError(ctx context.Context, err error) error {
	// exec.CommandContext reports a killed SSH process rather than a timeout.
	// Preserve permanent errors even if they arrive at the deadline boundary.
	if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) && !permanentSSHError(err) {
		return fmt.Errorf("%w: %v", ctx.Err(), err)
	}
	return err
}

// The native client may lack GSSAPI support. Its expected authentication
// failure must not mask a transient failure from the system SSH fallback.
type sshFallbackError struct{ native, fallback error }

func (e *sshFallbackError) Error() string {
	return fmt.Sprintf("native client: %v; system OpenSSH: %v", e.native, e.fallback)
}
func (e *sshFallbackError) Unwrap() error { return e.fallback }

type contextMutex struct{ slot chan struct{} }

func newContextMutex() *contextMutex { return &contextMutex{slot: make(chan struct{}, 1)} }
func (m *contextMutex) Lock()        { _ = m.LockContext(context.Background()) }
func (m *contextMutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *contextMutex) Unlock() { <-m.slot }

// One host shares a handshake slot and failure cooldown. Queued callers
// observe the newest cooldown after acquiring the slot; they cannot stampede
// a recovering server. The slot is released before any HTTP traffic.
type sshAttemptGate struct {
	*contextMutex
	failures int
	next     time.Time
}

func (g *sshAttemptGate) acquire(ctx context.Context) error {
	if err := g.LockContext(ctx); err != nil {
		return err
	}
	if err := waitSSHRetry(ctx, time.Until(g.next)); err != nil {
		g.Unlock()
		return err
	}
	return nil
}
func (g *sshAttemptGate) finish(err error) {
	if retryableSSHError(err) {
		g.failures = min(g.failures+1, 5)
		g.next = time.Now().Add(sshRetryDelay(g.failures))
	} else {
		g.failures = 0
		g.next = time.Time{}
	}
	g.Unlock()
}

type remoteHTTPError struct {
	status int
	body   string
}

func (e *remoteHTTPError) Error() string {
	return fmt.Sprintf("remote AgentMux returned HTTP %d: %s", e.status, e.body)
}
