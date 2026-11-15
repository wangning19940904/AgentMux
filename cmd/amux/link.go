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
	"github.com/wangning19940904/AgentMux/core"
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
		listen     string
		addrs      []string
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
			"it without a token to reconnect a machine that has already joined.\n\n" +
			"With --listen this machine instead listens for the hub to dial it (a passive " +
			"link), for a machine the hub can reach when the hub cannot be reached from " +
			"here. It prints an invite for the hub's administrator: `amux hub add <invite>` " +
			"on the hub, or Add a listening machine in its Console.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("listen") {
				if len(args) > 0 || foreground {
					return errors.New("--listen takes no join token and runs in the running AgentMux")
				}
				return listenForHub(cmd, cfg, listen, addrs)
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
	cmd.Flags().StringVar(&listen, "listen", "", "listen for the hub to dial this machine, on host:port (default [hub] passive_listen, the last one used, or :8767)")
	cmd.Flags().Lookup("listen").NoOptDefVal = " "
	cmd.Flags().StringArrayVar(&addrs, "addr", nil, "with --listen: host:port the hub dials, best first (repeatable; default this machine's addresses)")
	return cmd
}

// listenForHub makes the running AgentMux listen for its hub and prints the
// invite. The invite alone goes to stdout, so it can be piped.
func listenForHub(cmd *cobra.Command, cfg *config.Config, listen string, addrs []string) error {
	var invited struct {
		Invite      string    `json:"invite"`
		Addrs       []string  `json:"addrs"`
		Fingerprint string    `json:"fingerprint"`
		Listen      string    `json:"listen"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	err := daemonJSON(cmd.Context(), cfg, http.MethodPost, "/api/v1/link/passive",
		map[string]any{"listen": strings.TrimSpace(listen), "addrs": addrs}, &invited)
	if errors.Is(err, errNoDaemon) {
		return fmt.Errorf("%w; start it first: the running AgentMux listens for the hub", err)
	}
	if err != nil {
		return err
	}
	stderr := cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "Listening for the hub on %s. Key fingerprint (SHA-256 SPKI): %s\n", invited.Listen, invited.Fingerprint)
	fmt.Fprintf(stderr, "The hub will dial %s.\n", strings.Join(invited.Addrs, ", "))
	fmt.Fprintf(stderr, "On the hub, within %d minutes, run `amux hub add <invite>` with this invite (or paste it in the Console):\n",
		int(time.Until(invited.ExpiresAt).Round(time.Minute).Minutes()))
	fmt.Fprintln(cmd.OutOrStdout(), invited.Invite)
	fmt.Fprintln(stderr, "Open this port to the hub's address only. The invite pairs whoever uses it first; keep it private.")
	return nil
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
		state, err = linkpkg.Pair(ctx, join, localDeviceInfo(cfg, name))
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
		State: state, Info: localDeviceInfo(cfg, state.Name),
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
	var passive bool
	cmd := &cobra.Command{
		Use:   "leave",
		Short: "Disconnect this machine from its AgentMux hub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			if passive {
				err := daemonJSON(cmd.Context(), cfg, http.MethodDelete, "/api/v1/link/passive", nil, nil)
				if errors.Is(err, errNoDaemon) {
					path, pathErr := linkpkg.PassiveStatePath(cfg.Hub.StateDir)
					if pathErr != nil {
						return pathErr
					}
					err = linkpkg.RemoveState(path)
				}
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Stopped listening for the hub. It still lists this machine, offline, until it is removed there.")
				return nil
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
	cmd.Flags().BoolVar(&passive, "passive", false, "stop listening for a hub that dials this machine, and forget it")
	return cmd
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
					LinkMode   string     `json:"link_mode"`
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
				if device.LinkMode == "passive" {
					state += " (this hub dials it)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-24s %-28s %s\n", device.ID, device.Name,
					strings.TrimSpace(device.OS+"/"+device.Arch+" "+device.Version), state)
			}
			return nil
		},
	}

	var (
		addName  string
		addAddrs []string
	)
	add := &cobra.Command{
		Use:   "add <invite>",
		Short: "Add a machine that listens, from its invite; this hub dials it",
		Long: "Add a machine that cannot reach this hub but that this hub can reach. On that " +
			"machine, `amux join --listen` prints an invite: its addresses, the fingerprint of " +
			"its key, and a one-time secret. This hub dials it, trusts only that key, pairs, " +
			"and keeps the connection up. A machine that already dials this hub is refused: " +
			"remove it first.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			var added struct {
				Device struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"device"`
				Replaced bool `json:"replaced"`
				Online   bool `json:"online"`
				Passive  struct {
					Addrs     []string `json:"addrs"`
					LastError string   `json:"last_error"`
				} `json:"passive"`
			}
			body := map[string]any{"invite": args[0], "name": addName, "addrs": addAddrs}
			if err := daemonJSON(cmd.Context(), cfg, http.MethodPost, "/api/v1/devices/passive", body, &added); err != nil {
				return err
			}
			verb := "Added"
			if added.Replaced {
				verb = "Updated"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %q; this hub dials it at %s.\n", verb, added.Device.ID, added.Device.Name, strings.Join(added.Passive.Addrs, ", "))
			if !added.Online {
				reason := added.Passive.LastError
				if reason == "" {
					reason = "still dialing"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "It is not connected yet: %s\n", reason)
			}
			return nil
		},
	}
	add.Flags().StringVar(&addName, "name", "", "what to call the machine (default: its hostname)")
	add.Flags().StringArrayVar(&addAddrs, "addr", nil, "host:port to dial instead of the invite's addresses (repeatable, best first)")

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
	cmd.AddCommand(pair, add, devices, remove)
	cmd.AddCommand(hubStandbyCmds()...)
	return cmd
}

// localDeviceInfo describes this machine to a hub, with its machine UID when
// it can be read.
func localDeviceInfo(cfg *config.Config, name string) core.DeviceInfo {
	info := linkpkg.LocalDeviceInfo(name, version)
	if uid, err := linkpkg.LoadOrCreateMachineUID(cfg.Hub.StateDir); err == nil {
		info.MachineUID = uid
	} else {
		logger.Warn("machine UID unavailable", "err", err)
	}
	return info
}

func machineUIDCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "machine-uid",
		Short: "Print the identity this machine reports to other machines",
		Long: "Print this machine's UID. Every way of reaching a machine (SSH, its own hub " +
			"link, or the hub's relay) reports it, so a hub or controller that knows the " +
			"machine more than one way lists it once. It is created on first run in the " +
			"hub state directory.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			uid, err := linkpkg.LoadOrCreateMachineUID(cfg.Hub.StateDir)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), uid)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "reset",
		Short: "Give this machine a new UID, as for a cloned VM",
		Long: "Replace this machine's UID with a new one. A VM cloned from another keeps its " +
			"state directory and so reports the same UID, which makes the two look like one " +
			"machine. Run this on the clone, then restart its AgentMux.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			uid, err := linkpkg.ResetMachineUID(cfg.Hub.StateDir)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "This machine's UID is now %s. Restart AgentMux to report it.\n", uid)
			return nil
		},
	})
	return cmd
}
