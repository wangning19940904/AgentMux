package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/store"
)

func databaseCmd() *cobra.Command {
	command := &cobra.Command{Use: "database", Short: "Set up and migrate the PostgreSQL runtime store"}
	command.AddCommand(databaseSetupCmd())
	command.AddCommand(databaseMigrateSQLiteCmd())
	return command
}

func databaseSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Start local PostgreSQL, create the database, and apply schema migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			if flagDatabaseURL != "" {
				cfg.Database.URL = flagDatabaseURL
			}
			if cfg.Database.URL == store.DefaultPostgresURL {
				if err := ensureLocalPostgres(cmd.Context()); err != nil {
					return err
				}
			}
			st, err := openRuntimeStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			cmd.Println("PostgreSQL is ready; AgentMux schema migrations are current.")
			return nil
		},
	}
}

func databaseMigrateSQLiteCmd() *cobra.Command {
	var source, backup, sinceText string
	var dryRun, resume bool
	command := &cobra.Command{
		Use:   "migrate-sqlite",
		Short: "Offline-migrate a legacy SQLite store into PostgreSQL",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(source) == "" {
				source = store.DefaultPath()
			}
			expanded, err := config.ExpandPath(source)
			if err != nil {
				return err
			}
			sinceDuration, err := parseDayDuration(sinceText)
			if err != nil {
				return err
			}
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			if flagDatabaseURL != "" {
				cfg.Database.URL = flagDatabaseURL
			}
			st, err := openRuntimeStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			report, err := st.MigrateSQLite(cmd.Context(), store.SQLiteMigrationOptions{
				Source: expanded, BackupPath: backup, DryRun: dryRun,
				ObservationsSince: time.Now().UTC().Add(-sinceDuration), BatchSize: 5000, Resume: resume,
			})
			encoded, _ := json.MarshalIndent(report, "", "  ")
			cmd.Println(string(encoded))
			return err
		},
	}
	command.Flags().StringVar(&source, "source", store.DefaultPath(), "legacy SQLite database path")
	command.Flags().StringVar(&backup, "backup", "", "consistent backup destination (default timestamped next to source)")
	command.Flags().StringVar(&sinceText, "observations-since", "30d", "observation history window, for example 30d")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report selected rows without copying data")
	command.Flags().BoolVar(&resume, "resume", false, "resume an interrupted migration into the same PostgreSQL target")
	return command
}

func parseDayDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid day duration %q", value)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return duration, nil
}

func ensureLocalPostgres(ctx context.Context) error {
	if postgresReady(ctx) {
		return ensureAgentMuxDatabase(ctx)
	}
	brew, err := exec.LookPath("brew")
	if err != nil {
		return fmt.Errorf("PostgreSQL is not running and Homebrew is unavailable: %w", err)
	}
	output, err := exec.CommandContext(ctx, brew, "services", "start", "postgresql@16").CombinedOutput()
	if err != nil {
		return fmt.Errorf("start postgresql@16: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if postgresReady(ctx) {
			return ensureAgentMuxDatabase(ctx)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("postgresql@16 did not become ready on /tmp:5432")
}

func postgresReady(ctx context.Context) bool {
	command := exec.CommandContext(ctx, "pg_isready", "-h", "/tmp", "-p", "5432")
	return command.Run() == nil
}

func ensureAgentMuxDatabase(ctx context.Context) error {
	query := exec.CommandContext(ctx, "psql", "-h", "/tmp", "-d", "postgres", "-Atqc",
		`SELECT 1 FROM pg_database WHERE datname='agentmux'`)
	output, err := query.Output()
	if err != nil {
		return fmt.Errorf("inspect local PostgreSQL databases: %w", err)
	}
	if strings.TrimSpace(string(output)) == "1" {
		return nil
	}
	output, err = exec.CommandContext(ctx, "createdb", "-h", "/tmp", "agentmux").CombinedOutput()
	if err != nil {
		return fmt.Errorf("create agentmux database: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
