package traeauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeRefreshProtocol(t *testing.T) {
	var sent bytes.Buffer
	responses := `{"id":1,"result":{"userAgent":"traecli"}}
{"method":"account/updated","params":{}}
{"id":2,"result":{"account":{"type":"trae","userId":"private-account"}}}
{"id":3,"result":{"data":[{"id":"GPT-5.6-Sol"}]}}
`
	if err := refreshRPC(&sent, strings.NewReader(responses)); err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	decoder := json.NewDecoder(&sent)
	for decoder.More() {
		var request map[string]any
		if err := decoder.Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	if len(requests) != 4 || requests[0]["method"] != "initialize" || requests[1]["method"] != "initialized" || requests[2]["method"] != "account/read" || requests[2]["params"].(map[string]any)["refreshToken"] != true || requests[3]["method"] != "model/list" || requests[3]["params"].(map[string]any)["forceRefresh"] != true {
		t.Fatalf("incorrect refresh protocol: %#v", requests)
	}
}

func TestNativeRefreshFailuresAreActionableAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name, response string
		want           error
	}{
		{"revoked", `{"error":{"code":-32603,"message":"session could not be refreshed secret-token"}}`, ErrLoginRequired},
		{"network", `{"error":{"code":-32603,"message":"network timeout secret-token"}}`, ErrRefreshUnavailable},
		{"unsupported", `{"error":{"code":-32601,"message":"Method not found"}}`, ErrRefreshUnavailable},
		{"logged-out", `{"result":{"account":null}}`, ErrLoginRequired},
		{"malformed", `{"result":null}`, ErrRefreshUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var frame map[string]any
			_ = json.Unmarshal([]byte(test.response), &frame)
			frame["id"] = 2
			line, _ := json.Marshal(frame)
			err := refreshRPC(&bytes.Buffer{}, strings.NewReader("{\"id\":1,\"result\":{}}\n"+string(line)+"\n"))
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("refresh error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNativeRefreshCancellationStopsHangingCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "traecli"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := refreshNative(ctx, nil); !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("refresh process outlived cancellation")
	}
}
