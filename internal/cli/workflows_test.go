package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkersExtraEnv_DefaultsEmpty(t *testing.T) {
	cfg := config.Default()
	require.NotNil(t, cfg.Workers.ExtraEnv, "ExtraEnv should be non-nil empty slice")
	assert.Empty(t, cfg.Workers.ExtraEnv, "ExtraEnv should default to empty")
}

func TestWorkersExtraEnv_RendersInWorkflow(t *testing.T) {
	tests := []struct {
		name  string
		extra []string
	}{
		{"single entry", []string{"NPM_TOKEN"}},
		{"two entries", []string{"BUNDLE_RUBYGEMS__PKG__GITHUB__COM", "NPM_TOKEN"}},
		{"three entries with underscores", []string{"FOO", "BAR_BAZ", "X__Y__Z"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Workers.ExtraEnv = tt.extra

			wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
			out, err := RenderWorkflow(wf, cfg)
			require.NoError(t, err)
			s := string(out)

			// Each secret renders with proper GitHub Actions secrets.<NAME> reference,
			// preserving the 10-space indent.
			for _, name := range tt.extra {
				line := "          " + name + ": ${{ secrets." + name + " }}"
				assert.Contains(t, s, line, "missing rendered env line for %s", name)
			}

			// Position: every extra env appears after GEMINI_API_KEY and before ISSUE_NUMBER.
			geminiIdx := strings.Index(s, "GEMINI_API_KEY:")
			issueIdx := strings.Index(s, "ISSUE_NUMBER:")
			require.True(t, geminiIdx >= 0 && issueIdx >= 0, "anchor lines must be present")

			prevIdx := geminiIdx
			for _, name := range tt.extra {
				idx := strings.Index(s, name+":")
				require.True(t, idx >= 0, "extra env %s should be in output", name)
				assert.Greater(t, idx, prevIdx, "extra env %s should come after previous entry", name)
				prevIdx = idx
			}
			assert.Less(t, prevIdx, issueIdx, "extra envs should appear before ISSUE_NUMBER")
		})
	}
}

func TestWorkersExtraEnv_EmptyOmitted(t *testing.T) {
	cfg := config.Default() // ExtraEnv empty
	wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	// Must match the on-disk .github/workflows/herd-worker.yml byte-for-byte.
	onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-worker.yml"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(rendered, onDisk),
		"rendered template with empty ExtraEnv must match committed workflow.\nrendered:\n%s\non-disk:\n%s", rendered, onDisk)

	// Sanity: no double blank lines around the env block.
	assert.NotContains(t, string(rendered),
		"GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }}\n\n          ISSUE_NUMBER:",
		"empty ExtraEnv produced a stray blank line in env block")
}

func TestWorkersExtraEnv_NilSlice(t *testing.T) {
	cfg := config.Default()
	cfg.Workers.ExtraEnv = nil
	wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-worker.yml"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(rendered, onDisk),
		"nil ExtraEnv must render the same as empty ExtraEnv")
}

func TestRenderWorkflow_StaticPassThrough(t *testing.T) {
	cfg := config.Default()
	for _, wf := range workflowFiles() {
		if wf.Template {
			continue
		}
		out, err := RenderWorkflow(wf, cfg)
		require.NoError(t, err, "rendering static workflow %s", wf.SrcName)

		raw, err := workflowFS.ReadFile("workflows/" + wf.SrcName)
		require.NoError(t, err)
		assert.Equal(t, raw, out, "static workflow %s should pass through unchanged", wf.SrcName)
	}
}

func TestRenderWorkflow_UnknownSource(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "does-not-exist.yml", DestName: "x.yml"}
	_, err := RenderWorkflow(wf, cfg)
	assert.Error(t, err, "rendering nonexistent workflow source should error")
}

func TestWorkflowFiles_ContainsExpectedNames(t *testing.T) {
	files := workflowFiles()
	require.Len(t, files, 3)

	bySrc := map[string]workflowFile{}
	for _, wf := range files {
		bySrc[wf.SrcName] = wf
	}

	worker, ok := bySrc["herd-worker.yml.tmpl"]
	require.True(t, ok, "worker template must be registered")
	assert.True(t, worker.Template, "worker workflow must be marked as template")
	assert.Equal(t, "herd-worker.yml", worker.DestName)

	monitor, ok := bySrc["herd-monitor.yml"]
	require.True(t, ok)
	assert.False(t, monitor.Template)

	integrator, ok := bySrc["herd-integrator.yml"]
	require.True(t, ok)
	assert.False(t, integrator.Template)
}
