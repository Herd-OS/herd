package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/commands"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifySignature(t *testing.T) {
	payload := []byte(`{"action":"created"}`)
	valid := sign("secret", payload)

	tests := []struct {
		name      string
		secret    string
		header    string
		wantError error
	}{
		{name: "valid signature", secret: "secret", header: valid},
		{name: "invalid signature", secret: "wrong", header: valid, wantError: ErrInvalidSignature},
		{name: "missing signature", secret: "secret", header: "", wantError: ErrMissingSignature},
		{name: "malformed prefix", secret: "secret", header: "sha1=abc", wantError: ErrMalformedSignature},
		{name: "malformed hex", secret: "secret", header: "sha256=zz", wantError: ErrMalformedSignature},
		{name: "malformed length", secret: "secret", header: "sha256=abcd", wantError: ErrMalformedSignature},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySignature(tt.secret, payload, tt.header)
			if tt.wantError == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantError)
		})
	}
}

func TestHandler(t *testing.T) {
	validInstallationPayload := `{
		"action":"created",
		"installation":{
			"id":42,
			"account":{"login":"octo-org","id":100,"type":"Organization"},
			"target_type":"Organization",
			"repository_selection":"selected",
			"permissions":{"contents":"read"},
			"events":["installation_repositories"]
		},
		"repositories":[{"id":99,"name":"herd","owner":{"login":"octo-org"},"default_branch":"main","private":true}]
	}`

	tests := []struct {
		name          string
		payload       string
		eventName     string
		deliveryID    string
		signature     string
		store         *fakeStore
		wantStatus    int
		wantRecord    int
		wantUpserts   int
		wantInstalls  int
		wantBodyValue string
	}{
		{
			name:         "valid signature",
			payload:      `{"action":"created","installation":{"id":42,"account":{"login":"octo","id":1,"type":"User"}},"repositories":[]}`,
			eventName:    EventInstallation,
			deliveryID:   "delivery-1",
			store:        &fakeStore{},
			wantStatus:   http.StatusAccepted,
			wantRecord:   1,
			wantInstalls: 1,
		},
		{
			name:       "invalid signature",
			payload:    `{"action":"created"}`,
			eventName:  EventInstallation,
			deliveryID: "delivery-2",
			signature:  sign("other", []byte(`{"action":"created"}`)),
			store:      &fakeStore{},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing signature",
			payload:    `{"action":"created"}`,
			eventName:  EventInstallation,
			deliveryID: "delivery-3",
			signature:  " ",
			store:      &fakeStore{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed signature",
			payload:    `{"action":"created"}`,
			eventName:  EventInstallation,
			deliveryID: "delivery-4",
			signature:  "sha256=not-hex",
			store:      &fakeStore{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing delivery ID",
			payload:    `{"action":"created"}`,
			eventName:  EventInstallation,
			deliveryID: "",
			store:      &fakeStore{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "duplicate delivery",
			payload:    validInstallationPayload,
			eventName:  EventInstallation,
			deliveryID: "delivery-5",
			store: &fakeStore{
				recordCreated: boolPtr(false),
			},
			wantStatus: http.StatusAccepted,
			wantRecord: 1,
		},
		{
			name:        "unsupported event accepted",
			payload:     `{"zen":"Keep it logically awesome."}`,
			eventName:   "ping",
			deliveryID:  "delivery-6",
			store:       &fakeStore{},
			wantStatus:  http.StatusAccepted,
			wantRecord:  1,
			wantUpserts: 0,
		},
		{
			name:       "storage failure returns retryable error",
			payload:    `{"action":"created"}`,
			eventName:  EventInstallation,
			deliveryID: "delivery-7",
			store: &fakeStore{
				recordErr: errors.New("database unavailable"),
			},
			wantStatus: http.StatusInternalServerError,
			wantRecord: 1,
		},
		{
			name:         "installation event upsert",
			payload:      validInstallationPayload,
			eventName:    EventInstallation,
			deliveryID:   "delivery-8",
			store:        &fakeStore{},
			wantStatus:   http.StatusAccepted,
			wantRecord:   1,
			wantInstalls: 1,
			wantUpserts:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			if store == nil {
				store = &fakeStore{}
			}
			signature := tt.signature
			if signature == "" {
				signature = sign("secret", []byte(tt.payload))
			}

			req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewBufferString(tt.payload))
			req.Header.Set("X-GitHub-Event", tt.eventName)
			if tt.deliveryID != "" {
				req.Header.Set("X-GitHub-Delivery", tt.deliveryID)
			}
			if signature != " " {
				req.Header.Set("X-Hub-Signature-256", signature)
			}
			rec := httptest.NewRecorder()

			NewHandler("secret", store, log.New(io.Discard, "", 0)).ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.Len(t, store.deliveries, tt.wantRecord)
			assert.Len(t, store.installations, tt.wantInstalls)
			assert.Len(t, store.repositories, tt.wantUpserts)
		})
	}
}

func TestHandlerRequiresEventHeader(t *testing.T) {
	payload := []byte(`{"action":"created"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", "delivery")
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	rec := httptest.NewRecorder()

	NewHandler("secret", &fakeStore{}, log.New(io.Discard, "", 0)).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"error":"missing X-GitHub-Event header"}`, rec.Body.String())
}

func TestHandlerInstallationRepositoriesAddRemove(t *testing.T) {
	payload := []byte(`{
		"action":"removed",
		"installation":{"id":42,"account":{"login":"octo-org","id":100,"type":"Organization"}},
		"repository_selection":"selected",
		"repositories_added":[{"id":1,"name":"added","owner":{"login":"octo-org"},"default_branch":"main"}],
		"repositories_removed":[{"id":2,"name":"removed","owner":{"login":"octo-org"},"default_branch":"main"}]
	}`)
	store := &fakeStore{}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", "delivery-installation-repos")
	req.Header.Set("X-GitHub-Event", EventInstallationRepositories)
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	rec := httptest.NewRecorder()

	NewHandler("secret", store, log.New(io.Discard, "", 0)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, store.installations, 1)
	require.Len(t, store.repositories, 2)
	assert.Equal(t, "added", store.repositories[0].Name)
	assert.JSONEq(t, `{"full_name":"","installation_repositories_action":"removed","repository_selection":"selected","selection_state":"selected"}`, string(store.repositories[0].Metadata))
	assert.Equal(t, "removed", store.repositories[1].Name)
	assert.JSONEq(t, `{"full_name":"","installation_repositories_action":"removed","removed":true,"repository_selection":"selected","selection_state":"removed"}`, string(store.repositories[1].Metadata))
}

func TestHandlerIssueCommentCommandSink(t *testing.T) {
	payload := []byte(`{
		"action":"created",
		"installation":{"id":42},
		"repository":{"id":99,"name":"herd","owner":{"login":"octo-org"},"default_branch":"main"},
		"issue":{"number":7,"pull_request":{"url":"https://api.github.com/repos/octo-org/herd/pulls/7"}},
		"comment":{"id":123,"body":"@herd-os review","author_association":"OWNER","user":{"login":"mona","type":"User"}},
		"sender":{"login":"mona","type":"User"}
	}`)
	store := &fakeStore{
		repositoriesByName: map[string]store.Repository{
			"octo-org/herd": {ID: 10, Owner: "octo-org", Name: "herd", InstallationID: 42},
		},
	}
	commander := &fakeIssueCommentCommander{}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", "delivery-issue-comment-command")
	req.Header.Set("X-GitHub-Event", EventIssueComment)
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	rec := httptest.NewRecorder()

	NewHandler("secret", store, log.New(io.Discard, "", 0), WithIssueCommentCommandHandler(commander)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Empty(t, commander.events)
	require.Len(t, store.commands, 1)
	command := store.commands[0]
	assert.Equal(t, int64(10), command.RepositoryID)
	assert.Equal(t, int64(123), command.CommentID)
	assert.Equal(t, "review", command.CommandKey)
	assert.Equal(t, "review", command.CommandName)
	assert.Equal(t, commands.StatusAcknowledged, command.Status)
	assert.Equal(t, "mona", command.Actor)
}

func TestHandlerUpsertFailureReturnsServerError(t *testing.T) {
	payload := []byte(`{
		"action":"created",
		"installation":{"id":42,"account":{"login":"octo-org","id":100,"type":"Organization"}},
		"repositories":[{"id":99,"name":"herd","owner":{"login":"octo-org"}}]
	}`)
	store := &fakeStore{upsertRepoErr: errors.New("database unavailable")}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", "delivery-upsert-failure")
	req.Header.Set("X-GitHub-Event", EventInstallation)
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	rec := httptest.NewRecorder()

	NewHandler("secret", store, log.New(io.Discard, "", 0)).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Len(t, store.deliveries, 1)
	assert.Equal(t, "failed", store.deliveries[0].Status)
}

func TestHandlerRetriesFailedDeliveryOnRedelivery(t *testing.T) {
	payload := []byte(`{
		"action":"created",
		"installation":{"id":42,"account":{"login":"octo-org","id":100,"type":"Organization"}},
		"repositories":[{"id":99,"name":"herd","owner":{"login":"octo-org"}}]
	}`)
	store := &fakeStore{upsertRepoErr: errors.New("database unavailable")}
	handler := NewHandler("secret", store, log.New(io.Discard, "", 0))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", "delivery-redeliver-after-failure")
	req.Header.Set("X-GitHub-Event", EventInstallation)
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Len(t, store.repositories, 0)
	require.Len(t, store.deliveries, 1)
	assert.Equal(t, "failed", store.deliveries[0].Status)

	store.upsertRepoErr = nil
	req = httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", "delivery-redeliver-after-failure")
	req.Header.Set("X-GitHub-Event", EventInstallation)
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Len(t, store.repositories, 1)
	require.Len(t, store.deliveries, 2)
	assert.Equal(t, "processed", store.deliveries[0].Status)
}

func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func boolPtr(value bool) *bool {
	return &value
}

type fakeStore struct {
	recordCreated *bool
	recordErr     error
	upsertInstErr error
	upsertRepoErr error

	deliveries    []store.WebhookDelivery
	installations []store.Installation
	repositories  []store.Repository
	commands      []store.CommandRecord

	repositoriesByName map[string]store.Repository
}

func (s *fakeStore) RecordWebhookDelivery(_ context.Context, d store.WebhookDelivery) (bool, error) {
	s.deliveries = append(s.deliveries, d)
	if s.recordErr != nil {
		return false, s.recordErr
	}
	if s.recordCreated != nil {
		if !*s.recordCreated {
			s.deliveries[len(s.deliveries)-1].Status = "processed"
		}
		return *s.recordCreated, nil
	}
	for _, existing := range s.deliveries {
		if existing.DeliveryID == d.DeliveryID {
			return false, nil
		}
	}
	return true, nil
}

func (s *fakeStore) GetWebhookDelivery(_ context.Context, deliveryID string) (store.WebhookDelivery, error) {
	for _, delivery := range s.deliveries {
		if delivery.DeliveryID == deliveryID {
			return delivery, nil
		}
	}
	return store.WebhookDelivery{}, store.ErrNotFound
}

func (s *fakeStore) UpdateWebhookDeliveryStatus(_ context.Context, deliveryID string, status string, errorMessage string, processedAt *time.Time) error {
	for i := range s.deliveries {
		if s.deliveries[i].DeliveryID == deliveryID {
			s.deliveries[i].Status = status
			s.deliveries[i].Error = errorMessage
			s.deliveries[i].ProcessedAt = processedAt
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *fakeStore) UpsertInstallation(_ context.Context, i store.Installation) error {
	if s.upsertInstErr != nil {
		return s.upsertInstErr
	}
	s.installations = append(s.installations, i)
	return nil
}

func (s *fakeStore) UpsertRepository(_ context.Context, r store.Repository) (store.Repository, error) {
	if s.upsertRepoErr != nil {
		return store.Repository{}, s.upsertRepoErr
	}
	s.repositories = append(s.repositories, r)
	return r, nil
}

func (s *fakeStore) GetRepository(_ context.Context, owner string, name string) (store.Repository, error) {
	if s.repositoriesByName != nil {
		if repo, ok := s.repositoriesByName[owner+"/"+name]; ok {
			return repo, nil
		}
	}
	for _, repo := range s.repositories {
		if repo.Owner == owner && repo.Name == name {
			return repo, nil
		}
	}
	return store.Repository{}, store.ErrNotFound
}

func (s *fakeStore) RecordCommand(_ context.Context, c store.CommandRecord) (bool, error) {
	for _, existing := range s.commands {
		if existing.RepositoryID == c.RepositoryID && existing.CommentID == c.CommentID && existing.CommandKey == c.CommandKey {
			return false, nil
		}
	}
	s.commands = append(s.commands, c)
	return true, nil
}

type fakeIssueCommentCommander struct {
	err    error
	events []commands.IssueComment
}

func (c *fakeIssueCommentCommander) HandleIssueComment(_ context.Context, event commands.IssueComment) (commands.Result, error) {
	c.events = append(c.events, event)
	if c.err != nil {
		return commands.Result{}, c.err
	}
	return commands.Result{Status: commands.StatusAcknowledged}, nil
}

func TestPayloadHash(t *testing.T) {
	payload := []byte(`{"action":"created"}`)
	sum := sha256.Sum256(payload)

	assert.Equal(t, hex.EncodeToString(sum[:]), payloadHash(payload))
}

func TestMustJSON(t *testing.T) {
	raw := mustJSON(map[string]string{"status": "accepted"})

	assert.True(t, json.Valid(raw))
	assert.JSONEq(t, `{"status":"accepted"}`, string(raw))
}
