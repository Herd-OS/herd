package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
	"github.com/spf13/cobra"
)

func newDispatchCmd() *cobra.Command {
	var batchNum int
	var all, ignoreLimit, dryRun bool
	var timeout int

	cmd := &cobra.Command{
		Use:   "dispatch [issue-number]",
		Short: "Dispatch workers to execute issues",
		Long:  "Trigger worker workflows for ready issues. Dispatch a single issue, all ready issues in a batch, or all ready issues across all batches.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOrExit()
			if err != nil {
				return err
			}

			client, err := newClientOrExit(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return err
			}

			if timeout > 0 {
				cfg.Workers.TimeoutMinutes = timeout
			}

			ctx := cmd.Context()

			if len(args) == 1 {
				issueNum, err := strconv.Atoi(args[0])
				if err != nil {
					return fmt.Errorf("invalid issue number: %s", args[0])
				}
				return runDispatchSingle(ctx, client, cfg, issueNum, dryRun)
			}

			if batchNum > 0 {
				return runDispatchBatch(ctx, client, cfg, batchNum, ignoreLimit, dryRun)
			}

			if all {
				return runDispatchAll(ctx, client, cfg, ignoreLimit, dryRun)
			}

			return cmd.Help()
		},
	}

	cmd.Flags().IntVar(&batchNum, "batch", 0, "Dispatch all ready+failed issues in a batch (milestone number)")
	cmd.Flags().BoolVar(&all, "all", false, "Dispatch all ready+failed issues across all batches")
	cmd.Flags().BoolVar(&ignoreLimit, "ignore-limit", false, "Ignore max_concurrent worker limit")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "Override worker timeout in minutes")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be dispatched")

	return cmd
}

func runDispatchSingle(ctx context.Context, client platform.Platform, cfg *config.Config, issueNum int, dryRun bool) error {
	issue, err := client.Issues().Get(ctx, issueNum)
	if err != nil {
		return fmt.Errorf("getting issue #%d: %w", issueNum, err)
	}

	if issue.Milestone == nil {
		return fmt.Errorf("issue #%d has no milestone (not part of a batch)", issueNum)
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", issue.Milestone.Number, planner.Slugify(issue.Milestone.Title))

	if dryRun {
		fmt.Printf("Would dispatch issue #%d to branch %s\n", issueNum, batchBranch)
		return nil
	}

	if err := ensureBatchBranch(ctx, client, batchBranch); err != nil {
		return fmt.Errorf("creating batch branch: %w", err)
	}

	return dispatchIssue(ctx, client, cfg, issueNum, batchBranch)
}

func runDispatchBatch(ctx context.Context, client platform.Platform, cfg *config.Config, batchNum int, ignoreLimit, dryRun bool) error {
	ms, err := client.Milestones().Get(ctx, batchNum)
	if err != nil {
		return fmt.Errorf("getting milestone #%d: %w", batchNum, err)
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

	// Ensure batch branch exists (idempotent — no error if it already exists)
	if !dryRun {
		if err := ensureBatchBranch(ctx, client, batchBranch); err != nil {
			return fmt.Errorf("creating batch branch: %w", err)
		}
	}

	allIssues, err := client.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &ms.Number,
	})
	if err != nil {
		return fmt.Errorf("listing issues: %w", err)
	}

	// Filter to ready and failed
	var dispatchable []*platform.Issue
	for _, issue := range allIssues {
		status := issues.StatusLabel(issue.Labels)
		if status == issues.StatusReady || status == issues.StatusFailed {
			dispatchable = append(dispatchable, issue)
		}
	}

	if len(dispatchable) == 0 {
		fmt.Println("No ready or failed issues to dispatch")
		return nil
	}

	// Check concurrency limit
	capacity := len(dispatchable)
	if !ignoreLimit {
		active, err := countActiveWorkers(ctx, client)
		if err != nil {
			return fmt.Errorf("counting active workers: %w", err)
		}
		remaining := cfg.Workers.MaxConcurrent - active
		if remaining <= 0 {
			fmt.Printf("Worker limit reached (%d/%d active). Use --ignore-limit to override.\n",
				active, cfg.Workers.MaxConcurrent)
			return nil
		}
		if remaining < capacity {
			capacity = remaining
		}
	}

	fmt.Printf("Dispatching %d issues in batch #%d:\n", min(capacity, len(dispatchable)), batchNum)
	dispatched := 0
	for _, issue := range dispatchable {
		if dispatched >= capacity {
			fmt.Printf("  #%d %s %s\n", issue.Number, issue.Title, display.Blocked("skipped (limit)"))
			continue
		}
		if dryRun {
			fmt.Printf("  #%d %s (would dispatch)\n", issue.Number, issue.Title)
			dispatched++
			continue
		}
		if err := dispatchIssue(ctx, client, cfg, issue.Number, batchBranch); err != nil {
			fmt.Printf("  #%d %s %s\n", issue.Number, issue.Title, display.Error(err.Error()))
		} else {
			fmt.Printf("  #%d %s %s\n", issue.Number, issue.Title, display.Success("triggered"))
			dispatched++
		}
	}

	return nil
}

func runDispatchAll(ctx context.Context, client platform.Platform, cfg *config.Config, ignoreLimit, dryRun bool) error {
	milestones, err := client.Milestones().List(ctx)
	if err != nil {
		return fmt.Errorf("listing milestones: %w", err)
	}

	for _, ms := range milestones {
		if ms.State != "open" {
			continue
		}
		fmt.Printf("\nBatch #%d: %s\n", ms.Number, ms.Title)
		if err := runDispatchBatch(ctx, client, cfg, ms.Number, ignoreLimit, dryRun); err != nil {
			fmt.Printf("  %s\n", display.Error(err.Error()))
		}
	}
	return nil
}

// dispatchIssue triggers a worker workflow for a single issue.
func dispatchIssue(ctx context.Context, client platform.Platform, cfg *config.Config, issueNumber int, batchBranch string) error {
	// Get issue to validate state
	issue, err := client.Issues().Get(ctx, issueNumber)
	if err != nil {
		return fmt.Errorf("getting issue #%d: %w", issueNumber, err)
	}

	status := issues.StatusLabel(issue.Labels)
	if status != issues.StatusReady && status != issues.StatusFailed {
		return fmt.Errorf("issue #%d is %q, expected ready or failed", issueNumber, status)
	}

	// Remove old status label and add in-progress
	if status != "" {
		if err := client.Issues().RemoveLabels(ctx, issueNumber, []string{status}); err != nil {
			return fmt.Errorf("removing label: %w", err)
		}
	}
	if err := client.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusInProgress}); err != nil {
		return fmt.Errorf("adding in-progress label: %w", err)
	}

	// Get default branch for workflow dispatch ref
	defaultBranch, err := client.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return fmt.Errorf("getting default branch: %w", err)
	}

	// Dispatch worker workflow
	_, err = client.Workflows().Dispatch(ctx, "herd-worker.yml", defaultBranch, map[string]string{
		"issue_number":    fmt.Sprintf("%d", issueNumber),
		"batch_branch":    batchBranch,
		"timeout_minutes": fmt.Sprintf("%d", cfg.Workers.TimeoutMinutes),
		"runner_label":    cfg.Workers.RunnerLabel,
	})
	if err != nil {
		// Re-label as failed if dispatch fails
		_ = client.Issues().RemoveLabels(ctx, issueNumber, []string{issues.StatusInProgress})
		_ = client.Issues().AddLabels(ctx, issueNumber, []string{issues.StatusFailed})
		return fmt.Errorf("dispatching workflow: %w", err)
	}

	return nil
}

// ensureBatchBranch creates the batch branch from the default branch if it doesn't already exist.
func ensureBatchBranch(ctx context.Context, client platform.Platform, batchBranch string) error {
	// Check if branch already exists
	_, err := client.Repository().GetBranchSHA(ctx, batchBranch)
	if err == nil {
		return nil // already exists
	}

	// Get default branch SHA
	defaultBranch, err := client.Repository().GetDefaultBranch(ctx)
	if err != nil {
		return fmt.Errorf("getting default branch: %w", err)
	}
	sha, err := client.Repository().GetBranchSHA(ctx, defaultBranch)
	if err != nil {
		return fmt.Errorf("getting %s SHA: %w", defaultBranch, err)
	}

	// Create the batch branch
	if err := client.Repository().CreateBranch(ctx, batchBranch, sha); err != nil {
		return fmt.Errorf("creating branch %s: %w", batchBranch, err)
	}
	return nil
}

func countActiveWorkers(ctx context.Context, client platform.Platform) (int, error) {
	runs, err := client.Workflows().ListRuns(ctx, platform.RunFilters{
		Status: "in_progress",
	})
	if err != nil {
		return 0, fmt.Errorf("listing workflow runs: %w", err)
	}
	return len(runs), nil
}
