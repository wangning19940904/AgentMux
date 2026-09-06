package cliagents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestTraeAppProtocolSettingsAndSteer(t *testing.T) {
	agent := newTraeAppAgent(map[string]any{}, nil)
	session := &codexSession{agent: agent.codexAgent, threadID: "thread", activeTurnID: "turn", activeTurn: true, currentApprovalMode: core.ApprovalModeAutoEdit}
	params := session.turnStartParamsInput("thread", core.AgentTurnInput{Text: "hello"})
	if params["sandbox"] != nil {
		t.Fatal("TRAE received Codex legacy sandbox field")
	}
	sandbox, _ := params["sandboxPolicy"].(map[string]any)
	if sandbox["type"] != "workspaceWrite" {
		t.Fatalf("sandbox=%+v", params)
	}
	for _, tc := range []struct {
		name, response    string
		rejected, unknown bool
	}{
		{"accepted", `{"turnId":"turn"}`, false, false},
		{"wrong turn", `{"turnId":"other"}`, false, true},
		{"rejected", `{"error":{"code":-32601,"message":"unsupported"}}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			client := &codexAppClient{stdin: writer, pending: map[int]chan codexRPCResponse{}, done: make(chan struct{}), nextID: 1}
			session.client = client
			received := make(chan map[string]any, 1)
			go func() {
				var request map[string]any
				_ = json.NewDecoder(reader).Decode(&request)
				received <- request
				var response map[string]any
				_ = json.Unmarshal([]byte(tc.response), &response)
				id := int(request["id"].(float64))
				client.mu.Lock()
				wait := client.pending[id]
				client.mu.Unlock()
				if response["error"] != nil {
					wait <- codexRPCResponse{err: rpcError(response)}
				} else {
					wait <- codexRPCResponse{result: response}
				}
			}()
			err := session.Steer(context.Background(), "change direction")
			request := <-received
			p := request["params"].(map[string]any)
			if request["method"] != "turn/steer" || p["expectedTurnId"] != "turn" || p["threadId"] != "thread" {
				t.Fatalf("request=%+v", request)
			}
			var rejected *core.SteerRejectedError
			if errors.As(err, &rejected) != tc.rejected || (err != nil) != (tc.rejected || tc.unknown) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
