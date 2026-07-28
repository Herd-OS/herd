package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewWorkerParamsPassesPromptAsExtraInstructions(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		wantPrompt string
	}{
		{name: "empty prompt"},
		{name: "trims focused prompt", prompt: "  focus on auth and retries  ", wantPrompt: "focus on auth and retries"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := reviewWorkerParams(849, "/repo", tt.prompt, false)

			assert.Equal(t, 849, params.PRNumber)
			assert.Equal(t, "/repo", params.RepoRoot)
			assert.Equal(t, tt.wantPrompt, params.ExtraInstructions)
			assert.False(t, params.Manual)
		})
	}
}

func TestReviewWorkerParamsSetsManualFlag(t *testing.T) {
	tests := []struct {
		name       string
		manual     bool
		wantManual bool
	}{
		{name: "manual false by default"},
		{name: "manual true", manual: true, wantManual: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := reviewWorkerParams(849, "/repo", "  focus\n\nhere  ", tt.manual)

			assert.Equal(t, "focus\n\nhere", params.ExtraInstructions)
			assert.Equal(t, tt.wantManual, params.Manual)
		})
	}
}

func TestReviewWorkerCommandAcceptsPromptAndManualFlags(t *testing.T) {
	cmd := newReviewWorkerCmd()

	promptFlag := cmd.Flags().Lookup("prompt")
	manualFlag := cmd.Flags().Lookup("manual")

	require.NotNil(t, promptFlag)
	assert.Equal(t, "", promptFlag.DefValue)
	require.NotNil(t, manualFlag)
	assert.Equal(t, "false", manualFlag.DefValue)
}
