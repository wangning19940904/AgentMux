//go:build desktop

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestDesktopAssetMiddlewareProxiesAPIOnSameOrigin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/status" {
			t.Fatalf("upstream path = %q", request.URL.Path)
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

func TestDesktopAPITargetUsesLoopbackForWildcardListener(t *testing.T) {
	for _, addr := range []string{":9000", "0.0.0.0:9000", "[::]:9000"} {
		target := desktopAPITarget(addr)
		if target.String() != "http://127.0.0.1:9000" {
			t.Fatalf("desktopAPITarget(%q) = %q", addr, target)
		}
	}
}
