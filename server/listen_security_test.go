package server

import (
	"context"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
)

func TestListenAndServeRejectsUnauthenticatedWildcard(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Addr = "0.0.0.0:8765"
	srv := &Server{cfg: cfg}
	err := srv.ListenAndServe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("ListenAndServe error = %v", err)
	}
}
