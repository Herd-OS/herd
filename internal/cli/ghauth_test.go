package cli

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGHAuthenticatorSetupTokenFailureModes(t *testing.T) {
	tests := []struct {
		name       string
		run        ghRunner
		wantSubstr string
	}{
		{
			name: "gh missing",
			run: func(context.Context, string, ...string) ([]byte, error) {
				return nil, exec.ErrNotFound
			},
			wantSubstr: "gh CLI is not installed",
		},
		{
			name: "gh unauthenticated",
			run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "auth" && args[1] == "status" {
					return nil, errors.New("not logged in")
				}
				return []byte("token"), nil
			},
			wantSubstr: "gh auth login -h github.com",
		},
		{
			name: "wrong host detectable by auth status",
			run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "auth" && args[1] == "status" {
					return nil, errors.New("no active account for github.com")
				}
				return []byte("token"), nil
			},
			wantSubstr: "github.com",
		},
		{
			name: "empty token",
			run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "auth" && args[1] == "status" {
					return []byte("ok"), nil
				}
				return []byte("\n"), nil
			},
			wantSubstr: "empty setup token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (ghAuthenticator{run: tt.run}).SetupToken(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSubstr)
		})
	}
}

func TestGHAuthenticatorSetupTokenSuccess(t *testing.T) {
	var calls [][]string
	auth := ghAuthenticator{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if args[0] == "auth" && args[1] == "status" {
			return []byte("ok"), nil
		}
		return []byte("token\n"), nil
	}}

	token, err := auth.SetupToken(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "token", token)
	require.Len(t, calls, 2)
	assert.Equal(t, []string{"gh", "auth", "status", "-h", "github.com"}, calls[0])
	assert.Equal(t, []string{"gh", "auth", "token", "-h", "github.com"}, calls[1])
}
