package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type githubCLITransport func(*http.Request) (*http.Response, error)

func (f githubCLITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func githubCLITestArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(zipped)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestGitHubCLIStandaloneReleaseUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixture")
	}
	const version = "2.94.0"
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "armv6"
	}
	archiveName := "gh_2.94.0_linux_" + arch + ".tar.gz"
	archive := githubCLITestArchive(t, strings.TrimSuffix(archiveName, ".tar.gz")+"/bin/gh", "#!/bin/sh\necho 'gh version 2.94.0 (test)'\n")
	checksums := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), archiveName)
	previous := http.DefaultClient
	previousRelease := releaseHTTPClient
	t.Cleanup(func() { http.DefaultClient = previous; releaseHTTPClient = previousRelease })
	http.DefaultClient = &http.Client{Transport: githubCLITransport(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch r.URL.String() {
		case githubCLIReleaseBaseURL + "v2.94.0/" + archiveName:
			body = archive
		case githubCLIReleaseBaseURL + "v2.94.0/gh_2.94.0_checksums.txt":
			body = []byte(checksums)
		case "https://api.github.com/repos/cli/cli/releases/latest":
			body = []byte(`{"tag_name":"v2.94.0"}`)
		default:
			t.Fatalf("unexpected download: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})}
	releaseHTTPClient = http.DefaultClient
	bin := t.TempDir()
	destination := filepath.Join(bin, "gh")
	writeExecutable(t, destination, "#!/bin/sh\necho 'gh version 2.76.2 (old)'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	spec, _ := LookupCLI("github-cli")
	result := installGitHubCLIRelease(context.Background(), spec, CLIInstallResult{ID: spec.ID, Action: "update"}, destination, version, arch, nil)
	if !result.OK || normalizeVersion(result.Version) != version || strings.Contains(result.Command, "brew") {
		t.Fatalf("update result = %+v", result)
	}
	if output, err := commandOutput(context.Background(), destination, "--version"); err != nil || normalizeVersion(output) != version {
		t.Fatalf("installed binary version = %q, error = %v", output, err)
	}
	// Exercise the public entry point on the actual target platform as well.
	if runtime.GOOS == "linux" {
		writeExecutable(t, destination, "#!/bin/sh\necho 'gh version 2.76.2 (old)'\n")
		result = InstallCLIWithOptions(context.Background(), "github-cli", "update", CLIInstallOptions{})
		if !result.OK || normalizeVersion(result.Version) != version || strings.Contains(result.Command, "brew") {
			t.Fatalf("Linux entry point result = %+v", result)
		}
	}
}

func TestGitHubCLIReleaseFailurePreservesExistingBinary(t *testing.T) {
	const archiveName = "gh_2.94.0_linux_amd64.tar.gz"
	for _, failure := range []string{"checksum", "missing binary", "invalid executable"} {
		t.Run(failure, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "gh")
			if err := os.WriteFile(destination, []byte("original"), 0o755); err != nil {
				t.Fatal(err)
			}
			entry := "gh_2.94.0_linux_amd64/bin/gh"
			if failure == "missing binary" {
				entry = "../../gh"
			}
			archive := githubCLITestArchive(t, entry, "replacement")
			checksums := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), archiveName)
			if failure == "checksum" {
				checksums = "incorrect  " + archiveName
			}
			err := replaceGitHubCLIRelease(archive, checksums, archiveName, destination, func(string) error {
				return fmt.Errorf("executable cannot run on this platform")
			})
			if err == nil {
				t.Fatal("invalid release succeeded")
			}
			if content, err := os.ReadFile(destination); err != nil || string(content) != "original" {
				t.Fatalf("working executable changed: %q, %v", content, err)
			}
		})
	}
}

func TestGitHubCLIPackageOwnershipSelectsNativeUpdater(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command fixtures")
	}
	for _, manager := range []string{"apt-get", "dnf", "standalone"} {
		t.Run(manager, func(t *testing.T) {
			bin := t.TempDir()
			writeExecutable(t, filepath.Join(bin, "dpkg-query"), "#!/bin/sh\nexit 1\n")
			writeExecutable(t, filepath.Join(bin, "rpm"), "#!/bin/sh\nexit 1\n")
			if manager == "apt-get" {
				writeExecutable(t, filepath.Join(bin, "dpkg-query"), "#!/bin/sh\necho 'gh: /usr/bin/gh'\n")
			}
			if manager == "dnf" {
				writeExecutable(t, filepath.Join(bin, "rpm"), "#!/bin/sh\nprintf gh\n")
				writeExecutable(t, filepath.Join(bin, "dnf"), "#!/bin/sh\nexit 0\n")
			}
			t.Setenv("PATH", bin)
			command, err := githubCLIPackageCommand(context.Background(), "/usr/bin/gh", "update")
			if err != nil {
				t.Fatal(err)
			}
			if manager == "standalone" {
				if len(command) != 0 {
					t.Fatalf("unmanaged executable selected package command: %v", command)
				}
			} else if !strings.Contains(strings.Join(command, " "), manager+" ") {
				t.Fatalf("package manager = %s, command = %v", manager, command)
			}
		})
	}
}
