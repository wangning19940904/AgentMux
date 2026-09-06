package tools

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestGitHubCLILiveReleaseInstall(t *testing.T) {
	version := os.Getenv("AGENTMUX_TEST_RELEASE_VERSION")
	if version == "" || runtime.GOOS != "linux" {
		t.Skip("opt-in Linux release download test")
	}
	spec, _ := LookupCLI("github-cli")
	last := 0
	result := installGitHubCLIRelease(context.Background(), spec, CLIInstallResult{ID: spec.ID, Action: "install"}, filepath.Join(t.TempDir(), "gh"), version, runtime.GOARCH, func(phase, detail string, percent int) {
		if percent-last >= 10 {
			t.Logf("%s %d%% %s", phase, percent, detail)
			last = percent
		}
	})
	if !result.OK || normalizeVersion(result.Version) != version {
		t.Fatalf("release installation failed: %+v", result)
	}
	t.Log(result.Version)
}

func TestReleaseDownloadRangesRetryOnlyFailedChunk(t *testing.T) {
	content := bytes.Repeat([]byte("release binary"), 220000)
	var mu sync.Mutex
	requests := map[int64]int{}
	active, peak := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		requests[start]++
		attempt := requests[start]
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		if start == 2*releaseChunkSize && attempt == 1 {
			w.WriteHeader(502)
			return
		}
		time.Sleep(15 * time.Millisecond)
		end = min(end, int64(len(content))-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()
	var last int64
	got, err := downloadReleaseAsset(context.Background(), server.URL, 4<<20, func(done, total int64) {
		if done < last || total != int64(len(content)) {
			t.Errorf("invalid progress %d/%d after %d", done, total, last)
		}
		last = done
	})
	if err != nil || !bytes.Equal(content, got) {
		t.Fatalf("download error=%v, bytes=%d", err, len(got))
	}
	if last != int64(len(content)) {
		t.Fatalf("incomplete progress: %d", last)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak < 2 || peak > 6 {
		t.Fatalf("concurrency=%d", peak)
	}
	for start, count := range requests {
		want := 1
		if start == 2*releaseChunkSize {
			want = 2
		}
		if count != want {
			t.Errorf("range %d requested %d times, want %d", start, count, want)
		}
	}
}

func TestReleaseClientNegotiatesHTTP1ForIndependentRanges(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("release request used %s", r.Proto)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := newReleaseHTTPClient()
	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	defer transport.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.ProtoMajor != 1 {
		t.Fatalf("negotiated %s", response.Proto)
	}
}

func TestReleaseDownloadRejectsInvalidRangeMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 5-100/999999999")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("bad"))
	}))
	defer server.Close()
	if _, err := downloadGitHubCLIAsset(context.Background(), server.URL, 2<<20); err == nil {
		t.Fatal("accepted invalid range")
	}
}

func TestReleaseDownloadCancellationStopsRetries(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := downloadGitHubCLIAsset(ctx, server.URL, 2<<20); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled download succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("download ignored cancellation")
	}
}
