package main

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenServiceStoreMissingDatabaseURL(t *testing.T) {
	tests := []struct {
		name    string
		cfg     service.Config
		wantErr string
	}{
		{
			name: "production requires database url",
			cfg: service.Config{
				Env: "production",
			},
			wantErr: "HERD_DATABASE_URL is required",
		},
		{
			name: "development allows health-only startup",
			cfg: service.Config{
				Env: "development",
			},
		},
		{
			name: "non-production trims whitespace database url",
			cfg: service.Config{
				Env:         "staging",
				DatabaseURL: " \t\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := openServiceStore(context.Background(), tt.cfg)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, st)
				return
			}

			require.NoError(t, err)
			assert.Nil(t, st)
		})
	}
}
