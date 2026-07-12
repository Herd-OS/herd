package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/reconciler"
	"github.com/herd-os/herd/internal/controlplane/store"
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
		{name: "workflow events", method: http.MethodPost, path: "/api/v1/workflow-events", wantStatus: http.StatusNotImplemented},
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

func TestDefaultJobResultsRouteFailsClosedWithoutProcessors(t *testing.T) {
	handler, err := NewServer(Config{Env: "development"}, Dependencies{Store: store.NewMemoryStore()})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-123/results", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"job result processors are not configured"}`, rec.Body.String())
}

func TestWorkflowEventsRouteCanBeInjected(t *testing.T) {
	handler, err := NewServer(Config{Env: "development"}, Dependencies{
		WorkflowEventsRoute: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
		}),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-events", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"accepted"}`, rec.Body.String())
}

func TestDefaultWorkflowEventsRouteFailsClosedWithoutProcessor(t *testing.T) {
	handler, err := NewServer(Config{Env: "development"}, Dependencies{Store: store.NewMemoryStore()})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-events", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"workflow event processor is not configured"}`, rec.Body.String())
}

func TestStartReconcilerLoopStartsAndStopsWithContext(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	r := &reconciler.Reconciler{
		Store: st,
		Now:   func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) },
		Config: reconciler.Config{
			Interval:        time.Hour,
			JobTimeout:      time.Minute,
			CommandTimeout:  time.Minute,
			CallbackTimeout: time.Minute,
		},
	}

	stop, started := StartReconcilerLoop(ctx, Config{ReconcilerEnabled: true}, Dependencies{Reconciler: r})
	require.True(t, started)
	require.NoError(t, stop())

	assert.False(t, r.LastReport().StartedAt.IsZero())
}
