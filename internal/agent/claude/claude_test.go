package claude

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	a := New("", "")
	assert.Equal(t, "claude", a.BinaryPath)
	assert.Equal(t, "", a.Model)

	a = New("/usr/local/bin/claude", "opus")
	assert.Equal(t, "/usr/local/bin/claude", a.BinaryPath)
	assert.Equal(t, "opus", a.Model)
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
			path, err := writeSystemPromptFile(tc.prompt)
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
