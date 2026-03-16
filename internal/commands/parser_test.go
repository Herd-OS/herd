package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		want        *Command
		wantParseErr bool
	}{
		{"simple command", "/herd fix-ci", &Command{Name: "fix-ci"}, false},
		{"command with prompt", `/herd fix-ci "the Node version is wrong"`, &Command{Name: "fix-ci", Prompt: "the Node version is wrong"}, false},
		{"command with arg", "/herd retry 42", &Command{Name: "retry", Args: []string{"42"}}, false},
		{"command with prompt and no args", `/herd review "focus on error handling"`, &Command{Name: "review", Prompt: "focus on error handling"}, false},
		{"command with arg and prompt", `/herd fix "add missing error check"`, &Command{Name: "fix", Prompt: "add missing error check"}, false},
		{"command mid-comment", "Some context\n/herd fix-ci\nMore text", &Command{Name: "fix-ci"}, false},
		{"no command", "just a regular comment", nil, false},
		{"empty herd prefix", "/herd ", nil, false},
		{"herd without space", "/herdfix-ci", nil, false},
		{"indented command", "  /herd fix-ci", &Command{Name: "fix-ci"}, false},
		{"unclosed quote", `/herd fix "unclosed`, &Command{}, true},
		{"unclosed quote no command name before", `/herd "unclosed`, &Command{}, true},
		{"unclosed quote with spaces", `/herd fix "some prompt without closing`, &Command{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.body)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			if tt.wantParseErr {
				assert.Error(t, got.ParseErr)
				return
			}
			assert.NoError(t, got.ParseErr)
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

func TestRegistry_Handle_UnterminatedQuote(t *testing.T) {
	reg := NewRegistry()
	reg.Register("fix", func(_ *HandlerContext, _ Command) Result {
		return Result{Message: "fixed"}
	})

	cmd := Command{ParseErr: assert.AnError}
	result := reg.Handle(nil, cmd)
	assert.Equal(t, "⚠️ Unterminated quote in command.", result.Message)
	assert.NoError(t, result.Error)
}
