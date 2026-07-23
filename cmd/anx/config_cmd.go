package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	configpkg "github.com/agentnexus/agentnexus/config"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage AgentNexus config files",
	}
	cmd.AddCommand(configInitCmd())
	cmd.AddCommand(configPathCmd())
	return cmd
}

func configInitCmd() *cobra.Command {
	var (
		force bool
		print bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a starter config.toml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if print {
				cmd.Print(configpkg.ExampleConfig())
				return nil
			}
			path, err := configInitPath()
			if err != nil {
				return err
			}
			if !force {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(configpkg.ExampleConfig()), 0o600); err != nil {
				return err
			}
			cmd.Println("wrote config:", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	cmd.Flags().BoolVar(&print, "print", false, "print the starter config to stdout")
	return cmd
}

func configPathCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Show the resolved config path",
		RunE: func(cmd *cobra.Command, args []string) error {
			candidates, err := configpkg.CandidatePaths(flagConfig)
			if err != nil {
				return err
			}
			path, _, err := configpkg.ResolvePath(flagConfig)
			info := struct {
				Found      bool     `json:"found"`
				Path       string   `json:"path,omitempty"`
				Candidates []string `json:"candidates"`
			}{
				Candidates: candidates,
			}
			if err == nil {
				info.Found = true
				info.Path = path
			} else if !configpkg.IsNotFound(err) {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			if info.Found {
				cmd.Println(info.Path)
				return nil
			}
			cmd.Println("config not found")
			cmd.Println("searched:")
			for _, candidate := range info.Candidates {
				cmd.Println("  " + candidate)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func configInitPath() (string, error) {
	if flagConfig != "" {
		return configpkg.ExpandPath(flagConfig)
	}
	if envPath := os.Getenv(configpkg.EnvPath); envPath != "" {
		return configpkg.ExpandPath(envPath)
	}
	return configpkg.ExpandPath(configpkg.DefaultPath())
}
