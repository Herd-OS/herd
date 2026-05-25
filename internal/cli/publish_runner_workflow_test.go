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
}
