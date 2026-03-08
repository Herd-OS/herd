package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
	"github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newBatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Manage batches",
		Long:  "List, show, and cancel batches (milestones).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newBatchListCmd())
	cmd.AddCommand(newBatchShowCmd())
	cmd.AddCommand(newBatchCancelCmd())

	return cmd
}

func newBatchListCmd() *cobra.Command {
	var all, jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List batches",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			return runBatchList(cmd.Context(), client, all, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Include completed batches")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func runBatchList(ctx context.Context, client platform.Platform, showAll, jsonOutput bool) error {
	milestones, err := client.Milestones().List(ctx)
	if err != nil {
		return fmt.Errorf("listing milestones: %w", err)
	}

	var batchStatuses []BatchStatus
	for _, ms := range milestones {
		if !showAll && ms.State != "open" {
			continue
		}
		bs, err := getBatchStatus(ctx, client, ms)
		if err != nil {
			return err
		}
		batchStatuses = append(batchStatuses, bs)
	}

	if jsonOutput {
		return printJSON(batchStatuses)
	}

	if len(batchStatuses) == 0 {
		fmt.Println("No batches found")
		return nil
	}

	tbl := display.NewTable("#", "NAME", "PROGRESS", "STATUS")
	for _, bs := range batchStatuses {
		status := "in progress"
		if bs.Done == bs.Total && bs.Total > 0 {
			status = "landed"
		} else if bs.Failed > 0 {
			status = fmt.Sprintf("%d failed", bs.Failed)
		}
		tbl.AddRow(
			strconv.Itoa(bs.Number),
			bs.Title,
			display.Progress(bs.Done, bs.Total),
			status,
		)
	}
	fmt.Println(tbl.Render())
	return nil
}

func newBatchShowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "show <number>",
		Short: "Show batch details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			batchNum, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid batch number: %s", args[0])
			}
			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			return renderBatchDetail(cmd.Context(), client, batchNum, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newBatchCancelCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "cancel <number>",
		Short: "Cancel a batch",
		Long:  "Cancel all active workers, label remaining issues as failed, close the milestone, and delete the batch branch.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			batchNum, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid batch number: %s", args[0])
			}
			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			return runBatchCancel(cmd.Context(), client, batchNum, force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

func runBatchCancel(ctx context.Context, client platform.Platform, batchNum int, force bool) error {
	ms, err := client.Milestones().Get(ctx, batchNum)
	if err != nil {
		return fmt.Errorf("getting milestone #%d: %w", batchNum, err)
	}

	allIssues, err := client.Issues().List(ctx, platform.IssueFilters{
		State:     "open",
		Milestone: &ms.Number,
	})
	if err != nil {
		return fmt.Errorf("listing issues: %w", err)
	}

	// Count active runs
	runs, err := client.Workflows().ListRuns(ctx, platform.RunFilters{Status: "in_progress"})
	if err != nil {
		return fmt.Errorf("listing runs: %w", err)
	}

	batchBranch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))

	if !force {
		fmt.Printf("WARNING: This will:\n")
		fmt.Printf("  - Cancel %d active workflow runs\n", len(runs))
		fmt.Printf("  - Label %d remaining issues as %s\n", len(allIssues), issues.StatusFailed)
		fmt.Printf("  - Close milestone #%d\n", ms.Number)
		fmt.Printf("  - Delete branch %s\n", batchBranch)
		fmt.Print("Continue? [type \"yes\" to confirm] ")

		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "yes" {
			fmt.Println("Cancelled")
			return nil
		}
	}

	// Cancel active runs
	for _, r := range runs {
		if err := client.Workflows().CancelRun(ctx, r.ID); err != nil {
			fmt.Printf("  %s\n", display.Error(fmt.Sprintf("failed to cancel run %d: %v", r.ID, err)))
		} else {
			fmt.Println(display.Success(fmt.Sprintf("Cancelled run %d", r.ID)))
		}
	}

	// Label open issues as failed
	for _, issue := range allIssues {
		status := issues.StatusLabel(issue.Labels)
		if status != "" && status != issues.StatusFailed {
			_ = client.Issues().RemoveLabels(ctx, issue.Number, []string{status})
		}
		if status != issues.StatusFailed {
			_ = client.Issues().AddLabels(ctx, issue.Number, []string{issues.StatusFailed})
		}
	}
	fmt.Println(display.Success(fmt.Sprintf("Labeled %d issues as failed", len(allIssues))))

	// Close milestone
	closed := "closed"
	if _, err := client.Milestones().Update(ctx, ms.Number, platform.MilestoneUpdate{State: &closed}); err != nil {
		fmt.Printf("  %s\n", display.Error(fmt.Sprintf("failed to close milestone: %v", err)))
	} else {
		fmt.Println(display.Success(fmt.Sprintf("Closed milestone #%d", ms.Number)))
	}

	// Delete batch branch
	if err := client.Repository().DeleteBranch(ctx, batchBranch); err != nil {
		fmt.Printf("  %s\n", display.Error(fmt.Sprintf("failed to delete branch: %v", err)))
	} else {
		fmt.Println(display.Success(fmt.Sprintf("Deleted branch %s", batchBranch)))
	}

	return nil
}
