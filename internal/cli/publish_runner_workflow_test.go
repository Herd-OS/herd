package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishRunnerWorkflow_Rendered(t *testing.T) {
	cfg := config.Default()
	cfg.Platform.Owner = "acme"
	cfg.Platform.Repo = "widgets"

	out := renderPublishRunnerWorkflow(t, cfg)
	rendered := string(out)

	wants := []string{
		"name: Herd Publish Runner",
		"packages: write",
		"-f Dockerfile.herd_runner",
		"--platform linux/amd64,linux/arm64",
		"if: vars.HERD_ENABLED == 'true'",
	}
	for _, want := range wants {
		assert.Contains(t, rendered, want, "rendered workflow should contain %q", want)
	}

	// GitHub expressions must be rendered as literal ${{ ... }}, not Go template actions.
	assert.NotContains(t, rendered, "{{`", "template escaping should be fully resolved")
	assert.Contains(t, rendered, "${{ github.repository_owner }}")

	// The `release: types: [published]` trigger remains absent: GitHub
	// silently blocks events caused by the default GITHUB_TOKEN (which
	// creates the release) from cascading into other workflows, so it
	// never fired in practice. The push-on-Dockerfile.herd_runner trigger
	// below is the real auto-rebuild path; release-event triggering would
	// be dead weight even if it worked.
	assert.NotContains(t, rendered, "types: [published]", "broken release trigger should not be present")
	assert.NotContains(t, rendered, "release:", "broken release trigger should not be present")

	// The push-on-Dockerfile.herd_runner trigger MUST be present.
	// Without it, consumer repos that merge an `Update HerdOS to <tag>`
	// PR (which bumps Dockerfile.herd_runner's FROM line) get no
	// automatic wrapper-image rebuild, so workers continue running with
	// stale baked-in agent CLIs and project-specific tools until a
	// maintainer manually fires `gh workflow run
	// herd-publish-runner.yml`. The trigger was briefly removed in #713
	// because of a duplicate-build concern that only applies to
	// herd-os/herd itself (which has a release.yml that ALSO rebuilds
	// the wrapper); consumer repos have no release.yml and need this
	// trigger as their only auto-rebuild path. See the template comment
	// for the full rationale.
	assert.Contains(t, rendered, "workflow_dispatch:", "workflow_dispatch must remain the manual trigger")
	assert.Contains(t, rendered, "push:", "push trigger must be present for consumer auto-rebuild on Dockerfile.herd_runner changes")
	assert.Contains(t, rendered, "'Dockerfile.herd_runner'", "push paths must scope to Dockerfile.herd_runner so unrelated pushes don't trigger image rebuilds")
	assert.Contains(t, rendered, "branches: [ main ]", "push trigger must be scoped to main so feature-branch pushes don't fire it")
}

func TestPublishRunnerWorkflow_RunsOn(t *testing.T) {
	t.Run("default single label matches committed workflow", func(t *testing.T) {
		cfg := config.Default()
		rendered := renderPublishRunnerWorkflow(t, cfg)

		assert.Contains(t, string(rendered), "runs-on: ubuntu-latest")
		assert.NotContains(t, string(rendered), "runs-on: [")

		onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-publish-runner.yml"))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(rendered, onDisk),
			"rendered publish runner workflow with default config must match committed workflow.\nrendered:\n%s\non-disk:\n%s", rendered, onDisk)
	})

	tests := []struct {
		name   string
		runsOn []string
		want   string
	}{
		{
			name:   "multi label flow list",
			runsOn: []string{"self-hosted", "herd-publisher"},
			want:   `runs-on: ["self-hosted", "herd-publisher"]`,
		},
		{
			name:   "quoted labels",
			runsOn: []string{"self-hosted", "linux x64", "gpu:large"},
			want:   `runs-on: ["self-hosted", "linux x64", "gpu:large"]`,
		},
		{
			name:   "escaping guard",
			runsOn: []string{"self-hosted", `label"quote`, `path\\runner`},
			want:   `runs-on: ["self-hosted", ` + strconv.Quote(`label"quote`) + `, ` + strconv.Quote(`path\\runner`) + `]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.ImagePublish.RunsOn = tt.runsOn

			rendered := renderPublishRunnerWorkflow(t, cfg)
			assert.Contains(t, string(rendered), tt.want)
		})
	}
}

func renderPublishRunnerWorkflow(t *testing.T, cfg *config.Config) []byte {
	t.Helper()

	wf := workflowFile{
		SrcName:  "herd-publish-runner.yml.tmpl",
		DestName: "herd-publish-runner.yml",
		Template: true,
	}

	out, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)
	return out
}
