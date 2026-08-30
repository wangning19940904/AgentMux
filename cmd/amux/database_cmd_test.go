package main

import (
	"strings"
	"testing"
)

func TestDatabaseImportConfigRequiresExplicitMode(t *testing.T) {
	for _, args := range [][]string{{}, {"--dry-run", "--apply"}} {
		command := databaseImportConfigCmd()
		command.SetArgs(args)
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

func TestSendRequiresExplicitChannelConversation(t *testing.T) {
	command := sendCmd()
	command.SetArgs([]string{"--text", "hello"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--channel-id and --conversation-key") {
		t.Fatalf("error=%v", err)
	}
}
