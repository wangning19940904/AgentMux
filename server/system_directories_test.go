package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemDirectoryEnsureCreatesNestedPath(t *testing.T) {
	s, _ := newTestServer(t)
	want := filepath.Join(t.TempDir(), "agents", "demo")
	rec := doJSON(t, s, http.MethodPost, "/api/v1/system/directories", systemDirectoryRequest{Path: want})
	if rec.Code != http.StatusOK {
		t.Fatalf("ensure directory: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var got systemDirectoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != want {
		t.Fatalf("path = %q, want %q", got.Path, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("created dir stat = %+v, err = %v", info, err)
	}
}

func TestSystemDirectoryEnsureRejectsEmptyPath(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/api/v1/system/directories", systemDirectoryRequest{Path: "  "})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty path: code = %d body = %s", rec.Code, rec.Body.String())
	}
}
