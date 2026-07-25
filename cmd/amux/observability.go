package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
	observationpkg "github.com/wangning19940904/AgentMux/observability"
)

func observabilityCmd() *cobra.Command {
	command := &cobra.Command{Use: "observability", Short: "Maintain local observability data"}
	command.AddCommand(observabilityMigrateTranscriptPayloadsCmd())
	return command
}

func observabilityMigrateTranscriptPayloadsCmd() *cobra.Command {
	var batchSize int
	command := &cobra.Command{
		Use:   "migrate-transcript-payloads",
		Short: "Replace duplicated transcript bodies with verified local-file references",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := bootstrapStore()
			if err != nil {
				return err
			}
			defer st.Close()
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			runtime, err := observationpkg.NewRuntime(logger, cfg.Observability, st, home, nil)
			if err != nil {
				return err
			}
			lastReported := int64(0)
			result, err := runtime.MigrateTranscriptPayloadReferences(cmd.Context(), batchSize, func(progress observationpkg.TranscriptPayloadMigrationResult) {
				if progress.Scanned-lastReported < 10_000 {
					return
				}
				lastReported = progress.Scanned
				encoded, _ := json.Marshal(progress)
				cmd.PrintErrln(string(encoded))
			})
			if err != nil {
				return err
			}
			encoded, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			cmd.Println(string(encoded))
			return nil
		},
	}
	command.Flags().IntVar(&batchSize, "batch-size", 256, "events validated and replaced per transaction")
	return command
}
