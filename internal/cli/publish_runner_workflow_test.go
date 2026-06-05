package cli

import (
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishRunnerWorkflow_Rendered(t *testing.T) {
	cfg := config.Default()
	cfg.Platform.Owner = "acme"
	cfg.Platform.Repo = "widgets"

	wf := workflowFile{
		SrcName:  "herd-publish-runner.yml.tmpl",
		DestName: "herd-publish-runner.yml",
		Template: true,
	}

	out, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)
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

	// The `release: types: [published]` trigger was removed because GitHub
	// silently blocks events caused by the default GITHUB_TOKEN (which
	// creates the release) from cascading into other workflows, so the
	// trigger never fired in practice.
	assert.NotContains(t, rendered, "types: [published]", "broken release trigger should not be present")
	assert.NotContains(t, rendered, "release:", "broken release trigger should not be present")

	// The `push` trigger on Dockerfile.herd_runner was also removed: it
	// caused a duplicate wrapper-image build whenever a release tag
	// followed a PR that touched Dockerfile.herd_runner (one build from
	// the PR merge, one from the tag-driven publish-runner-image job in
	// release.yml). Manual-only is intentional — see the template comment.
	assert.NotContains(t, rendered, "push:", "workflow should be workflow_dispatch-only")
	assert.NotContains(t, rendered, "paths:", "push paths filter should not be present")
	assert.NotContains(t, rendered, "branches:", "branches filter should not be present")
	assert.Contains(t, rendered, "workflow_dispatch:", "workflow_dispatch must remain the manual trigger")
}
