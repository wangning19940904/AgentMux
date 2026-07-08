package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/provider"
	"github.com/spf13/cobra"
)

// providerProtocol reports the upstream wire protocol a provider speaks,
// defaulting to anthropic when unset (matches the proxy's fallback).
func providerProtocol(p *core.Provider) string {
	if p.Meta.APIFormat != "" {
		return p.Meta.APIFormat
	}
	return "anthropic"
}

func providerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage LLM providers (list / switch)",
	}
	cmd.AddCommand(providerListCmd())
	cmd.AddCommand(providerSwitchCmd())
	cmd.AddCommand(providerPresetsCmd())
	cmd.AddCommand(providerImportCmd())
	return cmd
}

func providerPresetsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "presets",
		Short: "List built-in provider presets",
		RunE: func(cmd *cobra.Command, args []string) error {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPROTOCOL\tBASE_URL")
			for _, p := range provider.Presets() {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.ID, p.Name, providerProtocol(p), p.BaseURL)
			}
			return tw.Flush()
		},
	}
}

func providerImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <preset-id>",
		Short: "Import a built-in preset as a provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, st, err := bootstrapStore()
			if err != nil {
				return err
			}
			defer st.Close()
			pm := provider.NewManager(st)
			p, err := pm.ImportPreset(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			cmd.Printf("imported provider %s (%s)\n", p.ID, p.Name)
			return nil
		},
	}
}

func providerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, st, err := bootstrapStore()
			if err != nil {
				return err
			}
			defer st.Close()
			pm := provider.NewManager(st)
			ps, err := pm.List(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPROTOCOL\tENABLED")
			for _, p := range ps {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", p.ID, p.Name, providerProtocol(p), p.Enabled)
			}
			return tw.Flush()
		},
	}
}

func providerSwitchCmd() *cobra.Command {
	var tool string
	cmd := &cobra.Command{
		Use:   "switch <provider-id>",
		Short: "Switch the active provider for a tool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := bootstrapStore()
			if err != nil {
				return err
			}
			defer st.Close()
			// Takeover-aware: switching a proxied tool hot-switches instead of
			// rewriting the live config away from the local routing proxy.
			svc := provider.NewService(logger, st, cfg.Provider.ProxyAddr)
			if err := svc.Switch(cmd.Context(), args[0], tool); err != nil {
				return err
			}
			cmd.Printf("switched %s -> provider %s\n", tool, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&tool, "tool", "claudecode", "target tool (claudecode/codex/gemini)")
	return cmd
}
