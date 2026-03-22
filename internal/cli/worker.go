package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform/github"
	"github.com/herd-os/herd/internal/worker"
	"github.com/spf13/cobra"
)

func newWorkerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "worker",
		Short:  "Worker commands (internal)",
		Hidden: true,
	}
	cmd.AddCommand(newWorkerExecCmd())
	return cmd
}

func newWorkerExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <issue-number>",
		Short: "Execute a task from an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("HERD_RUNNER") != "true" {
				return fmt.Errorf("herd worker exec is intended to run inside GitHub Actions (set HERD_RUNNER=true)")
			}

			issueNum, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
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

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}

			result, err := worker.Exec(cmd.Context(), client, ag, cfg, worker.ExecParams{
				IssueNumber: issueNum,
				RepoRoot:    cwd,
				HTTPClient:  client.HTTPClient(),
			})
			if err != nil {
				return err
			}

			if result.NoOp {
				fmt.Printf("No changes needed for #%d (acceptance criteria already met)\n", issueNum)
			} else {
				fmt.Printf("Done. Branch: %s\n", result.WorkerBranch)
			}
			return nil
		},
	}
}
