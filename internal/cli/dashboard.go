package cli

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/herd-os/herd/internal/dashboard"
	"github.com/spf13/cobra"
)

func newDashboardCmd() *cobra.Command {
	var refresh int
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Live TUI dashboard for open batches",
		Long:  "Read-only terminal dashboard showing active workers, open batches, and recent failures. Polls every --refresh-seconds seconds (default 15, clamped 5–300).",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOrExit()
			if err != nil {
				return err
			}
			client, err := newClientOrExit(cfg.Platform.Owner, cfg.Platform.Repo)
			if err != nil {
				return err
			}
			m := dashboard.NewModel(client, cfg.Platform.Owner, cfg.Platform.Repo, dashboard.ClampRefresh(refresh))
			_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}
	cmd.Flags().IntVar(&refresh, "refresh-seconds", 15, "Refresh interval in seconds (clamped to 5–300)")
	return cmd
}
