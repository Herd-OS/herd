package workflowevents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/jobs"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlerRecordsAndProcessesWorkflowEvent(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newEventStore()
	st.repos["octo/herd"] = store.Repository{ID: 7, Owner: "octo", Name: "herd", InstallationID: 42}
	processor := &capturingProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedValidator(validEventClaims(now)),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
		Processor: processor,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, eventRequest(validEventPayload()))

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"accepted","created":true,"kind":"integrator_event","action":"worker_completed"}`, rec.Body.String())
	require.Len(t, st.commands, 1)
	assert.Contains(t, st.commands[0].CommandKey, "integrator_event:worker_completed:workflow_run:workflow_run:123")
	assert.Equal(t, "integrator_event", st.commands[0].CommandName)
	assert.Contains(t, string(st.commands[0].Metadata), `"workflow_run"`)
	require.Len(t, processor.calls, 1)
	assert.Equal(t, int64(7), processor.calls[0].repo.ID)
	assert.Equal(t, "worker_completed", processor.calls[0].event.Action)
}

func TestHandlerDuplicateWorkflowEventDoesNotProcessAgain(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newEventStore()
	st.repos["octo/herd"] = store.Repository{ID: 7, Owner: "octo", Name: "herd"}
	processor := &capturingProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedValidator(validEventClaims(now)),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
		Processor: processor,
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, eventRequest(validEventPayload()))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, eventRequest(validEventPayload()))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	assert.Contains(t, second.Body.String(), `"created":false`)
	assert.Len(t, processor.calls, 1)
}

func TestHandlerDistinctWorkflowRunEventsDoNotCollide(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newEventStore()
	st.repos["octo/herd"] = store.Repository{ID: 7, Owner: "octo", Name: "herd"}
	processor := &capturingProcessor{}
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedValidator(validEventClaims(now)),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
		Processor: processor,
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, eventRequest(eventPayloadWithWorkflowRunID("123")))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, eventRequest(eventPayloadWithWorkflowRunID("456")))
	redelivery := httptest.NewRecorder()
	handler.ServeHTTP(redelivery, eventRequest(eventPayloadWithWorkflowRunID("456")))

	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusAccepted, second.Code)
	require.Equal(t, http.StatusAccepted, redelivery.Code)
	assert.Contains(t, first.Body.String(), `"created":true`)
	assert.Contains(t, second.Body.String(), `"created":true`)
	assert.Contains(t, redelivery.Body.String(), `"created":false`)
	assert.Len(t, st.commands, 2)
	assert.Len(t, processor.calls, 2)
}

func TestHandlerRejectsInvalidOIDCRepository(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	st := newEventStore()
	handler := NewHandler(HandlerOptions{
		Store:     st,
		Validator: fixedValidator(jobs.OIDCClaims{Issuer: jobs.GitHubActionsIssuer, Audience: []string{"herd-control-plane"}, Repository: "octo/other", ExpiresAt: now.Add(time.Hour)}),
		Audience:  "herd-control-plane",
		Now:       func() time.Time { return now },
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, eventRequest(validEventPayload()))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, st.commands)
}

func TestParseRejectsInvalidWorkflowEvents(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantError string
	}{
		{name: "empty", payload: "", wantError: "workflow event payload is empty"},
		{name: "unknown kind", payload: `{"version":1,"kind":"x","repository":"octo/herd","event_name":"workflow_run","action":"x"}`, wantError: "unsupported workflow event kind"},
		{name: "missing action", payload: `{"version":1,"kind":"monitor_event","repository":"octo/herd","event_name":"schedule"}`, wantError: "action is required"},
		{name: "unknown field", payload: `{"version":1,"kind":"monitor_event","repository":"octo/herd","event_name":"schedule","action":"patrol","extra":true}`, wantError: "invalid workflow event payload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.payload))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

type eventStore struct {
	mu       sync.Mutex
	repos    map[string]store.Repository
	commands []store.CommandRecord
}

func newEventStore() *eventStore {
	return &eventStore{repos: map[string]store.Repository{}}
}

func (s *eventStore) GetRepository(_ context.Context, owner string, name string) (store.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, ok := s.repos[owner+"/"+name]
	if !ok {
		return store.Repository{}, store.ErrNotFound
	}
	return repo, nil
}

func (s *eventStore) RecordCommand(_ context.Context, c store.CommandRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.commands {
		if existing.RepositoryID == c.RepositoryID && existing.CommentID == c.CommentID && existing.CommandKey == c.CommandKey {
			return false, nil
		}
	}
	s.commands = append(s.commands, c)
	return true, nil
}

type capturingProcessor struct {
	calls []processorCall
}

type processorCall struct {
	repo  store.Repository
	event Event
}

func (p *capturingProcessor) ProcessWorkflowEvent(_ context.Context, repo store.Repository, event Event) error {
	p.calls = append(p.calls, processorCall{repo: repo, event: event})
	return nil
}

type fixedValidator jobs.OIDCClaims

func (v fixedValidator) Validate(context.Context, string) (jobs.OIDCClaims, error) {
	return jobs.OIDCClaims(v), nil
}

func validEventClaims(now time.Time) jobs.OIDCClaims {
	return jobs.OIDCClaims{
		Issuer:     jobs.GitHubActionsIssuer,
		Audience:   []string{"herd-control-plane"},
		Repository: "octo/herd",
		Ref:        "refs/heads/main",
		Workflow:   ".github/workflows/herd-integrator.yml",
		RunID:      "123",
		ExpiresAt:  now.Add(time.Hour),
	}
}

func eventRequest(payload string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-events", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer oidc")
	return req
}

func validEventPayload() string {
	return eventPayloadWithWorkflowRunID("123")
}

func eventPayloadWithWorkflowRunID(id string) string {
	payload, _ := json.Marshal(map[string]any{
		"version":    1,
		"kind":       KindIntegratorEvent,
		"repository": "octo/herd",
		"event_name": "workflow_run",
		"action":     "worker_completed",
		"workflow_run": map[string]any{
			"id":          id,
			"conclusion":  "success",
			"head_branch": "herd/worker/868",
			"head_sha":    "abc",
		},
	})
	return string(payload)
}
