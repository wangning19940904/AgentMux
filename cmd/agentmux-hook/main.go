// Command agentmux-hook is the fail-open native hook transport installed at
// ~/.agentmux/bin/agentmux-hook. It always exits successfully so a stopped
// or unhealthy observability collector cannot interrupt Claude Code or Codex.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wangning19940904/AgentMux/hookrelay"
)

func main() {
	_ = run(os.Args[1:], os.Stdin, os.Stderr)
}

func run(args []string, stdin io.Reader, stderr io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	opts := hookrelay.DefaultOptions(home)
	fs := flag.NewFlagSet("agentmux-hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Source, "source", opts.Source, "native hook source")
	fs.StringVar(&opts.SocketPath, "socket", opts.SocketPath, "collector Unix socket")
	fs.StringVar(&opts.SpoolDir, "spool-dir", opts.SpoolDir, "encrypted fallback spool directory")
	fs.StringVar(&opts.KeyPath, "key-file", opts.KeyPath, "32-byte spool encryption key")
	fs.DurationVar(&opts.Timeout, "timeout", hookrelay.MaxSocketWait, "socket delivery timeout (maximum 200ms)")
	if err := fs.Parse(args); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookrelay.MaxSocketWait+time.Second)
	defer cancel()
	_, relayErr := hookrelay.Relay(ctx, stdin, opts)
	if relayErr != nil && os.Getenv("AMUX_DEBUG") != "" {
		_, _ = fmt.Fprintf(stderr, "agentmux-hook: %v\n", relayErr)
	}
	return nil
}
