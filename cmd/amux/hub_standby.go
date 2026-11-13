package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/wangning19940904/AgentMux/hubrelay"
)

// hubStandbyCmds are the commands for running two hubs, one standing by.
func hubStandbyCmds() []*cobra.Command {
	var disable bool
	standby := &cobra.Command{
		Use:   "standby <device-id>",
		Short: "Let a paired machine follow this hub's data, to take over if this hub goes away",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			var result map[string]any
			if err := daemonJSON(cmd.Context(), cfg, http.MethodPost, "/api/v1/hub/standby",
				map[string]any{"device_id": args[0], "enabled": !disable}, &result); err != nil {
				return err
			}
			if disable {
				fmt.Fprintf(cmd.OutOrStdout(), "%s no longer follows this hub.\n", args[0])
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s may now follow this hub. Set [hub] lease_secret on it too; it starts following when the relay tells it this hub is the primary.\n", args[0])
			return nil
		},
	}
	standby.Flags().BoolVar(&disable, "disable", false, "stop the machine from following")

	replication := &cobra.Command{
		Use:   "replication",
		Short: "Show this hub's role and how far its standby has caught up",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			var status json.RawMessage
			if err := daemonJSON(cmd.Context(), cfg, http.MethodGet, "/api/v1/hub/replication", nil, &status); err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(status)
		},
	}

	relay := &cobra.Command{
		Use:   "relay <config.toml>",
		Short: "Run the relay that forwards devices to whichever of two hubs holds the lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := hubrelay.LoadConfig(args[0])
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			r, err := hubrelay.New(cfg, log)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return r.Run(ctx)
		},
	}
	return []*cobra.Command{standby, replication, relay}
}
