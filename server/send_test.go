package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

type channelDeliveryTestSender struct {
	delivery core.ChannelDelivery
}

func (s *channelDeliveryTestSender) SendToChannel(_ context.Context, delivery core.ChannelDelivery) error {
	s.delivery = delivery
	return nil
}

func TestHandleSendLoadsLoopbackChannelAttachments(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "qr.png")
	filePath := filepath.Join(dir, "result.txt")
	if err := os.WriteFile(imagePath, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"channel_id": "channel-1", "conversation_key": "root:message-1",
		"text": "scan", "images": []string{imagePath}, "files": []string{filePath},
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &channelDeliveryTestSender{}
	server := &Server{sender: sender}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/send", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	server.handleSend(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if sender.delivery.ChannelID != "channel-1" || sender.delivery.ConversationKey != "root:message-1" || sender.delivery.Text != "scan" {
		t.Fatalf("delivery = %+v", sender.delivery)
	}
	if len(sender.delivery.Images) != 1 || sender.delivery.Images[0].Name != "qr.png" || string(sender.delivery.Images[0].Data) != "png" {
		t.Fatalf("images = %+v", sender.delivery.Images)
	}
	if len(sender.delivery.Files) != 1 || sender.delivery.Files[0].Name != "result.txt" || string(sender.delivery.Files[0].Data) != "result" {
		t.Fatalf("files = %+v", sender.delivery.Files)
	}
}

func TestHandleSendRejectsNonLoopbackChannelDelivery(t *testing.T) {
	body := []byte(`{"channel_id":"channel-1","conversation_key":"root:message-1","text":"hello"}`)
	server := &Server{sender: &channelDeliveryTestSender{}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/send", bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.1:12345"
	recorder := httptest.NewRecorder()
	server.handleSend(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
