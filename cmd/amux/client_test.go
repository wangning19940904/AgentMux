package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
)

func TestOpenDaemonStoreSupportsExplicitSQLiteForRemoteClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote", "agentmux.db")
	st, err := openDaemonStore(config.Default(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	status := st.DatabaseStatus(context.Background())
	if !status.Ready || status.Driver != "sqlite" {
		t.Fatalf("database status = %+v", status)
	}
}

func TestClientCommandExposesSQLitePath(t *testing.T) {
	if flag := clientCmd().Flags().Lookup("sqlite-path"); flag == nil {
		t.Fatal("client command has no --sqlite-path flag")
	}
}
