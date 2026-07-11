package service

import (
	"testing"

	"github.com/herd-os/herd/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string
	}{
		{
			name: "valid env",
			env: map[string]string{
				envGitHubAppID:         "123",
				envGitHubAppPrivateKey: "private-key",
				envWebhookSecret:       "secret",
				envPublicURL:           "https://service.example.com",
				envDatabaseURL:         "postgres://user:pass@localhost:5432/herd?sslmode=disable",
				envEnv:                 "staging",
				envGitHubAppLogin:      "custom-login",
				envOIDCAudience:        "custom-audience",
				envReconcilerEnabled:   "true",
				envReconcilerInterval:  "30s",
			},
			want: Config{
				GitHubAppID:         123,
				GitHubAppPrivateKey: "private-key",
				WebhookSecret:       "secret",
				PublicURL:           "https://service.example.com",
				DatabaseURL:         "postgres://user:pass@localhost:5432/herd?sslmode=disable",
				Env:                 "staging",
				AppLogin:            "custom-login",
				OIDCAudience:        "custom-audience",
				ReconcilerEnabled:   true,
				ReconcilerInterval:  "30s",
			},
		},
		{
			name: "defaults env app login and oidc audience",
			env:  map[string]string{},
			want: Config{
				Env:          defaultEnv,
				AppLogin:     defaultGitHubAppLogin,
				OIDCAudience: controlplane.DefaultOIDCAudience,
			},
		},
		{
			name: "invalid github app id",
			env: map[string]string{
				envGitHubAppID: "not-an-int",
			},
			wantErr: envGitHubAppID,
		},
		{
			name: "non-positive github app id",
			env: map[string]string{
				envGitHubAppID: "0",
			},
			wantErr: envGitHubAppID,
		},
		{
			name: "invalid reconciler enabled",
			env: map[string]string{
				envReconcilerEnabled: "sometimes",
			},
			wantErr: envReconcilerEnabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			got, err := LoadConfigFromEnv()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConfigValidate(t *testing.T) {
	validProduction := Config{
		GitHubAppID:         123,
		GitHubAppPrivateKey: "private-key",
		WebhookSecret:       "secret",
		PublicURL:           "https://service.example.com",
		DatabaseURL:         "postgres://user:pass@localhost:5432/herd?sslmode=disable",
		Env:                 "production",
		AppLogin:            defaultGitHubAppLogin,
		OIDCAudience:        controlplane.DefaultOIDCAudience,
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{
			name: "valid production config",
			cfg:  validProduction,
		},
		{
			name: "missing required production values",
			cfg: Config{
				Env: "production",
			},
			wantErr: []string{
				envGitHubAppID,
				envGitHubAppPrivateKey,
				envWebhookSecret,
				envPublicURL,
				envDatabaseURL,
			},
		},
		{
			name: "development allows missing operator values",
			cfg: Config{
				Env: "development",
			},
		},
		{
			name: "invalid public url",
			cfg: Config{
				Env:       "development",
				PublicURL: "://bad-url",
			},
			wantErr: []string{envPublicURL},
		},
		{
			name: "public url must be http or https",
			cfg: Config{
				Env:       "development",
				PublicURL: "ftp://service.example.com",
			},
			wantErr: []string{envPublicURL, "http or https"},
		},
		{
			name: "public url must include host",
			cfg: Config{
				Env:       "development",
				PublicURL: "https:///missing-host",
			},
			wantErr: []string{envPublicURL, "host"},
		},
		{
			name: "invalid reconciler interval",
			cfg: Config{
				Env:                "development",
				ReconcilerInterval: "soon",
			},
			wantErr: []string{envReconcilerInterval},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestControlPlaneDefaults(t *testing.T) {
	assert.Equal(t, "https://api.herd-os.com", controlplane.DefaultControlPlaneURL)
	assert.Equal(t, "herd-control-plane", controlplane.DefaultOIDCAudience)
}

func clearConfigEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		envGitHubAppID,
		envGitHubAppPrivateKey,
		envWebhookSecret,
		envPublicURL,
		envDatabaseURL,
		envEnv,
		envGitHubAppLogin,
		envOIDCAudience,
		envReconcilerEnabled,
		envReconcilerInterval,
	} {
		t.Setenv(key, "")
	}
}
