package remote

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
)

type httpResult struct {
	status int
	body   []byte
}

// DoHTTP reads a bounded HTTP response through the selected host's SSH tunnel.
// Replayable GETs retry the entire read, including headers and body. Other
// requests only retry opening the tunnel, before any HTTP bytes are sent.
func (m *Manager) DoHTTP(id string, request *http.Request, responseLimit int64) (int, []byte, error) {
	if responseLimit <= 0 {
		return 0, nil, fmt.Errorf("response limit must be positive")
	}
	host, ok := m.store.Get(id)
	if !ok {
		if device, linked := m.linked(request.Context(), id); linked {
			return m.doLinkedHTTP(device, request, responseLimit)
		}
		return 0, nil, os.ErrNotExist
	}
	replayable := request.Method == http.MethodGet &&
		(request.Body == nil || request.Body == http.NoBody || request.GetBody != nil)
	if replayable && request.Body != nil {
		defer request.Body.Close()
	}
	attempt := func(ctx context.Context) (httpResult, error) {
		req := request.Clone(ctx)
		if replayable && request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return httpResult{}, fmt.Errorf("reopen GET request body: %w", err)
			}
			req.Body = body
		}
		transport := &http.Transport{
			Proxy: nil, DisableKeepAlives: true,
			DialContext: func(_ context.Context, network, _ string) (net.Conn, error) {
				if replayable {
					// One retry budget covers dialing and reading; do not nest the
					// three tunnel attempts inside three HTTP attempts.
					conn, _, err := m.dialOnce(ctx, host, network)
					return conn, err
				}
				return m.DialContext(ctx, id, network)
			},
		}
		defer transport.CloseIdleConnections()
		response, err := transport.RoundTrip(req)
		if err != nil {
			return httpResult{}, fmt.Errorf("receive HTTP response: %w", err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
		if err != nil {
			return httpResult{}, fmt.Errorf("read HTTP response body: %w", err)
		}
		if int64(len(payload)) > responseLimit {
			return httpResult{}, fmt.Errorf("response exceeds %d bytes", responseLimit)
		}
		return httpResult{status: response.StatusCode, body: payload}, nil
	}
	var result httpResult
	var err error
	if replayable {
		// An HTTP channel closing does not prove that the shared SSH client is
		// dead. Preserve sibling requests; dialOnce invalidates a stale client
		// if opening the next channel fails.
		result, err = retrySSH(request.Context(), m.timeout, attempt)
	} else {
		result, err = attempt(request.Context())
	}
	if err != nil {
		return 0, nil, fmt.Errorf("remote %s: %w", host.Name, err)
	}
	return result.status, result.body, nil
}

// doLinkedHTTP reads a bounded response from a linked device. A stream on a
// live link opens without a handshake that can fail halfway, so there is
// nothing to retry: an offline device is reported at once.
func (m *Manager) doLinkedHTTP(device LinkedDevice, request *http.Request, responseLimit int64) (int, []byte, error) {
	transport := m.linkedTransport(device)
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(request)
	if err != nil {
		return 0, nil, fmt.Errorf("remote %s: %w", device.Name, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return 0, nil, fmt.Errorf("remote %s: read HTTP response body: %w", device.Name, err)
	}
	if int64(len(payload)) > responseLimit {
		return 0, nil, fmt.Errorf("remote %s: response exceeds %d bytes", device.Name, responseLimit)
	}
	return response.StatusCode, payload, nil
}
