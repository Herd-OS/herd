package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *Command
	}{
		{"simple command", "/herd fix-ci", &Command{Name: "fix-ci"}},
		{"command with prompt", `/herd fix-ci "the Node version is wrong"`, &Command{Name: "fix-ci", Prompt: "the Node version is wrong"}},
		{"command with arg", "/herd retry 42", &Command{Name: "retry", Args: []string{"42"}}},
		{"command with prompt and no args", `/herd review "focus on error handling"`, &Command{Name: "review", Prompt: "focus on error handling"}},
		{"command with arg and prompt", `/herd fix "add missing error check"`, &Command{Name: "fix", Prompt: "add missing error check"}},
		{"command mid-comment", "Some context\n/herd fix-ci\nMore text", &Command{Name: "fix-ci"}},
		{"no command", "just a regular comment", nil},
		{"empty herd prefix", "/herd ", nil},
		{"herd without space", "/herdfix-ci", nil},
		{"indented command", "  /herd fix-ci", &Command{Name: "fix-ci"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.body)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want.Name, got.Name)
			if len(tt.want.Args) > 0 {
				assert.Equal(t, tt.want.Args, got.Args)
			}
			if tt.want.Prompt != "" {
				assert.Equal(t, tt.want.Prompt, got.Prompt)
			}
		})
	}
}
