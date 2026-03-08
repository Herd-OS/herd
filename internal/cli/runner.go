package cli

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/platform/github"
	"github.com/spf13/cobra"
)

func newRunnerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runner",
		Short: "Manage runners",
		Long:  "List and inspect self-hosted runners.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newRunnerListCmd())

	return cmd
}

func newRunnerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List runners",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			client, err := github.New(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return fmt.Errorf("creating GitHub client: %w", err)
			}

			runners, err := client.Runners().List(cmd.Context())
			if err != nil {
				return fmt.Errorf("listing runners: %w", err)
			}

			if len(runners) == 0 {
				fmt.Println("No runners found")
				return nil
			}

			tbl := display.NewTable("RUNNER", "STATUS", "LABELS", "BUSY")
			for _, r := range runners {
				busy := "idle"
				if r.Busy {
					busy = "busy"
				}
				tbl.AddRow(r.Name, r.Status, strings.Join(r.Labels, ", "), busy)
			}
			fmt.Println(tbl.Render())
			return nil
		},
	}
}
