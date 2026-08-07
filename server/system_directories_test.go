package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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

func TestSystemDirectoryListReturnsOnlyDirectories(t *testing.T) {
	s, _ := newTestServer(t)
	root := t.TempDir()
	first := filepath.Join(root, "alpha")
	second := filepath.Join(root, "beta")
	if err := os.Mkdir(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodGet, "/api/v1/system/directories?path="+url.QueryEscape(root), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list directories: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var got systemDirectoryListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != root || got.ParentPath != filepath.Dir(root) {
		t.Fatalf("listing path = %q parent = %q", got.Path, got.ParentPath)
	}
	want := []systemDirectoryEntry{{Name: "alpha", Path: first}, {Name: "beta", Path: second}}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("entries = %#v, want %#v", got.Entries, want)
	}
}

func TestSystemDirectoryListRejectsFile(t *testing.T) {
	s, _ := newTestServer(t)
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodGet, "/api/v1/system/directories?path="+url.QueryEscape(path), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("file path: code = %d body = %s", rec.Code, rec.Body.String())
	}
}
