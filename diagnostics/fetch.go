package diagnostics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPFetcher reads the daemon's API at addr (host:port) with the bridge
// token, if there is one. The token goes only to that address and is never
// written into the bundle.
func HTTPFetcher(addr, token string) Fetcher {
	client := &http.Client{Timeout: 20 * time.Second}
	return func(ctx context.Context, path string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			detail := strings.TrimSpace(string(body))
			if len(detail) > 200 {
				detail = detail[:200]
			}
			if detail != "" {
				return nil, fmt.Errorf("%s: %s", resp.Status, detail)
			}
			return nil, fmt.Errorf("%s", resp.Status)
		}
		return body, nil
	}
}

// Probe checks that a daemon answers its status endpoint. It returns
// ErrNoDaemon, wrapped with the cause, when nothing listens.
func Probe(ctx context.Context, fetch Fetcher) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := fetch(ctx, "/api/v1/status"); err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
			return fmt.Errorf("the AgentMux daemon refused the request (%v); check [bridge] token in config.toml", err)
		}
		return fmt.Errorf("%w (%v)", ErrNoDaemon, err)
	}
	return nil
}
