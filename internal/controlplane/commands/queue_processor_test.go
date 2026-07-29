package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueuedCommentBodyReplaysPromptsWithoutDuplication(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		prompt     string
		wantPrompt string
		wantArgs   []string
	}{
		{
			name:       "quoted fix prompt with remaining lines",
			raw:        `@herd-os fix "update auth handling"`,
			prompt:     "update auth handling\ninclude regression tests",
			wantPrompt: "update auth handling\ninclude regression tests",
		},
		{
			name:       "quoted fix prompt with trailing same-line text and remaining lines",
			raw:        `@herd-os fix "update auth" include tests`,
			prompt:     "update auth include tests\nkeep multiline context",
			wantPrompt: "update auth include tests\nkeep multiline context",
		},
		{
			name:       "quoted fix-ci prompt with remaining lines",
			raw:        `@herd-os fix-ci "update auth handling"`,
			prompt:     "update auth handling\ninclude regression tests",
			wantPrompt: "update auth handling\ninclude regression tests",
		},
		{
			name:       "quoted fix-ci prompt with trailing same-line text and remaining lines",
			raw:        `@herd-os fix-ci "update auth" include tests`,
			prompt:     "update auth include tests\nkeep multiline context",
			wantPrompt: "update auth include tests\nkeep multiline context",
		},
		{
			name:       "unquoted multiline prompt",
			raw:        "@herd-os fix update auth handling",
			prompt:     "update auth handling\ninclude regression tests",
			wantPrompt: "update auth handling\ninclude regression tests",
			wantArgs:   []string{"update", "auth", "handling"},
		},
		{
			name:       "dispatch numeric argument",
			raw:        "@herd-os dispatch 42",
			prompt:     "42",
			wantPrompt: "42",
			wantArgs:   []string{"42"},
		},
		{
			name:       "retry numeric argument",
			raw:        "@herd-os retry 42",
			prompt:     "42",
			wantPrompt: "42",
			wantArgs:   []string{"42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := queuedCommentBody(tt.raw, tt.prompt)
			cmd, ok, err := ParseMentionCommand("herd-os", body)

			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.wantPrompt, cmd.Prompt)
			assert.Equal(t, tt.wantArgs, cmd.Args)
		})
	}
}
