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
			name:     "argument preservation",
			appLogin: "herd-os",
			body:     "@herd-os fix-ci retry failed tests",
			wantOK:   true,
			want: ParsedCommand{
				Kind: CommandFixCI,
				Args: []string{"retry", "failed", "tests"},
				Raw:  "@herd-os fix-ci retry failed tests",
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
