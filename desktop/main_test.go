//go:build desktop

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/store"
)

func TestDesktopAssetMiddlewareProxiesAPIOnSameOrigin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/status" {
			t.Fatalf("upstream path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer admin-secret" {
			t.Fatalf("upstream authorization = %q", got)
		}
		_, _ = io.WriteString(response, `{"ok":true}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	app := newApp()
	app.apiTarget.Store(target)
	app.setAPIToken("admin-secret")
	handler := app.assetServerMiddleware(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"ok":true}` {
		t.Fatalf("proxied response = %d %q", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://wails.localhost/assets/index.js", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTeapot {
		t.Fatalf("asset fallback status = %d", response.Code)
	}
}

func TestDesktopAssetMiddlewareMarksObservabilitySession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-AgentMux-Desktop") != "1" {
			t.Fatalf("desktop marker = %q", request.Header.Get("X-AgentMux-Desktop"))
		}
		if request.Header.Get("Origin") != "wails://wails.localhost" {
			t.Fatalf("origin = %q", request.Header.Get("Origin"))
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	app := newApp()
	app.apiTarget.Store(target)
	handler := app.assetServerMiddleware(http.NotFoundHandler())
	for _, path := range []string{
		"/api/v1/observability/session",
		"/api/v1/remote/proxy/host-id/observability/session",
	} {
		request := httptest.NewRequest(http.MethodPost, "http://wails.localhost"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
}

func TestDesktopAPITargetUsesLoopbackForWildcardListener(t *testing.T) {
	for _, addr := range []string{":9000", "0.0.0.0:9000", "[::]:9000"} {
		target := desktopAPITarget(addr)
		if target.String() != "http://127.0.0.1:9000" {
			t.Fatalf("desktopAPITarget(%q) = %q", addr, target)
		}
	}
}

func TestLocalWebUIURLFollowsConfiguredDesktopTarget(t *testing.T) {
	app := newApp()
	app.setAPITarget("0.0.0.0:9123")
	if got := app.localWebUIURL(); got != "http://127.0.0.1:9123" {
		t.Fatalf("localWebUIURL() = %q", got)
	}
}

func TestExternalBrowserURLOnlyAllowsWebLinks(t *testing.T) {
	got, err := externalBrowserURL(" https://auth.example.test/device?code=one ")
	if err != nil || got != "https://auth.example.test/device?code=one" {
		t.Fatalf("externalBrowserURL() = %q, %v", got, err)
	}
	for _, raw := range []string{"javascript:alert(1)", "file:///tmp/secret", "https:///missing-host"} {
		if _, err := externalBrowserURL(raw); err == nil {
			t.Fatalf("externalBrowserURL(%q) succeeded", raw)
		}
	}
}

func TestWaitForDesktopStoreRecoversAfterDependencyStarts(t *testing.T) {
	var attempts atomic.Int32
	want := &store.Store{}
	opener := func(context.Context, *config.Config) (*store.Store, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("postgres is starting")
		}
		return want, nil
	}

	got, err := waitForDesktopStore(
		context.Background(),
		config.Default(),
		time.Millisecond,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		opener,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || attempts.Load() != 3 {
		t.Fatalf("store = %p, attempts = %d", got, attempts.Load())
	}
}

func TestWaitForDesktopStoreStopsWhenDesktopShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var attempts atomic.Int32
	opener := func(context.Context, *config.Config) (*store.Store, error) {
		attempts.Add(1)
		return nil, errors.New("postgres is unavailable")
	}

	got, err := waitForDesktopStore(ctx, config.Default(), time.Hour, nil, opener)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if got != nil || attempts.Load() != 1 {
		t.Fatalf("store = %p, attempts = %d", got, attempts.Load())
	}
}
