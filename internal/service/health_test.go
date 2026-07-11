package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthz(t *testing.T) {
	handler, err := NewServer(Config{}, Dependencies{})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

func TestReadyz(t *testing.T) {
	validConfig := Config{
		GitHubAppID:         123,
		GitHubAppPrivateKey: "private-key",
		WebhookSecret:       "secret",
		PublicURL:           "https://service.example.com",
		DatabaseURL:         "postgres://user:pass@localhost:5432/herd?sslmode=disable",
		Env:                 "production",
	}

	tests := []struct {
		name       string
		cfg        Config
		store      HealthStore
		wantStatus int
		wantBody   string
	}{
		{
			name:       "config unhealthy",
			cfg:        Config{Env: "production"},
			store:      healthyStore{},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "configuration not ready",
		},
		{
			name:       "storage dependency missing",
			cfg:        validConfig,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "storage not ready",
		},
		{
			name: "storage unhealthy",
			cfg:  validConfig,
			store: unhealthyStore{
				err: errors.New("database unavailable"),
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "database unavailable",
		},
		{
			name:       "ready",
			cfg:        validConfig,
			store:      healthyStore{},
			wantStatus: http.StatusOK,
			wantBody:   `"status":"ready"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewServer(tt.cfg, Dependencies{Store: tt.store})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

type healthyStore struct{}

func (healthyStore) HealthCheck(context.Context) error {
	return nil
}

type unhealthyStore struct {
	err error
}

func (s unhealthyStore) HealthCheck(context.Context) error {
	return s.err
}
