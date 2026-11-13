package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/wangning19940904/AgentMux/config"
	linkpkg "github.com/wangning19940904/AgentMux/link"
)

// errNoDaemon means the local AgentMux did not answer, or is a version from
// before the hub link existed.
var errNoDaemon = errors.New("the local AgentMux is not running or does not support the hub link")

// daemonJSON calls the AgentMux running on this machine.
func daemonJSON(ctx context.Context, cfg *config.Config, method, path string, body, output any) error {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+linkpkg.LoopbackAddr(cfg.Server.Addr)+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if cfg.Bridge.Enabled && cfg.Bridge.Token != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.Bridge.Token)
	}
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
	if err != nil {
		return errNoDaemon
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	// An older daemon answers these paths with 404 or 405.
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return errNoDaemon
	}
	if response.StatusCode >= 300 {
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error != "" {
			return errors.New(envelope.Error)
		}
		return fmt.Errorf("the local AgentMux answered %s", response.Status)
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal(data, output)
}

func joinCmd() *cobra.Command {
	var (
		name       string
		replace    bool
		foreground bool
	)
	cmd := &cobra.Command{
		Use:   "join [join-token]",
		Short: "Connect this machine to an AgentMux hub",
		Long: "Connect this machine to an AgentMux hub with a join token from the hub " +
			"(`amux hub pair`, or Machines in its Console). This machine opens the " +
			"connection, so it needs no public address. Whoever runs the hub can then " +
			"use this machine's AgentMux as its administrator does.\n\n" +
			"The running AgentMux keeps the connection. With --foreground this command " +
			"keeps it instead, for a machine whose AgentMux predates the hub link; run " +
			"it without a token to reconnect a machine that has already joined.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			token := ""
			if len(args) == 1 {
				token = args[0]
			}
			if !foreground {
				if token == "" {
					return errors.New("a join token is required; create one on the hub with `amux hub pair`")
				}
				var joined struct {
					HubURL string `json:"hub_url"`
					Name   string `json:"name"`
				}
				err := daemonJSON(cmd.Context(), cfg, http.MethodPost, "/api/v1/link/join",
					map[string]any{"token": token, "name": name, "replace": replace}, &joined)
				if errors.Is(err, errNoDaemon) {
					return fmt.Errorf("%w; start it, or keep the connection from this command with --foreground", err)
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Joined the hub at %s as %q. The running AgentMux keeps the connection.\n", joined.HubURL, joined.Name)
				return nil
			}
			return joinForeground(cmd, cfg, token, name, replace)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "what the hub calls this machine (default: its hostname, or the name chosen on the hub)")
	cmd.Flags().BoolVar(&replace, "replace", false, "leave the hub this machine already belongs to")
	cmd.Flags().BoolVar(&foreground, "foreground", false, "keep the connection from this command instead of the running AgentMux")
	return cmd
}

func joinForeground(cmd *cobra.Command, cfg *config.Config, token, name string, replace bool) error {
	// Two holders of one membership would keep taking the connection from
	// each other.
	var status struct {
		Joined bool `json:"joined"`
	}
	if err := daemonJSON(cmd.Context(), cfg, http.MethodGet, "/api/v1/link", nil, &status); err == nil {
		return errors.New("the running AgentMux keeps the hub connection itself; run `amux join` without --foreground")
	}
	path, err := linkpkg.StatePath(cfg.Hub.StateDir)
	if err != nil {
		return err
	}
	state, joined, err := linkpkg.LoadState(path)
	if err != nil {
		return err
	}
	switch {
	case token == "" && !joined:
		return errors.New("this machine has not joined a hub; pass a join token")
	case token != "" && joined && !replace:
		return fmt.Errorf("this machine already belongs to the hub at %s; pass --replace to leave it", state.HubURL)
	case token != "":
		join, err := linkpkg.ParseJoinToken(token)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
		state, err = linkpkg.Pair(ctx, join, linkpkg.LocalDeviceInfo(name, version))
		cancel()
		if err != nil {
			return err
		}
		if err := linkpkg.SaveState(path, state); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Joined the hub at %s as %q.\n", state.HubURL, state.Name)
	}
	apiToken := ""
	if cfg.Bridge.Enabled {
		apiToken = cfg.Bridge.Token
	}
	client := &linkpkg.Client{
		State: state, Info: linkpkg.LocalDeviceInfo(state.Name, version),
		Handler: linkpkg.LocalAPIHandler(linkpkg.LoopbackAddr(cfg.Server.Addr), apiToken), Log: logger,
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(cmd.OutOrStdout(), "Keeping the connection to %s. Press Ctrl-C to stop.\n", state.HubURL)
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func leaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "leave",
		Short: "Disconnect this machine from its AgentMux hub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			err = daemonJSON(cmd.Context(), cfg, http.MethodPost, "/api/v1/link/leave", nil, nil)
			if errors.Is(err, errNoDaemon) {
				path, pathErr := linkpkg.StatePath(cfg.Hub.StateDir)
				if pathErr != nil {
					return pathErr
				}
				err = linkpkg.RemoveState(path)
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Left the hub. It still lists this machine, offline, until it is removed there.")
			return nil
		},
	}
}

func hubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Pair and manage the machines connected to this AgentMux as their hub",
	}
	var (
		name   string
		hubURL string
		pin    bool
	)
	pair := &cobra.Command{
		Use:   "pair",
		Short: "Create a one-time join token for another machine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "hub_url": hubURL}
			if cmd.Flags().Changed("pin") {
				body["pin"] = pin
			}
			var created struct {
				ExpiresAt time.Time `json:"expires_at"`
				HubURL    string    `json:"hub_url"`
				Command   string    `json:"command"`
				Warning   string    `json:"warning"`
			}
			if err := daemonJSON(cmd.Context(), cfg, http.MethodPost, "/api/v1/devices/pairing-codes", body, &created); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Run this on the machine to connect, within %d minutes:\n\n  %s\n\nIt will dial %s.\n",
				int(time.Until(created.ExpiresAt).Round(time.Minute).Minutes()), created.Command, created.HubURL)
			if created.Warning != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "\nNote: %s\n", created.Warning)
			}
			return nil
		},
	}
	pair.Flags().StringVar(&name, "name", "", "what to call the machine (default: its hostname)")
	pair.Flags().StringVar(&hubURL, "hub-url", "", "address the machine dials, when it is not the configured one")
	pair.Flags().BoolVar(&pin, "pin", false, "with --hub-url: that address presents the hub's own certificate")

	devices := &cobra.Command{
		Use:   "devices",
		Short: "List the machines paired with this hub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			var listed struct {
				Devices []struct {
					ID         string     `json:"id"`
					Name       string     `json:"name"`
					OS         string     `json:"os"`
					Arch       string     `json:"arch"`
					Version    string     `json:"version"`
					Online     bool       `json:"online"`
					LastSeenAt *time.Time `json:"last_seen_at"`
				} `json:"devices"`
			}
			if err := daemonJSON(cmd.Context(), cfg, http.MethodGet, "/api/v1/devices", nil, &listed); err != nil {
				return err
			}
			if len(listed.Devices) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No machines are paired. Create a join token with `amux hub pair`.")
				return nil
			}
			for _, device := range listed.Devices {
				state := "offline"
				switch {
				case device.Online:
					state = "online"
				case device.LastSeenAt != nil:
					state = "offline since " + device.LastSeenAt.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-24s %-28s %s\n", device.ID, device.Name,
					strings.TrimSpace(device.OS+"/"+device.Arch+" "+device.Version), state)
			}
			return nil
		},
	}

	remove := &cobra.Command{
		Use:   "remove <device-id>",
		Short: "Unpair a machine; its connection closes and its token stops working",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			return daemonJSON(cmd.Context(), cfg, http.MethodDelete, "/api/v1/devices?id="+args[0], nil, nil)
		},
	}
	cmd.AddCommand(pair, devices, remove)
	cmd.AddCommand(hubStandbyCmds()...)
	return cmd
}
