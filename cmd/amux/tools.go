package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	toolpkg "github.com/wangning19940904/AgentMux/tools"
)

func toolsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Manage supported local CLI tools",
	}
	cmd.AddCommand(toolsListCmd())
	cmd.AddCommand(toolsCheckCmd())
	cmd.AddCommand(toolsInstallCmd("install"))
	cmd.AddCommand(toolsInstallCmd("update"))
	return cmd
}

func toolsListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List supported local CLI tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			statuses := toolpkg.DetectCLIs(cmd.Context())
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(statuses)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tINSTALLED\tVERSION\tPATH")
			for _, status := range statuses {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					status.Spec.ID,
					status.Spec.Name,
					yesNo(status.Installed),
					status.Version,
					status.Path,
				)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func toolsCheckCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "check <id>",
		Short: "Check whether a supported CLI has an update",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result := toolpkg.CheckCLIUpdate(cmd.Context(), args[0])
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			} else {
				printCheckResult(cmd, result)
			}
			if result.Error != "" {
				return errors.New(result.Error)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func toolsInstallCmd(action string) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   action + " <id>",
		Short: actionLabel(action) + " a supported CLI tool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result := toolpkg.InstallCLI(cmd.Context(), args[0], action)
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			} else {
				printInstallResult(cmd, result)
			}
			if result.Error != "" {
				return errors.New(result.Error)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func printCheckResult(cmd *cobra.Command, result toolpkg.CLIUpdateCheck) {
	cmd.Printf("id: %s\n", result.ID)
	cmd.Printf("installed: %s\n", yesNo(result.Installed))
	if result.CurrentVersion != "" {
		cmd.Printf("current: %s\n", result.CurrentVersion)
	}
	if result.LatestVersion != "" {
		cmd.Printf("latest: %s\n", result.LatestVersion)
	}
	cmd.Printf("update_available: %s\n", yesNo(result.UpdateAvailable))
	if result.Error != "" {
		cmd.Printf("error: %s\n", result.Error)
	}
}

func printInstallResult(cmd *cobra.Command, result toolpkg.CLIInstallResult) {
	cmd.Printf("%s %s: %s\n", result.Action, result.ID, yesNo(result.OK))
	if result.Command != "" {
		cmd.Printf("command: %s\n", result.Command)
	}
	if result.Version != "" {
		cmd.Printf("version: %s\n", result.Version)
	}
	if result.Log != "" {
		cmd.Println(result.Log)
	}
	if result.Error != "" {
		cmd.Printf("error: %s\n", result.Error)
	}
}

func actionLabel(action string) string {
	if action == "update" {
		return "Update"
	}
	return "Install"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
