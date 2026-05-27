package prompt

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSuspiciousOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"empty string", "", true},
		{"whitespace only", "   \n  ", true},
		{"execution error", "Execution error", true},
		{"execution error mixed case", "execution error", true},
		{"execution error with whitespace", "  Execution error  \n", true},
		{"short single line", "Error", true},
		{"short no newline under threshold", "Something bad", true},
		{"valid short with newline", "line1\nline2", false},
		{"valid long output", "This is a real agent summary that describes work done on the task", false},
		{"exactly at threshold single line", strings.Repeat("x", MinValidOutputLen), false},
		{"below threshold single line", strings.Repeat("x", MinValidOutputLen-1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSuspiciousOutput(tt.output))
		})
	}
}

func TestWriteSystemPromptFile(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
	}{
		{name: "empty prompt", prompt: ""},
		{name: "small prompt", prompt: "hello world"},
		{name: "large prompt over 200KB", prompt: strings.Repeat("x", 250*1024)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, err := WriteSystemPromptFile(tc.prompt)
			require.NoError(t, err)
			require.NotEmpty(t, path)
			t.Cleanup(func() { _ = os.Remove(path) })

			assert.True(t, strings.HasSuffix(path, ".txt"), "expected .txt suffix, got %s", path)

			info, err := os.Stat(path)
			require.NoError(t, err, "temp file should exist")
			assert.False(t, info.IsDir())

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tc.prompt, string(data))

			require.NoError(t, os.Remove(path))
			_, err = os.Stat(path)
			assert.True(t, os.IsNotExist(err), "file should be removable")
		})
	}
}
