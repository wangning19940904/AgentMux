package main

import "github.com/spf13/cobra"

func clientCmd() *cobra.Command {
	var (
		addr string
		web  bool
		open bool
	)
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Run the headless Linux client",
		Long: "Run AgentNexus as a headless Linux client. It starts the local " +
			"daemon, serves the management API, and can expose the WebUI URL " +
			"without requiring a desktop shell.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if open {
				web = true
			}
			return runDaemon(cmd, daemonOptions{
				addrOverride: addr,
				printConfig:  true,
				printReady:   true,
				printWebUI:   web,
				openWebUI:    open,
				allowDefault: true,
			})
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "override server listen address from config")
	cmd.Flags().BoolVar(&web, "web", false, "print the WebUI URL after startup")
	cmd.Flags().BoolVar(&open, "open", false, "open the WebUI in a browser")
	return cmd
}
