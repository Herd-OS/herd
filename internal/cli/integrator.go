package cli

import (
	"fmt"
	"os"

	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newIntegratorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "integrator",
		Short:  "Integrator commands (internal)",
		Hidden: true,
	}
	cmd.AddCommand(newConsolidateCmd())
	cmd.AddCommand(newAdvanceCmd())
	cmd.AddCommand(newIntegratorReviewCmd())
	return cmd
}

func newConsolidateCmd() *cobra.Command {
	var runID int64

	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "Merge a completed worker branch into the batch branch",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator consolidate is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			cwd, _ := os.Getwd()
			g := git.New(cwd)

			result, err := integrator.Consolidate(cmd.Context(), client, g, cfg, integrator.ConsolidateParams{
				RunID:    runID,
				RepoRoot: cwd,
			})
			if err != nil {
				return err
			}

			if result.NoOp {
				fmt.Printf("No-op: issue #%d had no worker branch (already done)\n", result.IssueNumber)
			} else if result.Merged {
				fmt.Printf("Consolidated %s into batch branch\n", result.WorkerBranch)
			} else {
				fmt.Printf("Skipped: issue #%d run failed or cancelled\n", result.IssueNumber)
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&runID, "run-id", 0, "Workflow run ID (required)")
	cmd.MarkFlagRequired("run-id")
	return cmd
}

func newAdvanceCmd() *cobra.Command {
	var runID int64

	cmd := &cobra.Command{
		Use:   "advance",
		Short: "Check tier completion, dispatch next tier or open batch PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator advance is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			cwd, _ := os.Getwd()
			g := git.New(cwd)

			result, err := integrator.Advance(cmd.Context(), client, g, cfg, integrator.AdvanceParams{
				RunID:    runID,
				RepoRoot: cwd,
			})
			if err != nil {
				return err
			}

			if result.AllComplete {
				fmt.Printf("All tiers complete. Batch PR #%d opened.\n", result.BatchPRNumber)
			} else if result.TierComplete {
				fmt.Printf("Tier complete. Dispatched %d workers for next tier.\n", result.DispatchedCount)
			} else {
				fmt.Println("Tier not yet complete.")
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&runID, "run-id", 0, "Workflow run ID (required)")
	cmd.MarkFlagRequired("run-id")
	return cmd
}

func newIntegratorReviewCmd() *cobra.Command {
	var runID int64
	var prNumber int

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run agent review on the batch PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator review is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}
			if runID == 0 && prNumber == 0 {
				return fmt.Errorf("either --run-id or --pr is required")
			}
			if runID != 0 && prNumber != 0 {
				return fmt.Errorf("--run-id and --pr are mutually exclusive")
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			ag := claude.New(cfg.Agent.Binary, cfg.Agent.Model)
			cwd, _ := os.Getwd()
			g := git.New(cwd)

			result, err := integrator.Review(cmd.Context(), client, ag, g, cfg, integrator.ReviewParams{
				RunID:    runID,
				PRNumber: prNumber,
				RepoRoot: cwd,
			})
			if err != nil {
				return err
			}

			if result.Approved {
				fmt.Println("Batch PR approved by agent review.")
			} else if result.MaxCyclesHit {
				fmt.Println("Max fix cycles reached. Manual intervention needed.")
			} else if len(result.FixIssues) > 0 {
				fmt.Printf("Created %d fix issues and dispatched workers.\n", len(result.FixIssues))
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&runID, "run-id", 0, "Workflow run ID")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number (alternative to --run-id)")
	return cmd
}
