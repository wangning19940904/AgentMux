package traeauth

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in on a host with an existing TRAE login. This calls the native refresh
// RPC and model directory only; it never starts a model turn or browser login.
func TestLiveNativeRefresh(t *testing.T) {
	if os.Getenv("AGENTMUX_LIVE_TRAE_AUTH") != "1" {
		t.Skip("set AGENTMUX_LIVE_TRAE_AUTH=1 on a signed-in TRAE host")
	}
	before, err := ReadMetadata(nil)
	if err != nil || !before.Managed {
		t.Fatal("a TRAE-managed login is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := refreshNative(ctx, nil); err != nil {
		t.Fatal(err)
	}
	after, err := ReadMetadata(nil)
	if err != nil || !after.Managed || !after.ExpiresAt.After(time.Now()) {
		t.Fatal("native refresh did not leave valid credentials")
	}
	t.Logf("native refresh RPC succeeded; expiry before=%s after=%s", before.ExpiresAt.UTC().Format(time.RFC3339), after.ExpiresAt.UTC().Format(time.RFC3339))
}
