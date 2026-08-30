//go:build desktop

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestVersionLessHandlesDevelopmentSuffixes(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.1.3-12-geec7b8f", "0.1.3", false},
		{"v0.1.3", "v0.1.4", true},
		{"0.2.0", "0.1.9", false},
		{"development", "0.1.0", true},
	}
	for _, test := range tests {
		if got := versionLess(test.current, test.latest); got != test.want {
			t.Errorf("versionLess(%q, %q) = %t, want %t", test.current, test.latest, got, test.want)
		}
	}
}

func TestCheckDesktopReleaseRequiresArchiveAndChecksum(t *testing.T) {
	asset := desktopAssetName(runtime.GOOS, runtime.GOARCH)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(response, `{
			"tag_name":"v0.2.0",
			"html_url":"https://example.test/release",
			"published_at":"2026-08-30T00:00:00Z",
			"assets":[
				{"name":%q,"browser_download_url":"https://example.test/app.zip"},
				{"name":%q,"browser_download_url":"https://example.test/app.sha256"}
			]
		}`, asset, asset+".sha256")
	}))
	defer server.Close()

	got, err := checkDesktopRelease(context.Background(), server.Client(), server.URL, "0.1.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.status.UpdateAvailable || got.status.LatestVersion != "0.2.0" {
		t.Fatalf("status = %+v", got.status)
	}
	// The test runner's architecture controls native installation support; the
	// release selection itself must still resolve both URLs only on a match.
	if got.downloadURL == "" || got.checksumURL == "" {
		t.Fatalf("release URLs were not selected: %+v", got)
	}
}
