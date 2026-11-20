package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnosticsDownloadIsAdminOnlyAndRedacted(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Bridge.Enabled = true
	srv.cfg.Bridge.Token = "bridge-diagnostics-secret"
	handler := srv.withAuth(srv.mux)

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("without credentials: code %d", anonymous.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil)
	request.Header.Set("Authorization", "Bearer bridge-diagnostics-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("code %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content type %q", got)
	}
	if got := response.Header().Get("Content-Disposition"); !strings.Contains(got, "agentmux-diagnostics-") {
		t.Fatalf("content disposition %q", got)
	}
	var listed []struct {
		Name string `json:"name"`
		Size int    `json:"size"`
	}
	if err := json.Unmarshal([]byte(response.Header().Get("X-AgentMux-Diagnostics-Files")), &listed); err != nil {
		t.Fatalf("file list header: %v", err)
	}
	body := response.Body.Bytes()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != len(listed) {
		t.Fatalf("header lists %d files, zip holds %d", len(listed), len(reader.File))
	}
	names := map[string]bool{}
	for _, f := range reader.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		if strings.Contains(string(data), "bridge-diagnostics-secret") {
			t.Fatalf("%s contains the bridge token", f.Name)
		}
	}
	// The in-process reads of the server's own API went through.
	for _, want := range []string{"manifest.json", "config.json", "daemon/status.json", "daemon/channels.json", "database/recent-turns.json"} {
		if !names[want] {
			t.Errorf("bundle lacks %s; has %v", want, names)
		}
	}
}
