package commands

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMentionCommand(t *testing.T) {
	tests := []struct {
		name       string
		appLogin   string
		body       string
		wantOK     bool
		want       ParsedCommand
		wantErrIs  error
		wantErrMsg string
	}{
		{
			name:     "review command",
			appLogin: "herd-os",
			body:     "@herd-os review",
			wantOK:   true,
			want: ParsedCommand{
				Kind: CommandReview,
				Raw:  "@herd-os review",
			},
		},
		{
			name:     "extra whitespace",
			appLogin: "herd-os",
			body:     "\n\t @herd-os   review  \n",
			wantOK:   true,
			want: ParsedCommand{
				Kind: CommandReview,
				Raw:  "@herd-os   review",
			},
		},
		{
			name:     "mixed case login and command",
			appLogin: "HeRd-Os",
			body:     "@HERD-os ReVieW",
			wantOK:   true,
			want: ParsedCommand{
				Kind: CommandReview,
				Raw:  "@HERD-os ReVieW",
			},
		},
		{
			name:     "non mention comment",
			appLogin: "herd-os",
			body:     "please @herd-os review",
			wantOK:   false,
		},
		{
			name:     "wrong app login",
			appLogin: "herd-os",
			body:     "@other-app review",
			wantOK:   false,
		},
		{
			name:     "legacy slash command ignored",
			appLogin: "herd-os",
			body:     "/herd review",
			wantOK:   false,
		},
		{
			name:      "unknown command",
			appLogin:  "herd-os",
			body:      "@herd-os dance",
			wantOK:    true,
			wantErrIs: ErrUnknownCommand,
		},
		{
			name:      "plan unsupported by hosted app",
			appLogin:  "herd-os",
			body:      "@herd-os plan",
			wantOK:    true,
			wantErrIs: ErrUnknownCommand,
		},
		{
			name:     "argument preservation",
			appLogin: "herd-os",
			body:     "@herd-os fix-ci retry failed tests",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandFixCI,
				Args:   []string{"retry", "failed", "tests"},
				Prompt: "retry failed tests",
				Raw:    "@herd-os fix-ci retry failed tests",
			},
		},
		{
			name:     "fix prompt",
			appLogin: "herd-os",
			body:     "@herd-os fix update auth handling",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandFix,
				Args:   []string{"update", "auth", "handling"},
				Prompt: "update auth handling",
				Raw:    "@herd-os fix update auth handling",
			},
		},
		{
			name:     "fix multiline prompt",
			appLogin: "herd-os",
			body:     "@herd-os fix\nplease update auth handling\ninclude regression tests\n\n",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandFix,
				Prompt: "please update auth handling\ninclude regression tests",
				Raw:    "@herd-os fix",
			},
		},
		{
			name:     "fix quoted prompt",
			appLogin: "herd-os",
			body:     "@herd-os fix \"update auth handling\"",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandFix,
				Prompt: "update auth handling",
				Raw:    "@herd-os fix \"update auth handling\"",
			},
		},
		{
			name:       "unterminated quoted prompt",
			appLogin:   "herd-os",
			body:       "@herd-os fix \"update auth handling",
			wantOK:     true,
			wantErrMsg: "unterminated quote in command",
		},
		{
			name:     "fix-ci multiline hint",
			appLogin: "herd-os",
			body:     "@herd-os fix-ci\nfailing tests mention missing env var\ncheck setup step",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandFixCI,
				Prompt: "failing tests mention missing env var\ncheck setup step",
				Raw:    "@herd-os fix-ci",
			},
		},
		{
			name:     "review focus prompt",
			appLogin: "herd-os",
			body:     "@herd-os review focus on auth and retries",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandReview,
				Args:   []string{"focus", "on", "auth", "and", "retries"},
				Prompt: "focus on auth and retries",
				Raw:    "@herd-os review focus on auth and retries",
			},
		},
		{
			name:     "resolve conflicts command with context",
			appLogin: "herd-os",
			body:     "@herd-os resolve-conflicts keep generated files intact",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandResolveConflicts,
				Args:   []string{"keep", "generated", "files", "intact"},
				Prompt: "keep generated files intact",
				Raw:    "@herd-os resolve-conflicts keep generated files intact",
			},
		},
		{
			name:     "resolve conflicts multiline context",
			appLogin: "herd-os",
			body:     "@herd-os resolve-conflicts\nkeep generated files intact\nprefer current auth flow",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandResolveConflicts,
				Prompt: "keep generated files intact\nprefer current auth flow",
				Raw:    "@herd-os resolve-conflicts",
			},
		},
		{
			name:     "dispatch command without issue number",
			appLogin: "herd-os",
			body:     "@herd-os dispatch",
			wantOK:   true,
			want: ParsedCommand{
				Kind: CommandDispatch,
				Raw:  "@herd-os dispatch",
			},
		},
		{
			name:     "dispatch command with issue number",
			appLogin: "herd-os",
			body:     "@herd-os dispatch 42",
			wantOK:   true,
			want: ParsedCommand{
				Kind:   CommandDispatch,
				Args:   []string{"42"},
				Prompt: "42",
				Raw:    "@herd-os dispatch 42",
			},
		},
		{
			name:       "missing app login",
			appLogin:   " ",
			body:       "@herd-os review",
			wantErrMsg: "app login is required",
		},
		{
			name:      "missing command",
			appLogin:  "herd-os",
			body:      "@herd-os",
			wantOK:    true,
			wantErrIs: ErrUnknownCommand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := ParseMentionCommand(tt.appLogin, tt.body)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErrIs))
				return
			}
			if tt.wantErrMsg != "" {
				require.EqualError(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
