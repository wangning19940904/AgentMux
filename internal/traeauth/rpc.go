package traeauth

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/internal/procutil"
)

// TRAE 0.202.3 publishes these methods in its generated app-server schema.
// Refresh through the CLI so credential rotation and secure writes stay under
// its control. Never copy provider URLs or tokens into an AgentMux HTTP client.
func refreshNative(ctx context.Context, extra map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "traecli", "app-server", "--listen", "stdio://")
	cmd.Env = commandEnv(extra)
	cmd.Dir = os.TempDir()
	cmd.Stderr = io.Discard // Raw auth diagnostics can include credentials.
	procutil.Prepare(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ErrRefreshUnavailable
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ErrRefreshUnavailable
	}
	if err := cmd.Start(); err != nil {
		return ErrRefreshUnavailable
	}
	defer func() {
		_ = stdin.Close()
		cancel()
		_ = cmd.Wait()
	}()
	return refreshRPC(stdin, stdout)
}

func refreshRPC(stdin io.Writer, stdout io.Reader) error {
	writer := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	call := func(id int, method string, params any, result any) error {
		if writer.Encode(map[string]any{"id": id, "method": method, "params": params}) != nil {
			return ErrRefreshUnavailable
		}
		for scanner.Scan() {
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
					Code    int    `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != id {
				continue
			}
			if response.Error != nil {
				message := strings.ToLower(response.Error.Message)
				for _, marker := range []string{"sign in again", "log in again", "not logged in", "invalid_grant", "refresh token expired", "refresh token revoked", "session could not be refreshed"} {
					if strings.Contains(message, marker) {
						return ErrLoginRequired
					}
				}
				return ErrRefreshUnavailable
			}
			if len(response.Result) == 0 || string(response.Result) == "null" {
				return ErrRefreshUnavailable
			}
			if result != nil && json.Unmarshal(response.Result, result) != nil {
				return ErrRefreshUnavailable
			}
			return nil
		}
		return ErrRefreshUnavailable
	}
	if err := call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "agentmux-auth-refresh", "version": "1"}}, nil); err != nil {
		return err
	}
	if writer.Encode(map[string]any{"method": "initialized"}) != nil {
		return ErrRefreshUnavailable
	}
	var account struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if err := call(2, "account/read", map[string]bool{"refreshToken": true}, &account); err != nil {
		return err
	}
	if account.Account == nil {
		return ErrLoginRequired
	}
	if account.Account.Type != "trae" {
		return ErrRefreshUnavailable
	}
	// A forced catalog refresh also exercises TRAE's provider auth path in
	// versions where account/read only refreshes credentials when already due.
	return call(3, "model/list", map[string]any{"forceRefresh": true, "limit": 1}, nil)
}
