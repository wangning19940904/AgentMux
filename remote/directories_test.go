package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type shellDirectoryClient struct{}

func (shellDirectoryClient) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (shellDirectoryClient) Run(ctx context.Context, command string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = stdin
	return cmd.CombinedOutput()
}

func (shellDirectoryClient) Close() error { return nil }

func TestManagerDirectoriesThroughSSHClient(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "Alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	host, err := store.Upsert(Host{
		Name: "test", Host: "127.0.0.1", Port: 22, User: "tester",
		RemoteAddr: "127.0.0.1:8765", HostKeyFingerprint: "SHA256:test",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{store: store, clients: map[string]*cachedClient{}}
	manager.cache(host, shellDirectoryClient{})

	listing, err := manager.ListDirectories(context.Background(), host.ID, root)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		gotNames = append(gotNames, entry.Name)
	}
	if want := []string{".hidden", "Alpha", "zeta"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("directory names = %#v, want %#v", gotNames, want)
	}

	created := filepath.Join(root, "new", "nested")
	gotPath, err := manager.EnsureDirectory(context.Background(), host.ID, created)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.EvalSymlinks(created)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != wantPath {
		t.Fatalf("created path = %q, want %q", gotPath, wantPath)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("created directory stat = %+v, err = %v", info, err)
	}
}

func TestParseDirectoryListingSortsAndCleansPaths(t *testing.T) {
	raw := strings.Join([]string{
		"/home/dev", "/home", "zeta", "/home/dev/zeta/", "Alpha", "/home/dev/Alpha", "",
	}, "\x00")
	got, err := parseDirectoryListing([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := DirectoryListing{
		Path: "/home/dev", ParentPath: "/home",
		Entries: []DirectoryEntry{
			{Name: "Alpha", Path: "/home/dev/Alpha"},
			{Name: "zeta", Path: "/home/dev/zeta"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listing = %#v, want %#v", got, want)
	}
}

func TestParseDirectoryListingRejectsMalformedOutput(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("/home/dev\x00"), []byte("/home/dev\x00/home\x00name\x00")} {
		if _, err := parseDirectoryListing(raw); err == nil {
			t.Fatalf("parseDirectoryListing(%q) succeeded", raw)
		}
	}
}

func TestRemoteDirectoryPathScriptQuotesInput(t *testing.T) {
	script := remoteDirectoryPathScript("~/team's workspace", true)
	if !strings.Contains(script, `'~/team'"'"'s workspace'`) {
		t.Fatalf("path was not shell quoted: %s", script)
	}
}
