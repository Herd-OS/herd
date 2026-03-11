package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/spf13/cobra"
)

// StatusOutput is the JSON-serializable status structure.
type StatusOutput struct {
	Batches []BatchStatus `json:"batches"`
	Workers []WorkerRun   `json:"workers"`
}

// BatchStatus holds summary info for a batch.
type BatchStatus struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Failed   int    `json:"failed"`
	Active   int    `json:"active"`
	Blocked  int    `json:"blocked"`
	Ready    int    `json:"ready"`
}

// WorkerRun holds info about an active workflow run.
type WorkerRun struct {
	RunID  int64  `json:"run_id"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

func newStatusCmd() *cobra.Command {
	var batchNum int
	var showRunners, jsonOutput, watch bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show system status",
		Long:  "Display active batches, workers, and runners.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOrExit()
			if err != nil {
				return err
			}
			client, err := newClientOrExit(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			if showRunners {
				return renderRunners(ctx, client)
			}

			if watch {
				return runWatch(ctx, client, batchNum, jsonOutput)
			}

			if batchNum > 0 {
				return renderBatchDetail(ctx, client, batchNum, jsonOutput)
			}

			return renderOverview(ctx, client, jsonOutput)
		},
	}

	cmd.Flags().IntVar(&batchNum, "batch", 0, "Show status for a specific batch")
	cmd.Flags().BoolVar(&showRunners, "runners", false, "Show runner status")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&watch, "watch", false, "Refresh every 10 seconds")

	return cmd
}

func renderOverview(ctx context.Context, client platform.Platform, jsonOutput bool) error {
	milestones, err := client.Milestones().List(ctx)
	if err != nil {
		return fmt.Errorf("listing milestones: %w", err)
	}

	runs, err := client.Workflows().ListRuns(ctx, platform.RunFilters{Status: "in_progress"})
	if err != nil {
		return fmt.Errorf("listing runs: %w", err)
	}

	var statusOut StatusOutput

	for _, ms := range milestones {
		if ms.State != "open" {
			continue
		}
		bs, err := getBatchStatus(ctx, client, ms)
		if err != nil {
			return err
		}
		statusOut.Batches = append(statusOut.Batches, bs)
	}

	for _, r := range runs {
		statusOut.Workers = append(statusOut.Workers, WorkerRun{
			RunID:  r.ID,
			Status: r.Status,
			URL:    r.URL,
		})
	}

	if jsonOutput {
		return printJSON(statusOut)
	}

	// Render batches
	if len(statusOut.Batches) > 0 {
		fmt.Println("Batches:")
		for _, bs := range statusOut.Batches {
			fmt.Printf("  #%-3d %-30s %s  %d active\n",
				bs.Number, bs.Title, display.Progress(bs.Done, bs.Total), bs.Active)
		}
	} else {
		fmt.Println("No active batches")
	}

	// Render workers
	if len(statusOut.Workers) > 0 {
		fmt.Printf("\nWorkers:\n")
		for _, w := range statusOut.Workers {
			fmt.Printf("  Run %d  %s\n", w.RunID, display.InProgress(w.Status))
		}
	}

	return nil
}

func renderBatchDetail(ctx context.Context, client platform.Platform, batchNum int, jsonOutput bool) error {
	ms, err := client.Milestones().Get(ctx, batchNum)
	if err != nil {
		return fmt.Errorf("getting milestone #%d: %w", batchNum, err)
	}

	allIssues, err := client.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return fmt.Errorf("listing issues: %w", err)
	}

	if jsonOutput {
		return printJSON(map[string]interface{}{
			"milestone": ms,
			"issues":    allIssues,
		})
	}

	done := 0
	for _, issue := range allIssues {
		if issues.StatusLabel(issue.Labels) == issues.StatusDone || issue.State == "closed" {
			done++
		}
	}

	fmt.Printf("Batch: %s (#%d)\n", ms.Title, ms.Number)
	fmt.Printf("Progress: %s\n\n", display.Progress(done, len(allIssues)))

	for _, issue := range allIssues {
		status := issues.StatusLabel(issue.Labels)
		var symbol string
		switch status {
		case issues.StatusDone:
			symbol = display.Success("")
		case issues.StatusInProgress:
			symbol = display.InProgress("")
		case issues.StatusFailed:
			symbol = display.Error("")
		case issues.StatusBlocked:
			symbol = display.Blocked("")
		default:
			symbol = "  "
		}
		fmt.Printf("  %s #%-4d %s\n", symbol, issue.Number, issue.Title)
	}

	return nil
}

func renderRunners(ctx context.Context, client platform.Platform) error {
	runners, err := client.Runners().List(ctx)
	if err != nil {
		return fmt.Errorf("listing runners: %w", err)
	}

	if len(runners) == 0 {
		fmt.Println("No runners found")
		return nil
	}

	tbl := display.NewTable("RUNNER", "STATUS", "LABELS", "BUSY")
	for _, r := range runners {
		labels := fmt.Sprintf("%v", r.Labels)
		busy := "idle"
		if r.Busy {
			busy = "busy"
		}
		statusStr := r.Status
		tbl.AddRow(r.Name, statusStr, labels, busy)
	}
	fmt.Println(tbl.Render())
	return nil
}

func runWatch(ctx context.Context, client platform.Platform, batchNum int, jsonOutput bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	for {
		// Clear screen
		fmt.Print("\033[H\033[2J")
		fmt.Printf("Status (refreshing every 10s, Ctrl+C to stop)\n\n")

		if batchNum > 0 {
			if err := renderBatchDetail(ctx, client, batchNum, jsonOutput); err != nil {
				fmt.Printf("%s\n", display.Error(err.Error()))
			}
		} else {
			if err := renderOverview(ctx, client, jsonOutput); err != nil {
				fmt.Printf("%s\n", display.Error(err.Error()))
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
}

func getBatchStatus(ctx context.Context, client platform.Platform, ms *platform.Milestone) (BatchStatus, error) {
	allIssues, err := client.Issues().List(ctx, platform.IssueFilters{
		State:     "all",
		Milestone: &ms.Number,
	})
	if err != nil {
		return BatchStatus{}, fmt.Errorf("listing issues for milestone #%d: %w", ms.Number, err)
	}

	bs := BatchStatus{
		Number: ms.Number,
		Title:  ms.Title,
		Total:  len(allIssues),
	}
	for _, issue := range allIssues {
		switch issues.StatusLabel(issue.Labels) {
		case issues.StatusDone:
			bs.Done++
		case issues.StatusFailed:
			bs.Failed++
		case issues.StatusInProgress:
			bs.Active++
		case issues.StatusBlocked:
			bs.Blocked++
		case issues.StatusReady:
			bs.Ready++
		}
	}
	return bs, nil
}

func printJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
