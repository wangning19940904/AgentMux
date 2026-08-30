package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorePersistsSecretsPrivatelyAndViewsRedactThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-hosts.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Upsert(Host{
		Name: "Build box", Host: "10.0.0.5", Port: 22, User: "dev",
		RemoteAddr: "127.0.0.1:8765", APIToken: "secret",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Fatal("generated id is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	view := saved.View()
	if !view.APITokenSet {
		t.Fatal("API token should be reported as set")
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || strings.Contains(string(raw), "secret") {
		t.Fatalf("view leaked token: %s", raw)
	}

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get(saved.ID)
	if !ok || got.APIToken != "secret" {
		t.Fatalf("reloaded host = %+v, ok = %v", got, ok)
	}
}

func TestStorePreservesAndClearsTokenAndHostTrust(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "remote-hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Upsert(Host{
		Name: "box", Host: "box.local", Port: 22, User: "dev",
		RemoteAddr: "localhost:8765", APIToken: "secret",
		HostKeyFingerprint: "SHA256:old",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Upsert(Host{
		ID: saved.ID, Name: "renamed", Host: saved.Host, Port: saved.Port,
		User: saved.User, RemoteAddr: saved.RemoteAddr,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.APIToken != "secret" || updated.HostKeyFingerprint != "SHA256:old" {
		t.Fatalf("preserved values missing: %+v", updated)
	}
	updated, err = store.Upsert(Host{
		ID: saved.ID, Name: updated.Name, Host: "new-box.local", Port: 22,
		User: updated.User, RemoteAddr: updated.RemoteAddr,
		APIToken: "replacement-that-must-be-cleared",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.APIToken != "" || updated.HostKeyFingerprint != "" {
		t.Fatalf("token or host trust was not cleared: %+v", updated)
	}
}

func TestStoreRejectsNonLoopbackRemoteAddress(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "remote-hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Upsert(Host{
		Name: "box", Host: "box.local", User: "dev",
		RemoteAddr: "10.0.0.8:8765",
	}, false)
	if err == nil {
		t.Fatal("expected non-loopback remote address to be rejected")
	}
}

func TestStoreRejectsAPITokenControlCharacters(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "remote-hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Upsert(Host{
		Name: "box", Host: "box.local", User: "dev",
		RemoteAddr: "127.0.0.1:8765", APIToken: "secret\nAGENTMUX_UNIT",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "api_token") {
		t.Fatalf("control-character token error = %v", err)
	}
}
