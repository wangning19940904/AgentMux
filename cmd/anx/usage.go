package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentnexus/agentnexus/usage"
	"github.com/spf13/cobra"
)

func usageCmd() *cobra.Command {
	var (
		jsonOut bool
		sinceStr string
		withSSH  bool
	)
	cmd := &cobra.Command{
		Use:   "usage [daily|weekly|monthly|session|blocks]",
		Short: "Analyze token usage and estimated cost across coding agents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			period := "daily"
			if len(args) == 1 {
				period = args[0]
			}
			cfg, st, err := bootstrapStore()
			if err != nil {
				return err
			}
			defer st.Close()

			eng := usage.NewEngine(cfg, st, logger)

			since := time.Time{}
			if sinceStr != "" {
				since, err = usage.ParseSince(sinceStr)
				if err != nil {
					return err
				}
			}

			ctx := cmd.Context()
			if err := eng.Collect(ctx, since); err != nil {
				return err
			}
			if withSSH {
				if err := eng.CollectSSH(ctx, since); err != nil {
					return err
				}
			}
			rep, err := eng.Report(ctx, period, since)
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			fmt.Fprint(cmd.OutOrStdout(), usage.RenderTable(rep))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	cmd.Flags().StringVar(&sinceStr, "since", "", "filter since (e.g. 7d, 1w, 2026-01-01)")
	cmd.Flags().BoolVar(&withSSH, "ssh", false, "also collect from configured SSH targets")
	cmd.AddCommand(usageStatuslineCmd())
	return cmd
}

func usageStatuslineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "statusline",
		Short: "Print a compact one-line usage summary (for status bars/hooks)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := bootstrapStore()
			if err != nil {
				return err
			}
			defer st.Close()
			eng := usage.NewEngine(cfg, st, logger)
			if err := eng.Collect(cmd.Context(), time.Now().Truncate(24*time.Hour)); err != nil {
				return err
			}
			line, err := eng.Statusline(cmd.Context())
			if err != nil {
				return err
			}
			cmd.Println(line)
			return nil
		},
	}
}
