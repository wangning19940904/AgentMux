package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagConfig string
	flagDB     string
	logger     *slog.Logger
	// version is overridable at build time via -ldflags "-X main.version=...".
	version = "0.1.0"
)

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "anx",
		Short: "AgentNexus — one control plane for chat-driven coding agents",
		Long: "AgentNexus (智枢) is one control plane for chat-driven coding agents: " +
			"it connects IM platforms, routes agents, tracks token usage, and unifies " +
			"memory, skills, MCP servers and permission approvals across tools.",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			level := slog.LevelInfo
			if os.Getenv("ANX_DEBUG") != "" {
				level = slog.LevelDebug
			}
			logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
		},
	}
	root.PersistentFlags().StringVarP(&flagConfig, "config", "c", "", "path to config.toml (default: ./config.toml, $XDG_CONFIG_HOME/agentnexus/config.toml, /etc/agentnexus/config.toml)")
	root.PersistentFlags().StringVar(&flagDB, "db", "", "path to SQLite db (default ~/.agentnexus/agentnexus.db)")

	root.AddCommand(clientCmd())
	root.AddCommand(serveCmd())
	root.AddCommand(webCmd())
	root.AddCommand(configCmd())
	root.AddCommand(toolsCmd())
	root.AddCommand(usageCmd())
	root.AddCommand(providerCmd())
	root.AddCommand(observabilityCmd())
	root.AddCommand(sendCmd())
	root.AddCommand(versionCmd())
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println("anx " + version)
		},
	}
}
