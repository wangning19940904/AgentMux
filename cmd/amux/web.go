package main

import (
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

func webCmd() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the daemon and open the WebUI in a browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd, daemonOptions{printWebUI: true, openWebUI: !noOpen, allowDefault: true})
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser")
	return cmd
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
