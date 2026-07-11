package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutes(t *testing.T) {
	handler, err := NewServer(Config{Env: "development"}, Dependencies{})
	require.NoError(t, err)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "healthz", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK},
		{name: "readyz", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusServiceUnavailable},
		{name: "github webhook requires delivery", method: http.MethodPost, path: "/webhooks/github", wantStatus: http.StatusInternalServerError},
		{name: "repository register", method: http.MethodPost, path: "/api/v1/github/repositories/register", wantStatus: http.StatusNotImplemented},
		{name: "runner registration token", method: http.MethodPost, path: "/api/v1/runners/registration-token", wantStatus: http.StatusNotImplemented},
		{name: "job results", method: http.MethodPost, path: "/api/v1/jobs/job-123/results", wantStatus: http.StatusNotImplemented},
		{name: "unknown route", method: http.MethodGet, path: "/api/v1/unknown", wantStatus: http.StatusNotFound},
		{name: "wrong method", method: http.MethodGet, path: "/webhooks/github", wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestStubRoutesReturnJSON(t *testing.T) {
	handler, err := NewServer(Config{Env: "development"}, Dependencies{})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/github/repositories/register", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"error":"not implemented"}`, rec.Body.String())
}

func TestJobResultsRouteCanBeInjected(t *testing.T) {
	handler, err := NewServer(Config{Env: "development"}, Dependencies{
		JobResultsRoute: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusAccepted, map[string]string{"job_id": r.PathValue("job_id")})
		}),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-123/results", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"job_id":"job-123"}`, rec.Body.String())
}
