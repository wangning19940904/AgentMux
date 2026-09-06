package consolelogin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTargetURL(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{":9000", "http://127.0.0.1:9000"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000"},
		{"[::]:9000", "http://127.0.0.1:9000"},
		{"[::1]:9000", "http://[::1]:9000"},
		{"localhost:9123", "http://localhost:9123"},
	} {
		if got := TargetURL(tc.addr).String(); got != tc.want {
			t.Errorf("TargetURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestEntryURLUsesNativeCredential(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/console/sessions" || r.Header.Get("Authorization") != "Bearer bridge-secret" {
			t.Errorf("unexpected session request: %s %s", r.Method, r.URL.Path)
		}
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"enter_url":"http://%s/console/enter?nonce=one-time"}`, r.Host)
	}))
	defer srv.Close()
	entry, err := EntryURL(context.Background(), srv.URL, "bridge-secret")
	if err != nil || entry != srv.URL+"/console/enter?nonce=one-time" || attempts != 2 {
		t.Fatalf("EntryURL = %q, %v; attempts = %d", entry, err, attempts)
	}
	if strings.Contains(entry, "bridge-secret") {
		t.Fatal("bridge token exposed to browser")
	}
}

func TestEntryURLWithoutToken(t *testing.T) {
	entry, err := EntryURL(context.Background(), "http://127.0.0.1:8765", "")
	if err != nil || entry != "http://127.0.0.1:8765" {
		t.Fatalf("EntryURL = %q, %v", entry, err)
	}
}

func TestEntryURLFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"rejected credential", http.StatusUnauthorized, `unauthorized`},
		{"redirect", http.StatusFound, ``},
		{"invalid JSON", http.StatusOK, `<html>not an API</html>`},
		{"missing entry", http.StatusOK, `{}`},
		{"foreign entry", http.StatusOK, `{"enter_url":"https://other.example/console/enter?nonce=one-time"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			entry, err := EntryURL(context.Background(), srv.URL, "bridge-secret")
			if err == nil || entry != "" || attempts != 1 {
				t.Fatalf("EntryURL = %q, %v; attempts = %d", entry, err, attempts)
			}
		})
	}
}

func TestEntryURLRespectsCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	entry, err := EntryURL(ctx, srv.URL, "bridge-secret")
	if entry != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("EntryURL = %q, %v", entry, err)
	}
}
