package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureProductionControlPlaneAuth(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name: "outside runner allows local auth",
			env:  map[string]string{"HERD_RUNNER": "", "GITHUB_TOKEN": "ghp_local"},
		},
		{
			name:    "runner rejects github token",
			env:     map[string]string{"HERD_RUNNER": "true", "GITHUB_TOKEN": "ghp_legacy"},
			wantErr: "cannot use GITHUB_TOKEN, GH_TOKEN, or HERD_GITHUB_TOKEN",
		},
		{
			name:    "runner rejects gh token",
			env:     map[string]string{"HERD_RUNNER": "true", "GH_TOKEN": "ghp_legacy"},
			wantErr: "cannot use GITHUB_TOKEN, GH_TOKEN, or HERD_GITHUB_TOKEN",
		},
		{
			name:    "runner rejects herd github token",
			env:     map[string]string{"HERD_RUNNER": "true", "HERD_GITHUB_TOKEN": "ghp_legacy"},
			wantErr: "cannot use GITHUB_TOKEN, GH_TOKEN, or HERD_GITHUB_TOKEN",
		},
		{
			name: "runner without legacy token is allowed",
			env:  map[string]string{"HERD_RUNNER": "true"},
		},
		{
			name: "explicit local override allows",
			env:  map[string]string{"HERD_RUNNER": "true", localGitHubAuthOverrideEnv: "true", "GITHUB_TOKEN": "ghp_local"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{"HERD_RUNNER", "GITHUB_TOKEN", "GH_TOKEN", "HERD_GITHUB_TOKEN", localGitHubAuthOverrideEnv} {
				t.Setenv(key, "")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			err := ensureProductionControlPlaneAuth("herd worker exec")

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
