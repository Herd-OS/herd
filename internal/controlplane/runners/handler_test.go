package runners

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/appauth"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrationTokenHandlerSuccess(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	minter := &fakeMinter{response: RegistrationTokenResponse{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})

	rec := serveRegistrationRequest(t, handler, RegistrationTokenRequest{
		Owner:          "octo",
		Name:           "repo",
		RunnerName:     "runner-1",
		RunnerLabels:   []string{"self-hosted", "herd"},
		BootstrapToken: plain,
		RequestNonce:   "nonce-1",
	})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"token":"github-runner-token","expires_at":"2026-07-11T13:00:00Z"}`, rec.Body.String())
	assert.Equal(t, int64(1001), minter.installationID)
	assert.Equal(t, "octo", minter.owner)
	assert.Equal(t, "repo", minter.repo)
	require.NotNil(t, st.tokens[token.ID].UsedAt)
	assert.Equal(t, now, *st.tokens[token.ID].UsedAt)
	assert.NotContains(t, rec.Body.String(), "installation")
}

func TestRegistrationTokenHandlerRejectsInvalidRequests(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		mutateStore func(*handlerFakeStore, store.RunnerBootstrapToken)
		mutateReq   func(*RegistrationTokenRequest)
		wantStatus  int
	}{
		{
			name: "missing token",
			mutateReq: func(req *RegistrationTokenRequest) {
				req.BootstrapToken = ""
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown token",
			mutateReq: func(req *RegistrationTokenRequest) {
				req.BootstrapToken = "hrb_unknown"
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong repo",
			mutateStore: func(st *handlerFakeStore, token store.RunnerBootstrapToken) {
				token.RepositoryID = 99
				st.tokens[token.ID] = token
				st.tokensByHash[token.TokenHash] = token
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "revoked token",
			mutateStore: func(st *handlerFakeStore, token store.RunnerBootstrapToken) {
				revokedAt := now.Add(-time.Minute)
				token.RevokedAt = &revokedAt
				token.RevokedReason = "rotated"
				st.tokens[token.ID] = token
				st.tokensByHash[token.TokenHash] = token
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired token",
			mutateStore: func(st *handlerFakeStore, token store.RunnerBootstrapToken) {
				token.ExpiresAt = now.Add(-time.Minute)
				st.tokens[token.ID] = token
				st.tokensByHash[token.TokenHash] = token
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, plain, token := newHandlerTestStore(t, now)
			if tt.mutateStore != nil {
				tt.mutateStore(st, token)
			}
			req := RegistrationTokenRequest{
				Owner:          "octo",
				Name:           "repo",
				RunnerName:     "runner-1",
				BootstrapToken: plain,
				RequestNonce:   "nonce-1",
			}
			if tt.mutateReq != nil {
				tt.mutateReq(&req)
			}
			minter := &fakeMinter{response: RegistrationTokenResponse{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}}
			handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})

			rec := serveRegistrationRequest(t, handler, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, 0, minter.calls)
			assert.Nil(t, st.tokens[token.ID].UsedAt)
		})
	}
}

func TestRegistrationTokenHandlerRotatedTokenInvalidatesOldActiveToken(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, oldPlain, oldToken := newHandlerTestStore(t, now)
	newPlain := "hrb_new"
	newToken := store.RunnerBootstrapToken{
		ID:           2,
		RepositoryID: oldToken.RepositoryID,
		TokenHash:    HashBootstrapToken(newPlain),
		CreatedAt:    now,
		ExpiresAt:    now.Add(time.Hour),
	}
	revokedAt := now
	oldToken.RevokedAt = &revokedAt
	oldToken.RevokedReason = "rotated"
	st.tokens[oldToken.ID] = oldToken
	st.tokensByHash[oldToken.TokenHash] = oldToken
	st.tokens[newToken.ID] = newToken
	st.tokensByHash[newToken.TokenHash] = newToken
	minter := &fakeMinter{response: RegistrationTokenResponse{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})

	oldRec := serveRegistrationRequest(t, handler, RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: oldPlain, RequestNonce: "nonce-old"})
	newRec := serveRegistrationRequest(t, handler, RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: newPlain, RequestNonce: "nonce-new"})

	assert.Equal(t, http.StatusUnauthorized, oldRec.Code)
	assert.Equal(t, http.StatusOK, newRec.Code)
	assert.Equal(t, 1, minter.calls)
}

func TestRegistrationTokenHandlerDuplicateNonceReplaysResponse(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, _ := newHandlerTestStore(t, now)
	minter := &fakeMinter{response: RegistrationTokenResponse{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", RunnerLabels: []string{"herd"}, BootstrapToken: plain, RequestNonce: "nonce-1"}

	first := serveRegistrationRequest(t, handler, req)
	second := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	assert.JSONEq(t, first.Body.String(), second.Body.String())
	assert.Equal(t, 1, minter.calls)
}

func TestRegistrationTokenHandlerDuplicateNonceReplaysWithNormalizedMetadata(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	minter := &fakeMinter{response: RegistrationTokenResponse{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", RunnerLabels: []string{"self-hosted", "herd"}, BootstrapToken: plain, RequestNonce: "nonce-normalized"}
	resultJSON, err := json.Marshal(minter.response)
	require.NoError(t, err)
	key := registrationIDKey(st.repository.ID, token.ID, req.RequestNonce)
	st.idempotency[key] = store.IdempotencyKey{
		Key:       key,
		Scope:     idempotencyScope,
		Status:    idempotencyStatusDone,
		ResultRef: string(resultJSON),
		Metadata:  json.RawMessage(`{"request_nonce":"nonce-normalized","bootstrap_token_id":1,"runner_labels":["self-hosted","herd"],"runner_name":"runner-1","repository_id":10}`),
		CreatedAt: now,
	}

	rec := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"token":"github-runner-token","expires_at":"2026-07-11T13:00:00Z"}`, rec.Body.String())
	assert.Equal(t, 0, minter.calls)
	require.NotNil(t, st.tokens[token.ID].UsedAt)
}

func TestRegistrationTokenHandlerReplayRepairsMissingTokenUse(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	minter := &fakeMinter{response: RegistrationTokenResponse{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", RunnerLabels: []string{"herd"}, BootstrapToken: plain, RequestNonce: "nonce-repair"}

	key := registrationIDKey(st.repository.ID, token.ID, req.RequestNonce)
	metadata, err := runnerRequestMetadata(st.repository.ID, req, token)
	require.NoError(t, err)
	resultJSON, err := json.Marshal(minter.response)
	require.NoError(t, err)
	st.idempotency[key] = store.IdempotencyKey{
		Key:       key,
		Scope:     idempotencyScope,
		Status:    idempotencyStatusDone,
		ResultRef: string(resultJSON),
		Metadata:  metadata,
		CreatedAt: now,
	}

	rec := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"token":"github-runner-token","expires_at":"2026-07-11T13:00:00Z"}`, rec.Body.String())
	assert.Equal(t, 0, minter.calls)
	require.NotNil(t, st.tokens[token.ID].UsedAt)
	assert.Equal(t, now, *st.tokens[token.ID].UsedAt)
}

func TestRegistrationTokenHandlerDuplicateNonceRejectsExpiredStoredToken(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	minter := &fakeMinter{response: RegistrationTokenResponse{Token: "new-token", ExpiresAt: now.Add(time.Hour)}}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", RunnerLabels: []string{"herd"}, BootstrapToken: plain, RequestNonce: "nonce-expired"}

	key := registrationIDKey(st.repository.ID, token.ID, req.RequestNonce)
	metadata, err := runnerRequestMetadata(st.repository.ID, req, token)
	require.NoError(t, err)
	resultJSON, err := json.Marshal(RegistrationTokenResponse{Token: "expired-token", ExpiresAt: now.Add(-time.Minute)})
	require.NoError(t, err)
	st.idempotency[key] = store.IdempotencyKey{
		Key:       key,
		Scope:     idempotencyScope,
		Status:    idempotencyStatusDone,
		ResultRef: string(resultJSON),
		Metadata:  metadata,
		CreatedAt: now.Add(-time.Hour),
	}

	rec := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusGone, rec.Code)
	assert.Contains(t, rec.Body.String(), "expired")
	assert.NotContains(t, rec.Body.String(), "expired-token")
	assert.Equal(t, 0, minter.calls)
	require.NotNil(t, st.tokens[token.ID].UsedAt)
	assert.Equal(t, 1, st.markUsedSuccesses)
}

func TestRegistrationTokenHandlerMinterFailures(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		minter TokenMinter
	}{
		{name: "app token mint failure", minter: AppInstallationMinter{Source: fakeTokenSource{err: errors.New("mint failed")}}},
		{name: "github registration token API failure", minter: &fakeMinter{err: errors.New("github failed")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, plain, token := newHandlerTestStore(t, now)
			handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: tt.minter, Now: func() time.Time { return now }})

			rec := serveRegistrationRequest(t, handler, RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: plain, RequestNonce: tt.name})

			assert.Equal(t, http.StatusBadGateway, rec.Code)
			assert.Nil(t, st.tokens[token.ID].UsedAt)
			for _, record := range st.idempotency {
				assert.Equal(t, "failed", record.Status)
			}
		})
	}
}

func TestRegistrationTokenHandlerRetriesAfterMinterFailure(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	minter := &fakeMinter{
		responses: []RegistrationTokenResponse{{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}},
		errors:    []error{errors.New("github failed"), nil},
	}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: plain, RequestNonce: "nonce-retry"}

	first := serveRegistrationRequest(t, handler, req)
	second := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusBadGateway, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "outcome is unknown")
	assert.Equal(t, 1, minter.calls)
	assert.Nil(t, st.tokens[token.ID].UsedAt)
}

func TestRegistrationTokenHandlerRetriesAfterMinterFailureWhenFailIdempotencyFails(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	st.failErrs = []error{errors.New("database down")}
	minter := &fakeMinter{
		responses: []RegistrationTokenResponse{{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}},
		errors:    []error{errors.New("github failed"), nil},
	}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: plain, RequestNonce: "nonce-fail-idem"}

	first := serveRegistrationRequest(t, handler, req)
	second := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "already in progress")
	assert.Equal(t, 0, minter.calls)
	assert.Nil(t, st.tokens[token.ID].UsedAt)
}

func TestRegistrationTokenHandlerRetriesAfterMarkUsedFailure(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	st.markUsedErrs = []error{errors.New("database down"), nil}
	minter := &fakeMinter{
		responses: []RegistrationTokenResponse{
			{Token: "github-runner-token-1", ExpiresAt: now.Add(time.Hour)},
		},
	}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: plain, RequestNonce: "nonce-mark-used"}

	first := serveRegistrationRequest(t, handler, req)
	second := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Contains(t, second.Body.String(), "github-runner-token-1")
	assert.Equal(t, 1, minter.calls)
	assert.Equal(t, 1, st.markUsedSuccesses)
	require.NotNil(t, st.tokens[token.ID].UsedAt)
}

func TestRegistrationTokenHandlerCompleteFailureDoesNotMintAgain(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	st.completeErrs = []error{errors.New("database down"), nil}
	minter := &fakeMinter{
		responses: []RegistrationTokenResponse{
			{Token: "github-runner-token-1", ExpiresAt: now.Add(time.Hour)},
		},
	}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: plain, RequestNonce: "nonce-complete"}

	first := serveRegistrationRequest(t, handler, req)
	second := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Contains(t, second.Body.String(), "github-runner-token-1")
	assert.Equal(t, 1, minter.calls)
	require.NotNil(t, st.tokens[token.ID].UsedAt)
}

func TestRegistrationTokenHandlerCompleteAndFallbackFailureDoesNotMintAgain(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, token := newHandlerTestStore(t, now)
	st.completeErrs = []error{errors.New("database down"), nil}
	st.failErrs = []error{nil, errors.New("fallback down")}
	minter := &fakeMinter{
		responses: []RegistrationTokenResponse{
			{Token: "github-runner-token-1", ExpiresAt: now.Add(time.Hour)},
		},
	}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	req := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", BootstrapToken: plain, RequestNonce: "nonce-complete-fallback"}

	first := serveRegistrationRequest(t, handler, req)
	second := serveRegistrationRequest(t, handler, req)

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Contains(t, second.Body.String(), "github-runner-token-1")
	assert.Equal(t, 1, minter.calls)
	require.NotNil(t, st.tokens[token.ID].UsedAt)
}

func TestRegistrationTokenHandlerSameNonceHandlesRunnerMetadata(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	st, plain, _ := newHandlerTestStore(t, now)
	minter := &fakeMinter{response: RegistrationTokenResponse{Token: "github-runner-token", ExpiresAt: now.Add(time.Hour)}}
	handler := NewRegistrationTokenHandler(HandlerOptions{Store: st, Minter: minter, Now: func() time.Time { return now }})
	firstReq := RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", RunnerLabels: []string{"self-hosted", "herd"}, BootstrapToken: plain, RequestNonce: "nonce-1"}
	tests := []struct {
		name       string
		req        RegistrationTokenRequest
		wantStatus int
	}{
		{name: "runner name changed", req: RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-2", RunnerLabels: []string{"self-hosted", "herd"}, BootstrapToken: plain, RequestNonce: "nonce-1"}, wantStatus: http.StatusConflict},
		{name: "labels reordered", req: RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", RunnerLabels: []string{"herd", "self-hosted"}, BootstrapToken: plain, RequestNonce: "nonce-1"}, wantStatus: http.StatusOK},
		{name: "labels changed", req: RegistrationTokenRequest{Owner: "octo", Name: "repo", RunnerName: "runner-1", RunnerLabels: []string{"self-hosted", "other"}, BootstrapToken: plain, RequestNonce: "nonce-1"}, wantStatus: http.StatusConflict},
	}

	first := serveRegistrationRequest(t, handler, firstReq)
	require.Equal(t, http.StatusOK, first.Code)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveRegistrationRequest(t, handler, tt.req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusConflict {
				assert.Contains(t, rec.Body.String(), "different runner metadata")
			} else {
				assert.JSONEq(t, first.Body.String(), rec.Body.String())
			}
			assert.Equal(t, 1, minter.calls)
		})
	}
}

func serveRegistrationRequest(t *testing.T, handler http.Handler, body RegistrationTokenRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/registration-token", bytes.NewReader(data))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func newHandlerTestStore(t *testing.T, now time.Time) (*handlerFakeStore, string, store.RunnerBootstrapToken) {
	t.Helper()
	plain := "hrb_bootstrap"
	token := store.RunnerBootstrapToken{
		ID:           1,
		RepositoryID: 10,
		TokenHash:    HashBootstrapToken(plain),
		CreatedAt:    now,
		ExpiresAt:    now.Add(time.Hour),
	}
	st := &handlerFakeStore{
		repository: store.Repository{ID: 10, InstallationID: 1001, Owner: "octo", Name: "repo"},
		tokens:     map[int64]store.RunnerBootstrapToken{token.ID: token},
		tokensByHash: map[string]store.RunnerBootstrapToken{
			token.TokenHash: token,
		},
		idempotency: map[string]store.IdempotencyKey{},
	}
	return st, plain, token
}

type handlerFakeStore struct {
	repository        store.Repository
	tokens            map[int64]store.RunnerBootstrapToken
	tokensByHash      map[string]store.RunnerBootstrapToken
	idempotency       map[string]store.IdempotencyKey
	completeErrs      []error
	failErrs          []error
	markUsedErrs      []error
	markUsedSuccesses int
}

func (s *handlerFakeStore) GetRepository(_ context.Context, owner string, name string) (store.Repository, error) {
	if s.repository.Owner == owner && s.repository.Name == name {
		return s.repository, nil
	}
	return store.Repository{}, store.ErrNotFound
}

func (s *handlerFakeStore) GetRunnerBootstrapTokenByHash(_ context.Context, tokenHash string) (store.RunnerBootstrapToken, error) {
	token, ok := s.tokensByHash[tokenHash]
	if !ok {
		return store.RunnerBootstrapToken{}, store.ErrNotFound
	}
	return token, nil
}

func (s *handlerFakeStore) MarkRunnerBootstrapTokenUsed(_ context.Context, tokenID int64, usedAt time.Time) error {
	if len(s.markUsedErrs) > 0 {
		err := s.markUsedErrs[0]
		s.markUsedErrs = s.markUsedErrs[1:]
		if err != nil {
			return err
		}
	}
	token, ok := s.tokens[tokenID]
	if !ok {
		return store.ErrNotFound
	}
	token.UsedAt = &usedAt
	s.tokens[tokenID] = token
	s.tokensByHash[token.TokenHash] = token
	s.markUsedSuccesses++
	return nil
}

func (s *handlerFakeStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	if _, ok := s.idempotency[key.Key]; ok {
		return false, nil
	}
	s.idempotency[key.Key] = key
	return true, nil
}

func (s *handlerFakeStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	record, ok := s.idempotency[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *handlerFakeStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	if len(s.completeErrs) > 0 {
		err := s.completeErrs[0]
		s.completeErrs = s.completeErrs[1:]
		if err != nil {
			return err
		}
	}
	record, ok := s.idempotency[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = idempotencyStatusDone
	record.ResultRef = resultRef
	record.CompletedAt = &now
	s.idempotency[key] = record
	return nil
}

func (s *handlerFakeStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	if len(s.failErrs) > 0 {
		err := s.failErrs[0]
		s.failErrs = s.failErrs[1:]
		if err != nil {
			return err
		}
	}
	record, ok := s.idempotency[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "failed"
	record.ResultRef = errorMessage
	record.CompletedAt = &now
	s.idempotency[key] = record
	return nil
}

type fakeMinter struct {
	response       RegistrationTokenResponse
	responses      []RegistrationTokenResponse
	err            error
	errors         []error
	calls          int
	installationID int64
	owner          string
	repo           string
}

func (m *fakeMinter) CreateRegistrationToken(_ context.Context, installationID int64, owner string, repo string) (RegistrationTokenResponse, error) {
	m.calls++
	m.installationID = installationID
	m.owner = owner
	m.repo = repo
	if len(m.errors) > 0 {
		err := m.errors[0]
		m.errors = m.errors[1:]
		if err != nil {
			return RegistrationTokenResponse{}, err
		}
	}
	if len(m.responses) > 0 {
		response := m.responses[0]
		m.responses = m.responses[1:]
		return response, nil
	}
	return m.response, m.err
}

type fakeTokenSource struct {
	err error
}

func (s fakeTokenSource) InstallationToken(context.Context, int64) (appauth.InstallationToken, error) {
	return appauth.InstallationToken{}, s.err
}
