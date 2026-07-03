package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func sendCmd() *cobra.Command {
	var (
		project string
		text    string
		addr    string
		token   string
	)
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a message to a project via the running daemon's bridge",
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := json.Marshal(map[string]string{"project": project, "text": text})
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
				return fmt.Errorf("send failed: %s", resp.Status)
			}
			cmd.Println("sent")
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "target project name")
	cmd.Flags().StringVar(&text, "text", "", "message text")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8765", "daemon address")
	cmd.Flags().StringVar(&token, "token", "", "bridge token")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}
