package cli

import (
	"fmt"
	"os"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/monitor"
	"github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newMonitorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "monitor",
		Short:  "Monitor commands (internal)",
		Hidden: true,
	}
	cmd.AddCommand(newPatrolCmd())
	return cmd
}

func newPatrolCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "patrol",
		Short: "Check for stale, failed, or stuck work",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd monitor patrol is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			result, err := monitor.Patrol(cmd.Context(), client, cfg)
			if err != nil {
				return err
			}

			fmt.Printf("Patrol complete: %d stale, %d failed, %d redispatched, %d escalated, %d stuck PRs\n",
				result.StaleIssues, result.FailedIssues, result.RedispatchedCount, result.EscalatedCount, result.StuckPRs)
			return nil
		},
	}
}
