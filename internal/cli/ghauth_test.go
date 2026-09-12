package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGHRunner struct {
	lookPathErr error
	outputs     map[string][]byte
	errs        map[string]error
	calls       []string
}

func (r *fakeGHRunner) LookPath(file string) (string, error) {
	if r.lookPathErr != nil {
		return "", r.lookPathErr
	}
	return "/usr/bin/" + file, nil
}

func (r *fakeGHRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + fmt.Sprint(args)
	r.calls = append(r.calls, key)
	return r.outputs[key], r.errs[key]
}

func TestGHAuthorizerSetupToken(t *testing.T) {
	statusKey := "gh [auth status -h github.com]"
	tokenKey := "gh [auth token -h github.com]"
	tests := []struct {
		name       string
		runner     *fakeGHRunner
		wantToken  string
		wantErrSub string
	}{
		{
			name: "valid token",
			runner: &fakeGHRunner{
				outputs: map[string][]byte{
					statusKey: []byte("github.com\nLogged in"),
					tokenKey:  []byte("gho_secret\n"),
				},
				errs: map[string]error{},
			},
			wantToken: "gho_secret",
		},
		{
			name:       "gh missing",
			runner:     &fakeGHRunner{lookPathErr: errors.New("not found")},
			wantErrSub: "gh CLI is not installed",
		},
		{
			name: "unauthenticated",
			runner: &fakeGHRunner{
				outputs: map[string][]byte{statusKey: []byte("not logged in")},
				errs:    map[string]error{statusKey: errors.New("exit 1")},
			},
			wantErrSub: "gh CLI is not authenticated",
		},
		{
			name: "wrong host detectable",
			runner: &fakeGHRunner{
				outputs: map[string][]byte{statusKey: []byte("git.example.com\nLogged in")},
				errs:    map[string]error{},
			},
			wantErrSub: "did not report github.com",
		},
		{
			name: "empty token",
			runner: &fakeGHRunner{
				outputs: map[string][]byte{
					statusKey: []byte("github.com\nLogged in"),
					tokenKey:  []byte(" \n"),
				},
				errs: map[string]error{},
			},
			wantErrSub: "empty token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := newGHAuthorizer(tt.runner).SetupToken(context.Background())
			if tt.wantErrSub != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSub)
				assert.Contains(t, err.Error(), "gh auth login")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantToken, token)
		})
	}
}
