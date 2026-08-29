package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

type cardImageTestClient struct {
	clientAPI
	uploadName   string
	uploadData   []byte
	streamTexts  int
	streamImages [][]streamCardImage
	finalImages  []streamCardImage
}

func (c *cardImageTestClient) BeginStreamCard(context.Context, string, *streamCardControl) (string, error) {
	return "card-1", nil
}

func (c *cardImageTestClient) StreamCardText(context.Context, string, string, int) error {
	c.streamTexts++
	return nil
}

func (c *cardImageTestClient) InsertStreamCardImage(_ context.Context, _, _ string, _ int, image streamCardImage) error {
	c.streamImages = append(c.streamImages, []streamCardImage{image})
	return nil
}

func (c *cardImageTestClient) FinishStreamCard(_ context.Context, _ string, _ string, _ int, _ bool, images []streamCardImage, _ *streamCardControl) error {
	c.finalImages = append([]streamCardImage(nil), images...)
	return nil
}

func (c *cardImageTestClient) uploadImage(_ context.Context, fileName string, data []byte) (string, error) {
	c.uploadName = fileName
	c.uploadData = append([]byte(nil), data...)
	return "img-screenshot", nil
}

func TestActiveCardEmbedsDeliveredImageAndKeepsItOnFinalUpdate(t *testing.T) {
	client := &cardImageTestClient{}
	platform := &Platform{name: "feishu", client: client}
	msg := &core.Message{
		ID: "om_1", ChatID: "oc_1", ChatType: "p2p", ConversationKey: "chat:oc_1",
	}
	stream, err := platform.BeginReply(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Update(context.Background(), "正在处理…", false, false); err != nil {
		t.Fatal(err)
	}
	imageData := []byte("png bytes")
	if err := platform.ReplyImage(context.Background(), msg, "screen.png", imageData); err != nil {
		t.Fatal(err)
	}
	if client.uploadName != "screen.png" || !bytes.Equal(client.uploadData, imageData) {
		t.Fatalf("uploaded image = %q %q", client.uploadName, client.uploadData)
	}
	if len(client.streamImages) != 1 || len(client.streamImages[0]) != 1 || client.streamImages[0][0].Key != "img-screenshot" {
		t.Fatalf("stream card images = %+v", client.streamImages)
	}
	if err := stream.Update(context.Background(), "还在处理…", false, false); err != nil {
		t.Fatal(err)
	}
	if client.streamTexts != 2 || len(client.streamImages) != 1 {
		t.Fatalf("post-image text update used full card refresh: text=%d structural=%d", client.streamTexts, len(client.streamImages))
	}
	if err := stream.Update(context.Background(), "处理完成", true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.finalImages) != 1 || client.finalImages[0].Key != "img-screenshot" || client.finalImages[0].Name != "screen.png" {
		t.Fatalf("final card images = %+v", client.finalImages)
	}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := platform.ReplyImage(context.Background(), msg, "late.png", imageData); err == nil || !strings.Contains(err.Error(), "image delivery is unavailable") {
		t.Fatalf("image after card close error = %v", err)
	}
}

func TestCardJSONRendersUploadedImagesBeforeControls(t *testing.T) {
	images := []streamCardImage{
		{Key: "img_1", Name: "login.png", ElementID: "agentmux_image_1"},
		{Key: "img_2", Name: "result.png", ElementID: "agentmux_image_2"},
	}
	control := &streamCardControl{taskID: "task-1", conversationKey: "chat:oc_1"}
	for name, card := range map[string]string{
		"native": buildStreamCardJSONWithImages("完成", true, false, images, control),
		"legacy": buildCardWithImages("完成", true, false, images, control),
	} {
		if !json.Valid([]byte(card)) {
			t.Fatalf("%s card is not valid JSON: %s", name, card)
		}
		for _, want := range []string{`"tag":"img"`, `"img_key":"img_1"`, `"content":"login.png"`, `"img_key":"img_2"`} {
			if !strings.Contains(card, want) {
				t.Fatalf("%s card missing %q: %s", name, want, card)
			}
		}
		if name == "native" && !strings.Contains(card, `"element_id":"agentmux_image_1"`) {
			t.Fatalf("native card image is missing its element id: %s", card)
		}
		if imageAt, controlsAt := strings.Index(card, `"img_key":"img_1"`), strings.Index(card, `"agentmux_action":"channel_session_control"`); imageAt < 0 || controlsAt < 0 || imageAt > controlsAt {
			t.Fatalf("%s card did not place images before controls: %s", name, card)
		}
	}
}
