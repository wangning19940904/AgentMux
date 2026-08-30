package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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
	cmd.AddCommand(toolsInstallCmd("uninstall"))
	cmd.AddCommand(toolsBundleCmd())
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
	var yes bool
	cmd := &cobra.Command{
		Use:   action + " <id>",
		Short: actionLabel(action) + " a supported CLI tool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, known := toolpkg.LookupCLI(args[0])
			if action == "install" && known && spec.InternalOnly && !yes {
				if jsonOut {
					return errors.New("--yes is required to install ByteDance-internal tools with --json")
				}
				if err := confirmInternalInstall(cmd, spec.Name); err != nil {
					return err
				}
				yes = true
			}
			result := toolpkg.InstallCLIWithOptions(cmd.Context(), args[0], action, toolpkg.CLIInstallOptions{AcknowledgeInternal: yes || !known || !spec.InternalOnly})
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
	cmd.Flags().BoolVar(&yes, "yes", false, "acknowledge internal-only availability without prompting")
	return cmd
}

func toolsBundleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "bundle", Short: "Manage curated tool bundles"}
	cmd.AddCommand(toolsBundleListCmd(), toolsBundleInstallCmd())
	return cmd
}

func toolsBundleListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use: "list", Short: "List curated tool bundles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			statuses := toolpkg.DetectBundles(cmd.Context())
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(statuses)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tREADY\tTOTAL\tINTERNAL")
			for _, status := range statuses {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", status.Spec.ID, status.Spec.Name, status.ReadyComponents, status.TotalComponents, yesNo(status.Spec.InternalOnly))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func toolsBundleInstallCmd() *cobra.Command {
	var jsonOut bool
	var yes bool
	cmd := &cobra.Command{
		Use: "install <id>", Short: "Install every missing component in a curated bundle", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, known := toolpkg.LookupBundle(args[0])
			if known && spec.InternalOnly && !yes {
				if jsonOut {
					return errors.New("--yes is required to install ByteDance-internal bundles with --json")
				}
				if err := confirmInternalInstall(cmd, spec.Name); err != nil {
					return err
				}
				yes = true
			}
			result := toolpkg.InstallBundle(cmd.Context(), args[0], toolpkg.BundleInstallOptions{AcknowledgeInternal: yes})
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			} else {
				cmd.Printf("install bundle %s: %s\n", result.ID, yesNo(result.OK))
				for _, component := range result.Components {
					state := yesNo(component.OK)
					if component.Skipped {
						state = "skipped"
					}
					cmd.Printf("  %s/%s: %s", component.Kind, component.ID, state)
					if component.Version != "" {
						cmd.Printf(" (%s)", component.Version)
					}
					cmd.Println()
					if component.Error != "" {
						cmd.Printf("    error: %s\n", component.Error)
					}
				}
				if result.Error != "" {
					cmd.Printf("error: %s\n", result.Error)
				}
			}
			if result.Error != "" {
				return errors.New(result.Error)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	cmd.Flags().BoolVar(&yes, "yes", false, "acknowledge internal-only availability without prompting")
	return cmd
}

func confirmInternalInstall(cmd *cobra.Command, name string) error {
	if file, ok := cmd.InOrStdin().(*os.File); ok {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return errors.New("--yes is required for non-interactive ByteDance-internal installation")
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s is only available inside ByteDance and requires internal accounts, network, and registries. Continue? [y/N] ", name)
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && strings.TrimSpace(answer) == "" {
		return errors.New("installation cancelled; use --yes to acknowledge internal-only availability")
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return errors.New("installation cancelled")
	}
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
	if action == "uninstall" {
		return "Uninstall"
	}
	return "Install"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
