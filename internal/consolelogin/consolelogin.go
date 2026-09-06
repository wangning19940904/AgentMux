// Package consolelogin prepares authenticated browser entry links for native
// clients. The bridge credential stays in Go; only a single-use nonce enters
// the browser, where the server exchanges it for an HttpOnly session cookie.
package consolelogin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TargetURL converts a daemon listen address to a browser-reachable address.
func TargetURL(addr string) *url.URL {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		host, port = "127.0.0.1", "8765"
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
}

// EntryURL returns the plain Console URL in unauthenticated local mode, or
// mints a short-lived login link using the native client's bridge token.
func EntryURL(ctx context.Context, baseURL, token string) (string, error) {
	if token == "" {
		return baseURL, nil
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return "", fmt.Errorf("invalid Console URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := &http.Client{
		// Never forward the native credential to a redirect destination.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	endpoint := base.ResolveReference(&url.URL{Path: "/api/v1/console/sessions"}).String()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if err != nil {
			return "", fmt.Errorf("prepare Console login: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err == nil {
			if response.StatusCode != http.StatusBadGateway && response.StatusCode != http.StatusServiceUnavailable && response.StatusCode != http.StatusGatewayTimeout {
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					return "", fmt.Errorf("create Console login session: HTTP %d", response.StatusCode)
				}
				var session struct {
					EnterURL string `json:"enter_url"`
				}
				if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&session); err != nil {
					return "", fmt.Errorf("decode Console login session: %w", err)
				}
				entry, err := url.Parse(session.EnterURL)
				if err != nil || entry.Scheme != base.Scheme || !strings.EqualFold(entry.Host, base.Host) || entry.User != nil || entry.Path != "/console/enter" || entry.Query().Get("nonce") == "" {
					return "", fmt.Errorf("invalid Console login entry URL")
				}
				return entry.String(), nil
			}
			response.Body.Close()
		}
		// The daemon starts asynchronously. Wait briefly for its listener, but
		// never retry rejected credentials or fall back to an anonymous page.
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("create Console login session: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
