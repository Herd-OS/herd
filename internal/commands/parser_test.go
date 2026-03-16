package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *Command
	}{
		{
			name: "simple command",
			body: "/herd fix-ci",
			want: &Command{Name: "fix-ci"},
		},
		{
			name: "command with double-quoted prompt",
			body: `/herd fix-ci "the problem is Node version"`,
			want: &Command{Name: "fix-ci", Prompt: "the problem is Node version"},
		},
		{
			name: "command with single-quoted prompt",
			body: `/herd fix-ci 'the problem is Node version'`,
			want: &Command{Name: "fix-ci", Prompt: "the problem is Node version"},
		},
		{
			name: "command with args",
			body: "/herd retry 42",
			want: &Command{Name: "retry", Args: "42"},
		},
		{
			name: "command with args and quoted prompt",
			body: `/herd retry 42 "please retry this"`,
			want: &Command{Name: "retry", Args: "42", Prompt: "please retry this"},
		},
		{
			name: "fix with prompt",
			body: `/herd fix "footer links are broken on mobile"`,
			want: &Command{Name: "fix", Prompt: "footer links are broken on mobile"},
		},
		{
			name: "review with prompt",
			body: `/herd review "focus on security"`,
			want: &Command{Name: "review", Prompt: "focus on security"},
		},
		{
			name: "command on non-first line",
			body: "some comment text\n/herd fix-ci",
			want: &Command{Name: "fix-ci"},
		},
		{
			name: "command on non-first line with prompt",
			body: "Hello world\n/herd fix \"bad node\"",
			want: &Command{Name: "fix", Prompt: "bad node"},
		},
		{
			name: "no command",
			body: "just a regular comment",
			want: nil,
		},
		{
			name: "empty body",
			body: "",
			want: nil,
		},
		{
			name: "herd prefix but not a command line",
			body: "I like /herd but this shouldn't match",
			want: nil,
		},
		{
			name: "herd command with CRLF line endings",
			body: "some text\r\n/herd fix-ci",
			want: &Command{Name: "fix-ci"},
		},
		{
			name: "multiple args",
			body: "/herd retry 42 43",
			want: &Command{Name: "retry", Args: "42 43"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.body)
			assert.Equal(t, tt.want, got)
		})
	}
}
