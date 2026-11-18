package framework

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersionCacheFollowsBinaryFingerprint(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "tool")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 1.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fingerprint, ok := binaryFingerprint(binary)
	if !ok {
		t.Fatal("expected a fingerprint for a regular file")
	}
	t.Cleanup(func() { ForgetVersion("test-tool") })
	storeVersion("test-tool", fingerprint, "1.0.0")
	if version, ok := cachedVersion("test-tool", fingerprint); !ok || version != "1.0.0" {
		t.Fatalf("cachedVersion = %q, %v", version, ok)
	}

	// Replacing the binary (as an update does) changes the fingerprint.
	later := time.Now().Add(time.Minute)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 1.0.1-updated\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(binary, later, later); err != nil {
		t.Fatal(err)
	}
	updated, _ := binaryFingerprint(binary)
	if updated == fingerprint {
		t.Fatal("fingerprint did not change after replacing the binary")
	}
	if _, ok := cachedVersion("test-tool", updated); ok {
		t.Fatal("served a cached version for a replaced binary")
	}

	ForgetVersion("test-tool")
	if _, ok := cachedVersion("test-tool", fingerprint); ok {
		t.Fatal("ForgetVersion kept the entry")
	}
}

func TestVersionCacheFollowsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "v1")
	second := filepath.Join(dir, "v2")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "tool")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	before, _ := binaryFingerprint(link)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	after, _ := binaryFingerprint(link)
	if before == after {
		t.Fatal("fingerprint ignored the symlink target")
	}
}
