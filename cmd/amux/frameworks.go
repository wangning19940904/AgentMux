package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

func frameworksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "frameworks",
		Short: "Check agent frameworks on this machine",
	}
	cmd.AddCommand(frameworksModelsCmd(), frameworksTestCmd())
	return cmd
}

func frameworksModelsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "models <kind>",
		Short: "List the models a framework offers with its current configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agent, err := core.CreateAgent(args[0], map[string]any{})
			if err != nil {
				return err
			}
			defer func() { _ = agent.Stop(cmd.Context()) }()
			cataloger, ok := agent.(core.RuntimeSettingsCataloger)
			if !ok {
				return fmt.Errorf("%s does not report a model list", args[0])
			}
			workDir, _ := os.Getwd()
			defaults, capabilities, err := cataloger.RuntimeSettingsCatalog(cmd.Context(), workDir)
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"kind": args[0], "defaults": defaults, "capabilities": capabilities})
			}
			out := cmd.OutOrStdout()
			for _, model := range capabilities.Models {
				marker := "  "
				if model.Value == defaults.Model {
					marker = "* "
				}
				line := marker + model.Value
				if model.Label != "" && model.Label != model.Value {
					line += "  (" + model.Label + ")"
				}
				if efforts := capabilities.ForModel(model.Value).ReasoningEfforts; len(efforts) > 0 {
					line += "  effort:"
					for _, effort := range efforts {
						line += " " + effort.Value
					}
				}
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func frameworksTestCmd() *cobra.Command {
	var (
		jsonOut bool
		opts    framework.SmokeOptions
	)
	cmd := &cobra.Command{
		Use:   "test <kind>",
		Short: `Run a framework with a model and check that it replies "ok" (one short model call)`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Kind = args[0]
			result, err := framework.SmokeTest(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			} else {
				printSmokeResult(cmd, result)
			}
			if !result.OK {
				// The result above already explains the failure.
				cmd.SilenceUsage, cmd.SilenceErrors = true, true
				return errors.New("framework test failed")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Model, "model", "", "model ID to test (default: the framework's default)")
	cmd.Flags().StringVar(&opts.ReasoningEffort, "effort", "", "reasoning effort")
	cmd.Flags().StringVar(&opts.ServiceTier, "service-tier", "", "service tier")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", framework.DefaultSmokeTimeout, "give up after this long")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func printSmokeResult(cmd *cobra.Command, r framework.SmokeResult) {
	out := cmd.OutOrStdout()
	model := r.Model
	if model == "" {
		model = "(default)"
	}
	if r.ResolvedModel != "" && r.ResolvedModel != r.Model {
		model += " → " + r.ResolvedModel
	}
	latency := (time.Duration(r.LatencyMs) * time.Millisecond).Round(10 * time.Millisecond)
	if r.OK {
		fmt.Fprintf(out, "ok    %s  %s  %s\n", r.Kind, model, latency)
		return
	}
	fmt.Fprintf(out, "FAIL  %s  %s  %s  %s\n", r.Kind, model, latency, r.ErrorKind)
	if r.ErrorDetail != "" {
		fmt.Fprintf(out, "      %s\n", r.ErrorDetail)
	} else if r.Reply != "" {
		fmt.Fprintf(out, "      reply: %s\n", r.Reply)
	}
}
