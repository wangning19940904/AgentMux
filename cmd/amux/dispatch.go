package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wangning19940904/AgentMux/superagent"
)

// dispatchCmd is how a worker running a Ticket starts and reads helpers: it
// calls this machine's AgentMux, which passes the call to the agent's machine
// when the Ticket lives elsewhere. The Ticket's token is the credential.
func dispatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Start and read helpers for the Ticket you are working on",
		Long: "A worker whose agent allows dispatch is given a token and these commands in its Ticket's first input. " +
			"start hands a sub-task to another worker on the agent's roster and prints the helper's id; " +
			"read prints the helper's state and, once it finished, its final answer.",
	}
	cmd.AddCommand(dispatchStartCmd(), dispatchReadCmd())
	return cmd
}

type dispatchFlags struct {
	addr, token string
	json        bool
}

func (f *dispatchFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.addr, "addr", "", "this machine's AgentMux address (default from config)")
	cmd.Flags().StringVar(&f.token, "token", "", "the Ticket's dispatch token")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the answer as JSON")
}

func dispatchStartCmd() *cobra.Command {
	var flags dispatchFlags
	var request superagent.DispatchRequest
	var instructionsFile string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a helper on another worker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if instructionsFile != "" {
				var data []byte
				var err error
				if instructionsFile == "-" {
					data, err = io.ReadAll(cmd.InOrStdin())
				} else {
					data, err = os.ReadFile(instructionsFile)
				}
				if err != nil {
					return err
				}
				request.Instructions = string(data)
			}
			request.Token = flags.token
			result, err := callDispatch(cmd.Context(), flags.addr, superagent.DispatchStartPath, request)
			if err != nil {
				return err
			}
			return printDispatch(cmd, result, flags.json)
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&request.Worker, "worker", "", "the worker to run the helper on, as the Ticket's brief lists them")
	cmd.Flags().StringVar(&request.Runtime, "runtime", "", "short for --worker auto:<runtime>")
	cmd.Flags().StringVar(&request.Title, "title", "", "a few words naming the sub-task")
	cmd.Flags().StringVar(&request.Instructions, "instructions", "", "what the helper should do and report back")
	cmd.Flags().StringVar(&instructionsFile, "instructions-file", "", "read the instructions from a file, or - for standard input")
	return cmd
}

func dispatchReadCmd() *cobra.Command {
	var flags dispatchFlags
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "read <helper id>",
		Short: "Read a helper's state and result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deadline := time.Now().Add(wait)
			for {
				request := superagent.DispatchRequest{Token: flags.token, ID: args[0]}
				if left := time.Until(deadline); left > 0 {
					request.WaitSeconds = int(min(left, 30*time.Second).Seconds()) + 1
				}
				result, err := callDispatch(cmd.Context(), flags.addr, superagent.DispatchReadPath, request)
				if err != nil {
					return err
				}
				if result.Status != "running" || time.Until(deadline) <= 0 {
					return printDispatch(cmd, result, flags.json)
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(time.Second):
				}
			}
		},
	}
	flags.register(cmd)
	cmd.Flags().DurationVar(&wait, "wait", 0, "wait up to this long for a running helper to finish, such as 10m")
	return cmd
}

func callDispatch(ctx context.Context, addr, path string, request superagent.DispatchRequest) (superagent.DispatchResult, error) {
	var result superagent.DispatchResult
	if strings.TrimSpace(request.Token) == "" {
		return result, errors.New("--token is required: use the token from your Ticket's brief")
	}
	if addr == "" {
		cfg, _, err := loadConfig(false)
		if err != nil {
			return result, err
		}
		addr = cfg.Server.Addr
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	body, _ := json.Marshal(request)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(addr, "/")+path, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 3 * time.Minute}).Do(httpRequest)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return result, fmt.Errorf("dispatch refused (%s): %s", response.Status, failure.Error)
		}
		return result, fmt.Errorf("dispatch failed: %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("read the answer: %w", err)
	}
	return result, nil
}

func printDispatch(cmd *cobra.Command, result superagent.DispatchResult, asJSON bool) error {
	out := cmd.OutOrStdout()
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Fprintf(out, "helper: %s\nworker: %s\nstatus: %s\n", result.ID, result.WorkerID, result.Status)
	if result.Title != "" {
		fmt.Fprintf(out, "title: %s\n", result.Title)
	}
	fmt.Fprintf(out, "helpers running for this Ticket: %d\n", result.Running)
	if result.Error != "" {
		fmt.Fprintf(out, "error: %s\n", result.Error)
	}
	if result.Result != "" {
		fmt.Fprintf(out, "\n%s\n", result.Result)
	}
	return nil
}
