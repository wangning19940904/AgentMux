package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/diagnostics"
	"github.com/wangning19940904/AgentMux/framework"
	"github.com/wangning19940904/AgentMux/internal/postgressetup"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/store"
)

func diagnoseCmd() *cobra.Command {
	var (
		output         string
		includeContent bool
		turns          int
	)
	cmd := &cobra.Command{
		Use:   "diagnose",
		Short: "Write a redacted diagnostics zip for support (nothing is uploaded)",
		Long: "Write a local diagnostics zip: AgentMux version, OS and architecture, " +
			"the configuration with secrets removed, framework health, channels and agents, " +
			"the hub link and linked machines, recent turns and recent errors. " +
			"Credentials, tokens, cookies and SSH keys are never included, the home folder " +
			"is written as ~, and message content is left out unless --include-content is given. " +
			"The file stays on this machine; read it before sending it to anyone.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiagnose(cmd, output, includeContent, turns)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "zip path, or a folder to write it in (default: the current folder)")
	cmd.Flags().BoolVar(&includeContent, "include-content", false, "include prompts, titles and other message content")
	cmd.Flags().IntVar(&turns, "turns", 100, "how many recent turns to summarize")
	return cmd
}

func runDiagnose(cmd *cobra.Command, output string, includeContent bool, turns int) error {
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	home, _ := os.UserHomeDir()

	cfg, configPath, configErr := loadConfig(false)
	if configErr != nil {
		// A broken config.toml is a reason to diagnose, not to stop.
		fmt.Fprintf(stderr, "config.toml could not be read (%v); using defaults.\n", configErr)
		cfg = config.Default()
	}
	opts := diagnostics.Options{
		Version:        version,
		Config:         cfg,
		ConfigPath:     configPath,
		ConfigError:    configErr,
		Home:           home,
		Environ:        os.Environ(),
		IncludeContent: includeContent,
		RecentTurns:    turns,
		Origin:         "cli",
		LocalFrameworks: func(context.Context) (any, error) {
			return map[string]any{"prereqs": framework.DetectPrereqs(), "frameworks": framework.DetectAll()}, nil
		},
	}

	addr := linkpkg.LoopbackAddr(cfg.Server.Addr)
	fetch := diagnostics.HTTPFetcher(addr, cfg.Bridge.Token)
	if err := diagnostics.Probe(ctx, fetch); err != nil {
		opts.DaemonError = err
		if errors.Is(err, diagnostics.ErrNoDaemon) {
			fmt.Fprintf(stderr, "AgentMux is not running at %s: collecting what is available offline.\n", addr)
		} else {
			fmt.Fprintf(stderr, "%v: collecting what is available without it.\n", err)
		}
	} else {
		opts.Daemon = fetch
	}

	st, err := openDiagnosticsStore(ctx, cfg)
	if err != nil {
		opts.StoreError = err
		fmt.Fprintln(stderr, "The database could not be read: recent turns and errors are left out.")
	} else {
		defer st.Close()
		opts.Store = st
	}

	bundle, err := diagnostics.Build(ctx, opts)
	if err != nil {
		return err
	}
	path, err := diagnosticsPath(output, time.Now())
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, bundle.Zip, 0o600); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Files in the bundle:")
	width := 0
	for _, f := range bundle.Files {
		width = max(width, len(f.Name))
	}
	for _, f := range bundle.Files {
		fmt.Fprintf(out, "  %-*s  %s\n", width, f.Name, humanSize(f.Size))
	}
	if len(bundle.Notes) > 0 {
		fmt.Fprintln(out, "\nNot collected:")
		for _, n := range bundle.Notes {
			fmt.Fprintf(out, "  - %s\n", n)
		}
	}
	shown := path
	if abs, err := filepath.Abs(path); err == nil {
		shown = abs
	}
	fmt.Fprintf(out, "\nWrote %s (%s). Nothing was uploaded; check the files before sharing them.\n", shown, humanSize(len(bundle.Zip)))
	return nil
}

// openDiagnosticsStore connects to the database without starting or
// migrating it, and gives up quickly when it is not there.
func openDiagnosticsStore(ctx context.Context, cfg *config.Config) (*store.Store, error) {
	lifetime, err := time.ParseDuration(cfg.Database.ConnectionMaxLifetime)
	if err != nil {
		lifetime = 30 * time.Minute
	}
	url := cfg.Database.URL
	if flagDatabaseURL != "" {
		url = flagDatabaseURL
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return store.OpenPostgres(ctx, store.DatabaseConfig{
		URL:                   postgressetup.ResolveURL(url),
		MaxOpenConnections:    4,
		MaxIdleConnections:    1,
		ConnectionMaxLifetime: lifetime,
		SkipMigrations:        true,
	})
}

// diagnosticsPath resolves --output: empty means the current folder, an
// existing folder gets the default name, anything else is the file itself.
func diagnosticsPath(output string, now time.Time) (string, error) {
	name := diagnostics.FileName(now)
	output = strings.TrimSpace(output)
	if output == "" {
		return name, nil
	}
	if info, err := os.Stat(output); err == nil && info.IsDir() {
		return filepath.Join(output, name), nil
	}
	if strings.HasSuffix(output, string(os.PathSeparator)) {
		if err := os.MkdirAll(output, 0o755); err != nil {
			return "", err
		}
		return filepath.Join(output, name), nil
	}
	return output, nil
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
