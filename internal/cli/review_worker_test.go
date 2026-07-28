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
			params := reviewWorkerParams(849, "/repo", tt.prompt)

			assert.Equal(t, 849, params.PRNumber)
			assert.Equal(t, "/repo", params.RepoRoot)
			assert.Equal(t, tt.wantPrompt, params.ExtraInstructions)
		})
	}
}

func TestReviewWorkerCommandAcceptsPromptFlag(t *testing.T) {
	cmd := newReviewWorkerCmd()

	flag := cmd.Flags().Lookup("prompt")

	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)
}
