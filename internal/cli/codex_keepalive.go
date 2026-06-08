package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newCodexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Codex provider tools",
	}
	cmd.AddCommand(&cobra.Command{
		Use:    "keepalive-loop",
		Short:  "Keep the Codex OAuth chain warm (runner-side daemon)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(c *cobra.Command, _ []string) error {
			runKeepaliveLoop(c.Context())
			return nil
		},
		SilenceUsage: true,
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Diagnose local Codex configuration",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runCodexDoctor(c.Context(), c.OutOrStdout())
		},
		SilenceUsage: true,
	})
	return cmd
}

func keepaliveCodexHome() string {
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func keepaliveInterval() time.Duration {
	interval := 6 * 24 * time.Hour // 6-day cadence; 2-day buffer before the ~8-day forced refresh
	if v := os.Getenv("HERD_CODEX_KEEPALIVE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	return interval
}

func runKeepaliveLoop(ctx context.Context) {
	interval := keepaliveInterval()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if shouldRefresh(filepath.Join(keepaliveCodexHome(), "auth.json"), interval) {
			cmd := exec.CommandContext(ctx, "codex", "exec",
				"--ephemeral", "--skip-git-repo-check",
				"Reply with the single character 'k' and stop.")
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			_ = cmd.Run()
		}
	}
}

// shouldRefresh decides whether the keepalive should trigger a refresh. It is
// factored out so it is unit-testable without invoking codex or sleeping.
func shouldRefresh(authFile string, interval time.Duration) bool {
	data, err := os.ReadFile(authFile)
	if err != nil {
		return false // no auth yet
	}
	var auth struct {
		AuthMode    *string    `json:"auth_mode"`
		LastRefresh *time.Time `json:"last_refresh"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return false
	}
	if auth.AuthMode == nil || *auth.AuthMode != "chatgpt" {
		return false
	}
	if auth.LastRefresh != nil && time.Since(*auth.LastRefresh) < interval-time.Hour {
		return false
	}
	return true
}
