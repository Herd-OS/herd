package cli

import (
	"fmt"
	"os"

	"github.com/herd-os/herd/internal/agent/factory"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newReviewWorkerCmd() *cobra.Command {
	var prNumber int
	var resultFile string
	cmd := &cobra.Command{
		Use:    "review-worker",
		Short:  "Run hosted review worker (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd review-worker is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}
			if prNumber <= 0 {
				return fmt.Errorf("--pr is required")
			}
			if resultFile == "" {
				return fmt.Errorf("--result-file is required")
			}
			if err := ensureProductionControlPlaneAuth("herd review-worker"); err != nil {
				return err
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed before review execution.",
				})
				return fmt.Errorf("creating GitHub client: %w", err)
			}
			ag, err := factory.New(cfg.Agent.Resolve(config.AgentRoleWorkers))
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed before review execution.",
				})
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed before review execution.",
				})
				return fmt.Errorf("getting current directory: %w", err)
			}

			result, err := integrator.Review(cmd.Context(), client, ag, git.New(cwd), cfg, integrator.ReviewParams{
				PRNumber: prNumber,
				RepoRoot: cwd,
			})
			if err != nil {
				_ = writeHostedReviewResult(resultFile, hostedReviewWorkflowResult{
					Status:  "failed",
					Summary: "Herd Review failed.",
				})
				return err
			}
			if err := writeHostedReviewResult(resultFile, hostedReviewResultFromIntegrator(result)); err != nil {
				return err
			}
			printReviewResultMessage(result)
			return nil
		},
	}
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number")
	cmd.Flags().StringVar(&resultFile, "result-file", "", "Write hosted review workflow result JSON")
	return cmd
}
