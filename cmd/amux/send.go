package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func sendCmd() *cobra.Command {
	var (
		project         string
		channelID       string
		conversationKey string
		text            string
		images          []string
		files           []string
		addr            string
		token           string
	)
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send text or artifacts through the running AgentMux daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			channelDelivery := strings.TrimSpace(channelID) != "" || strings.TrimSpace(conversationKey) != "" || len(images) > 0 || len(files) > 0
			if channelDelivery {
				if strings.TrimSpace(channelID) == "" || strings.TrimSpace(conversationKey) == "" {
					return fmt.Errorf("--channel-id and --conversation-key are required for channel delivery")
				}
				if strings.TrimSpace(project) != "" {
					return fmt.Errorf("--project cannot be combined with channel delivery")
				}
			} else if strings.TrimSpace(text) == "" {
				return fmt.Errorf("--text is required for project delivery")
			}
			if strings.TrimSpace(text) == "" && len(images) == 0 && len(files) == 0 {
				return fmt.Errorf("provide --text, --image, or --file")
			}
			var err error
			if images, err = absolutePaths(images); err != nil {
				return err
			}
			if files, err = absolutePaths(files); err != nil {
				return err
			}
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			if addr == "" {
				addr = cfg.Server.Addr
			}
			if token == "" {
				token = cfg.Bridge.Token
			}
			body, _ := json.Marshal(struct {
				Project         string   `json:"project,omitempty"`
				ChannelID       string   `json:"channel_id,omitempty"`
				ConversationKey string   `json:"conversation_key,omitempty"`
				Text            string   `json:"text,omitempty"`
				Images          []string `json:"images,omitempty"`
				Files           []string `json:"files,omitempty"`
			}{project, channelID, conversationKey, text, images, files})
			url := "http://" + addr + "/api/v1/send"
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				message := strings.TrimSpace(string(detail))
				if message != "" {
					return fmt.Errorf("send failed: %s: %s", resp.Status, message)
				}
				return fmt.Errorf("send failed: %s", resp.Status)
			}
			cmd.Println("sent")
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "target project name")
	cmd.Flags().StringVar(&channelID, "channel-id", "", "active channel ID")
	cmd.Flags().StringVar(&conversationKey, "conversation-key", "", "active conversation key")
	cmd.Flags().StringVar(&text, "text", "", "message text (optional with an attachment)")
	cmd.Flags().StringSliceVar(&images, "image", nil, "image path to upload (repeatable)")
	cmd.Flags().StringSliceVar(&files, "file", nil, "file path to upload (repeatable)")
	cmd.Flags().StringVar(&addr, "addr", "", "daemon address (default from config)")
	cmd.Flags().StringVar(&token, "token", "", "bridge token (default from config)")
	return cmd
}

func absolutePaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(strings.TrimSpace(path))
		if err != nil {
			return nil, err
		}
		result = append(result, absolute)
	}
	return result, nil
}
