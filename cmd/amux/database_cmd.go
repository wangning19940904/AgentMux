package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/internal/postgressetup"
	"github.com/wangning19940904/AgentMux/migration/configimport"
	"github.com/wangning19940904/AgentMux/store"
)

func databaseCmd() *cobra.Command {
	command := &cobra.Command{Use: "database", Short: "Set up and migrate the PostgreSQL runtime store"}
	command.AddCommand(databaseSetupCmd())
	command.AddCommand(databaseMigrateSQLiteCmd())
	command.AddCommand(databaseImportConfigCmd())
	return command
}

func databaseImportConfigCmd() *cobra.Command {
	var dryRun, apply bool
	command := &cobra.Command{
		Use:   "import-config",
		Short: "Import legacy config.toml projects and hooks into PostgreSQL",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun == apply {
				return fmt.Errorf("choose exactly one of --dry-run or --apply")
			}
			cfg, path, err := loadConfig(true)
			if err != nil {
				return err
			}
			st, err := openRuntimeStore(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			report, importErr := configimport.Import(cmd.Context(), st, cfg, dryRun)
			encoded, _ := json.MarshalIndent(map[string]any{"source": path, "report": report}, "", "  ")
			cmd.Println(string(encoded))
			return importErr
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "show creates, unchanged resources, and conflicts without writing")
	command.Flags().BoolVar(&apply, "apply", false, "atomically write the imported resources")
	return command
}

func databaseSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install missing PostgreSQL dependencies, start it, and initialize the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(false)
			if err != nil {
				return err
			}
			if flagDatabaseURL != "" {
				cfg.Database.URL = flagDatabaseURL
			}
			if err := postgressetup.Ensure(cmd.Context(), cfg.Database.URL, cmd.ErrOrStderr()); err != nil {
				return err
			}
			st, err := openRuntimeStore(cmd.Context(), cfg)
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
			st, err := openRuntimeStore(cmd.Context(), cfg)
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
