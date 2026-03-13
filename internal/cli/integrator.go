package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/platform"
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
	cmd.AddCommand(newIntegratorMergeCmd())
	cmd.AddCommand(newIntegratorCleanupCmd())
	cmd.AddCommand(newIntegratorCheckCICmd())
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
	var batchNum int

	cmd := &cobra.Command{
		Use:   "advance",
		Short: "Check tier completion, dispatch next tier or open batch PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator advance is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}
			if runID == 0 && batchNum == 0 {
				return fmt.Errorf("either --run-id or --batch is required")
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

			var result *integrator.AdvanceResult

			if batchNum > 0 {
				result, err = integrator.AdvanceByBatch(cmd.Context(), client, g, cfg, batchNum)
			} else {
				ok, err := runWasSuccessful(cmd.Context(), client, runID)
				if err != nil {
					return fmt.Errorf("checking run status: %w", err)
				}
				if !ok {
					fmt.Println("Skipped: triggering run was not successful.")
					return nil
				}

				result, err = integrator.Advance(cmd.Context(), client, g, cfg, integrator.AdvanceParams{
					RunID:    runID,
					RepoRoot: cwd,
				})
			}
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

	cmd.Flags().Int64Var(&runID, "run-id", 0, "Workflow run ID")
	cmd.Flags().IntVar(&batchNum, "batch", 0, "Batch (milestone) number")
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

			if runID != 0 {
				ok, err := runWasSuccessful(cmd.Context(), client, runID)
				if err != nil {
					return fmt.Errorf("checking run status: %w", err)
				}
				if !ok {
					fmt.Println("Skipped: triggering run was not successful.")
					return nil
				}
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

func newIntegratorMergeCmd() *cobra.Command {
	var prNumber int

	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Merge an approved batch PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator merge is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			result, err := integrator.MergeApproved(cmd.Context(), client, cfg, integrator.MergeApprovedParams{
				PRNumber: prNumber,
			})
			if err != nil {
				return err
			}

			if result.Skipped {
				fmt.Printf("Skipped: %s\n", result.Reason)
			} else if result.Merged {
				fmt.Println("Batch PR merged and cleanup complete.")
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number (required)")
	cmd.MarkFlagRequired("pr")
	return cmd
}

func newIntegratorCleanupCmd() *cobra.Command {
	var prNumber int

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Run post-merge cleanup for a merged batch PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator cleanup is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}
			_ = cfg // config loaded for consistency but CleanupMerged doesn't need it

			if err := integrator.CleanupMerged(cmd.Context(), client, integrator.CleanupParams{
				PRNumber: prNumber,
			}); err != nil {
				return err
			}
			fmt.Println("Post-merge cleanup complete.")
			return nil
		},
	}

	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number (required)")
	cmd.MarkFlagRequired("pr")
	return cmd
}

// runWasSuccessful checks if the triggering run succeeded. Returns false for
// failed/cancelled runs — the subsequent integrator steps (check-ci, advance, review)
// should be skipped since consolidate already handled labeling.
func runWasSuccessful(ctx context.Context, client platform.Platform, runID int64) (bool, error) {
	run, err := client.Workflows().GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	return run.Conclusion == "success", nil
}

func newIntegratorCheckCICmd() *cobra.Command {
	var runID int64

	cmd := &cobra.Command{
		Use:   "check-ci",
		Short: "Check CI status and dispatch fix workers if needed",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator check-ci is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			ok, err := runWasSuccessful(cmd.Context(), client, runID)
			if err != nil {
				return fmt.Errorf("checking run status: %w", err)
			}
			if !ok {
				fmt.Println("Skipped: triggering run was not successful.")
				return nil
			}

			cwd, _ := os.Getwd()
			result, err := integrator.CheckCI(cmd.Context(), client, cfg, integrator.CheckCIParams{
				RunID:    runID,
				RepoRoot: cwd,
			})
			if err != nil {
				return err
			}

			if result.Skipped {
				fmt.Println("CI check skipped (require_ci is false).")
			} else if result.MaxCyclesHit {
				fmt.Println("CI failed — max fix cycles reached. Manual intervention needed.")
			} else if len(result.FixIssues) > 0 {
				fmt.Printf("CI failed — created %d fix issues and dispatched workers.\n", len(result.FixIssues))
			} else {
				fmt.Printf("CI status: %s\n", result.Status)
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&runID, "run-id", 0, "Workflow run ID (required)")
	cmd.MarkFlagRequired("run-id")
	return cmd
}
