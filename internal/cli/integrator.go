package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/commands"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/planner"
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
	cmd.AddCommand(newHandleCommentCmd())
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

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			g := git.New(cwd)

			result, err := integrator.Consolidate(cmd.Context(), client, g, cfg, integrator.ConsolidateParams{
				RunID:    runID,
				RepoRoot: cwd,
			})
			if err != nil {
				if issNum, lookupErr := issueNumberFromRun(cmd.Context(), client, runID); lookupErr == nil {
					postIntegratorFailure(cmd.Context(), client.Issues(), issNum, "consolidation", err)
				}
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

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			g := git.New(cwd)

			var result *integrator.AdvanceResult

			if batchNum > 0 {
				result, err = integrator.AdvanceByBatch(cmd.Context(), client, g, cfg, batchNum)
			} else {
				var ok bool
				ok, err = runWasSuccessful(cmd.Context(), client, runID)
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
				if runID != 0 {
					if issNum, lookupErr := issueNumberFromRun(cmd.Context(), client, runID); lookupErr == nil {
						postIntegratorFailure(cmd.Context(), client.Issues(), issNum, "tier advancement", err)
					}
				} else if batchNum > 0 {
					if prNum, lookupErr := batchPRNumber(cmd.Context(), client, batchNum); lookupErr == nil {
						postIntegratorFailure(cmd.Context(), client.Issues(), prNum, "tier advancement", err)
					}
				}
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
	var batchNum int

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run agent review on the batch PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator review is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}
			set := 0
			if runID != 0 { set++ }
			if prNumber != 0 { set++ }
			if batchNum != 0 { set++ }
			if set == 0 {
				return fmt.Errorf("one of --run-id, --pr, or --batch is required")
			}
			if set > 1 {
				return fmt.Errorf("--run-id, --pr, and --batch are mutually exclusive")
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
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			g := git.New(cwd)

			result, err := integrator.Review(cmd.Context(), client, ag, g, cfg, integrator.ReviewParams{
				RunID:       runID,
				PRNumber:    prNumber,
				BatchNumber: batchNum,
				RepoRoot:    cwd,
			})
			if err != nil {
				if prNumber != 0 {
					postIntegratorFailure(cmd.Context(), client.Issues(), prNumber, "review", err)
				} else if runID != 0 {
					if issNum, lookupErr := issueNumberFromRun(cmd.Context(), client, runID); lookupErr == nil {
						postIntegratorFailure(cmd.Context(), client.Issues(), issNum, "review", err)
					}
				} else if batchNum != 0 {
					if prNum, lookupErr := batchPRNumber(cmd.Context(), client, batchNum); lookupErr == nil {
						postIntegratorFailure(cmd.Context(), client.Issues(), prNum, "review", err)
					}
				}
				return err
			}

			if result.Approved {
				fmt.Println("Batch PR approved by agent review.")
			} else if result.MaxCyclesHit {
				fmt.Println("Max fix cycles reached. Manual intervention needed.")
			} else if len(result.FixIssues) > 0 {
				fmt.Printf("Created %d fix issues and dispatched workers.\n", len(result.FixIssues))
			} else if result.AllCreatesFailed {
				fmt.Printf("Review found %d issues but all fix-issue creations failed.\n", result.FindingsCount)
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&runID, "run-id", 0, "Workflow run ID")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number")
	cmd.Flags().IntVar(&batchNum, "batch", 0, "Batch/milestone number")
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
	var merged bool

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Run cleanup for a closed batch PR",
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
			_ = cfg

			if err := integrator.CleanupClosed(cmd.Context(), client, integrator.CleanupParams{
				PRNumber: prNumber,
				Merged:   merged,
			}); err != nil {
				return err
			}
			fmt.Println("Cleanup complete.")
			return nil
		},
	}

	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR number (required)")
	cmd.Flags().BoolVar(&merged, "merged", false, "Whether the PR was merged (vs closed without merging)")
	cmd.MarkFlagRequired("pr")
	return cmd
}

func newHandleCommentCmd() *cobra.Command {
	var (
		commentID         int64
		issueNumber       int
		authorLogin       string
		authorAssociation string
		isPRStr           string
	)

	cmd := &cobra.Command{
		Use:   "handle-comment",
		Short: "Handle a /herd command from an issue/PR comment",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator handle-comment is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			if commentID <= 0 {
				return fmt.Errorf("--comment-id must be greater than 0, got %d", commentID)
			}
			if issueNumber <= 0 {
				return fmt.Errorf("--issue-number must be greater than 0, got %d", issueNumber)
			}

			commentBody := os.Getenv("COMMENT_BODY")
			issueBody := os.Getenv("ISSUE_BODY")

			if commentBody == "" {
				return fmt.Errorf("COMMENT_BODY env var is required")
			}

			isPR := isPRStr == "true"

			parsed := commands.Parse(commentBody)
			if parsed == nil {
				fmt.Println("No /herd command found in comment.")
				return nil
			}

			allowed := map[string]bool{
				"OWNER":        true,
				"MEMBER":       true,
				"COLLABORATOR": true,
			}
			if !allowed[authorAssociation] {
				if !strings.HasSuffix(authorLogin, "[bot]") {
					fmt.Printf("Ignoring command from %s (association: %s)\n", authorLogin, authorAssociation)
					return nil
				}
			}

			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			_ = client.Issues().CreateCommentReaction(cmd.Context(), commentID, "eyes")

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			ag := claude.New(cfg.Agent.Binary, cfg.Agent.Model)
			g := git.New(cwd)

			reg := commands.DefaultRegistry()
			hctx := &commands.HandlerContext{
				Ctx:         cmd.Context(),
				Platform:    client,
				Agent:       ag,
				Git:         g,
				Config:      cfg,
				RepoRoot:    cwd,
				IssueNumber: issueNumber,
				CommentID:   commentID,
				IssueBody:   issueBody,
				AuthorLogin: authorLogin,
				IsPR:        isPR,
			}

			result := reg.Handle(hctx, *parsed)

			if result.Error != nil {
				msg := fmt.Sprintf("❌ **HerdOS Command Failed**\n\n`/herd %s` failed: %s", parsed.Name, result.Error)
				postCommentWithLog(cmd.Context(), client.Issues(), issueNumber, msg)
				return result.Error
			}
			if result.Message != "" {
				postCommentWithLog(cmd.Context(), client.Issues(), issueNumber, result.Message)
			}

			fmt.Println(result.Message)
			return nil
		},
	}

	cmd.Flags().Int64Var(&commentID, "comment-id", 0, "Comment ID (required)")
	cmd.Flags().IntVar(&issueNumber, "issue-number", 0, "Issue/PR number (required)")
	cmd.Flags().StringVar(&authorLogin, "author-login", "", "Comment author login")
	cmd.Flags().StringVar(&authorAssociation, "author-association", "", "Comment author association")
	cmd.Flags().StringVar(&isPRStr, "is-pr", "false", "Whether the comment was posted on a pull request")
	cmd.MarkFlagRequired("comment-id")
	cmd.MarkFlagRequired("issue-number")
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
	var batchNum int

	cmd := &cobra.Command{
		Use:   "check-ci",
		Short: "Check CI status and dispatch fix workers if needed",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd integrator check-ci is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}
			if runID == 0 && batchNum == 0 {
				return fmt.Errorf("one of --run-id or --batch is required")
			}
			if runID != 0 && batchNum != 0 {
				return fmt.Errorf("--run-id and --batch are mutually exclusive")
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

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			result, err := integrator.CheckCI(cmd.Context(), client, cfg, integrator.CheckCIParams{
				RunID:       runID,
				BatchNumber: batchNum,
				RepoRoot:    cwd,
			})
			if err != nil {
				if runID != 0 {
					if issNum, lookupErr := issueNumberFromRun(cmd.Context(), client, runID); lookupErr == nil {
						postIntegratorFailure(cmd.Context(), client.Issues(), issNum, "CI check", err)
					}
				} else if batchNum > 0 {
					if prNum, lookupErr := batchPRNumber(cmd.Context(), client, batchNum); lookupErr == nil {
						postIntegratorFailure(cmd.Context(), client.Issues(), prNum, "CI check", err)
					}
				}
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

	cmd.Flags().Int64Var(&runID, "run-id", 0, "Workflow run ID")
	cmd.Flags().IntVar(&batchNum, "batch", 0, "Batch/milestone number")
	return cmd
}

// postCommentWithLog posts a comment to an issue and logs a warning to stderr
// if the post fails. This ensures comment-posting failures are visible in
// workflow logs rather than being silently discarded.
func postCommentWithLog(ctx context.Context, issues platform.IssueService, issueNumber int, body string) {
	if err := issues.AddComment(ctx, issueNumber, body); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to post comment on issue #%d: %v\n", issueNumber, err)
	}
}

func postIntegratorFailure(ctx context.Context, issueSvc platform.IssueService, number int, step string, err error) {
	body := fmt.Sprintf(
		"⚠️ **Integrator failed** during %s: %s\n\nYou can retry with `/herd integrate` on this issue or the batch PR.",
		step, err,
	)
	postCommentWithLog(ctx, issueSvc, number, body)
}

func issueNumberFromRun(ctx context.Context, client platform.Platform, runID int64) (int, error) {
	run, err := client.Workflows().GetRun(ctx, runID)
	if err != nil {
		return 0, err
	}
	numStr, ok := run.Inputs["issue_number"]
	if !ok {
		return 0, fmt.Errorf("run %d has no issue_number input", runID)
	}
	return strconv.Atoi(numStr)
}

func batchPRNumber(ctx context.Context, client platform.Platform, batchNum int) (int, error) {
	ms, err := client.Milestones().Get(ctx, batchNum)
	if err != nil {
		return 0, err
	}
	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))
	prs, err := client.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: batchBranch})
	if err != nil {
		return 0, fmt.Errorf("no open batch PR found: %w", err)
	}
	if len(prs) == 0 {
		return 0, fmt.Errorf("no open batch PR found")
	}
	return prs[0].Number, nil
}
