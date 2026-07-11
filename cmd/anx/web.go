package main

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

func webCmd() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the daemon and open the WebUI in a browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := bootstrap()
			if err != nil {
				return err
			}
			defer st.Close()

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			srv, providerSvc, usageEngine := newServer(cfg, st)
			eng, connectSvc, err := attachRuntime(ctx, cfg, st, srv, providerSvc, usageEngine)
			if err != nil {
				return err
			}
			if err := providerSvc.RestoreProxyState(ctx); err != nil {
				logger.Warn("local routing restore failed", "err", err)
			}
			defer func() { _ = providerSvc.Proxy().Stop() }()
			go func() { _ = srv.ListenAndServe(ctx) }()
			go func() { _ = eng.Start(ctx) }()
			if err := connectSvc.Start(ctx); err != nil {
				logger.Warn("connect runtime start failed", "err", err)
			}

			url := "http://" + cfg.Server.Addr
			if !noOpen {
				time.Sleep(300 * time.Millisecond)
				_ = openBrowser(url)
			}
			cmd.Println("WebUI:", url, "(Ctrl-C to stop)")
			<-ctx.Done()
			return nil
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
