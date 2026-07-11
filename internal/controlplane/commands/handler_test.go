package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlerAuthorization(t *testing.T) {
	tests := []struct {
		association string
		wantAck     bool
	}{
		{association: "OWNER", wantAck: true},
		{association: "MEMBER", wantAck: true},
		{association: "COLLABORATOR", wantAck: true},
		{association: "CONTRIBUTOR", wantAck: false},
		{association: "NONE", wantAck: false},
		{association: "FIRST_TIMER", wantAck: false},
		{association: "FIRST_TIME_CONTRIBUTOR", wantAck: false},
	}

	for _, tt := range tests {
		t.Run(tt.association, func(t *testing.T) {
			st := newFakeStore()
			gh := &fakeGitHub{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh}

			result, err := h.HandleIssueComment(context.Background(), validComment(tt.association, "@herd-os review"))

			require.NoError(t, err)
			if tt.wantAck {
				assert.Equal(t, StatusAcknowledged, result.Status)
				assert.Len(t, gh.comments, 1)
				assert.Len(t, st.commandRecords, 1)
				assert.Len(t, st.idempotencyKeys, 1)
				return
			}
			assert.Equal(t, StatusIgnored, result.Status)
			assert.Empty(t, gh.comments)
			assert.Empty(t, st.commandRecords)
			assert.Empty(t, st.idempotencyKeys)
		})
	}
}

func TestHandlerIgnoresBotAuthoredComments(t *testing.T) {
	tests := []IssueComment{
		func() IssueComment {
			event := validComment("OWNER", "@herd-os review")
			event.CommentAuthorType = "Bot"
			return event
		}(),
		func() IssueComment {
			event := validComment("OWNER", "@herd-os review")
			event.SenderLogin = "herd-os[bot]"
			return event
		}(),
	}

	for _, event := range tests {
		t.Run(event.SenderLogin+"/"+event.CommentAuthorType, func(t *testing.T) {
			st := newFakeStore()
			gh := &fakeGitHub{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh}

			result, err := h.HandleIssueComment(context.Background(), event)

			require.NoError(t, err)
			assert.Equal(t, StatusIgnored, result.Status)
			assert.Empty(t, gh.comments)
			assert.Empty(t, st.commandRecords)
		})
	}
}

func TestHandlerLegacySlashCommandMigrationResponseNoDispatch(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

	result, err := h.HandleIssueComment(context.Background(), validComment("OWNER", "/herd review"))

	require.NoError(t, err)
	assert.Equal(t, StatusIgnored, result.Status)
	require.Len(t, gh.comments, 1)
	assert.Contains(t, gh.comments[0].body, "@herd-os <command>")
	assert.Len(t, st.commandRecords, 1)
	assert.Equal(t, "migration", st.commandRecords[0].CommandKey)
	assert.Empty(t, dispatcher.dispatched)
}

func TestHandlerLegacySlashCommandUnauthorizedNoResponseNoDispatch(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

	result, err := h.HandleIssueComment(context.Background(), validComment("CONTRIBUTOR", "/herd review"))

	require.NoError(t, err)
	assert.Equal(t, StatusIgnored, result.Status)
	assert.Empty(t, gh.comments)
	assert.Empty(t, st.commandRecords)
	assert.Empty(t, dispatcher.dispatched)
}

func TestHandlerIdempotencyDuplicateCommentAndCommand(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	first, err := h.HandleIssueComment(context.Background(), event)
	require.NoError(t, err)
	second, err := h.HandleIssueComment(context.Background(), event)
	require.NoError(t, err)

	assert.Equal(t, StatusAcknowledged, first.Status)
	assert.Equal(t, StatusAcknowledged, second.Status)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, st.commandRecords, 1)
	assert.Len(t, st.idempotencyKeys, 1)
	assert.Empty(t, dispatcher.dispatched)
}

func TestHandlerEditedCommentIdempotent(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	require.NoError(t, err)
	event.Action = "edited"
	_, err = h.HandleIssueComment(context.Background(), event)
	require.NoError(t, err)

	assert.Len(t, gh.comments, 1)
	assert.Len(t, st.commandRecords, 1)
}

func TestHandlerUnknownCommandReturnsErrorWithoutMutation(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh}

	result, err := h.HandleIssueComment(context.Background(), validComment("OWNER", "@herd-os unknown"))

	require.ErrorIs(t, err, ErrUnknownCommand)
	assert.Empty(t, result.Status)
	assert.Empty(t, gh.comments)
	assert.Empty(t, st.commandRecords)
}

func TestHandlerNonMentionIgnored(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh}

	result, err := h.HandleIssueComment(context.Background(), validComment("OWNER", "please review"))

	require.NoError(t, err)
	assert.Equal(t, StatusIgnored, result.Status)
	assert.Empty(t, gh.comments)
	assert.Empty(t, st.commandRecords)
}

func TestHandlerStoreAndGitHubFailures(t *testing.T) {
	tests := []struct {
		name    string
		store   *fakeStore
		github  *fakeGitHub
		wantErr string
	}{
		{
			name:    "repository lookup",
			store:   &fakeStore{getRepoErr: store.ErrNotFound},
			github:  &fakeGitHub{},
			wantErr: "get repository",
		},
		{
			name:    "idempotency",
			store:   func() *fakeStore { s := newFakeStore(); s.acquireErr = errors.New("down"); return s }(),
			github:  &fakeGitHub{},
			wantErr: "acquire command idempotency key",
		},
		{
			name:    "ack",
			store:   newFakeStore(),
			github:  &fakeGitHub{err: errors.New("down")},
			wantErr: "add acknowledgement comment",
		},
		{
			name:    "record",
			store:   func() *fakeStore { s := newFakeStore(); s.recordErr = errors.New("down"); return s }(),
			github:  &fakeGitHub{},
			wantErr: "record command",
		},
		{
			name:    "complete",
			store:   func() *fakeStore { s := newFakeStore(); s.completeErr = errors.New("down"); return s }(),
			github:  &fakeGitHub{},
			wantErr: "complete command idempotency key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Handler{AppLogin: "herd-os", Store: tt.store, GitHub: tt.github}

			_, err := h.HandleIssueComment(context.Background(), validComment("OWNER", "@herd-os review"))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func validComment(association, body string) IssueComment {
	return IssueComment{
		Action:            "created",
		Owner:             "octo-org",
		Repo:              "herd",
		IssueNumber:       7,
		PullRequestURL:    "https://api.github.com/repos/octo-org/herd/pulls/7",
		CommentID:         123,
		CommentBody:       body,
		CommentAuthorType: "User",
		SenderLogin:       "mona",
		AuthorAssociation: association,
	}
}

type fakeStore struct {
	repo store.Repository

	getRepoErr  error
	acquireErr  error
	recordErr   error
	completeErr error

	idempotencyKeys map[string]store.IdempotencyKey
	commandRecords  []store.CommandRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		repo: store.Repository{
			ID:    42,
			Owner: "octo-org",
			Name:  "herd",
		},
		idempotencyKeys: map[string]store.IdempotencyKey{},
	}
}

func (s *fakeStore) GetRepository(_ context.Context, owner string, name string) (store.Repository, error) {
	if s.getRepoErr != nil {
		return store.Repository{}, s.getRepoErr
	}
	if owner != s.repo.Owner || name != s.repo.Name {
		return store.Repository{}, store.ErrNotFound
	}
	return s.repo, nil
}

func (s *fakeStore) AcquireIdempotencyKey(_ context.Context, key store.IdempotencyKey) (bool, error) {
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	if _, ok := s.idempotencyKeys[key.Key]; ok {
		return false, nil
	}
	s.idempotencyKeys[key.Key] = key
	return true, nil
}

func (s *fakeStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "completed"
	record.ResultRef = resultRef
	record.CompletedAt = &now
	s.idempotencyKeys[key] = record
	return nil
}

func (s *fakeStore) RecordCommand(_ context.Context, c store.CommandRecord) (bool, error) {
	if s.recordErr != nil {
		return false, s.recordErr
	}
	for _, existing := range s.commandRecords {
		if existing.RepositoryID == c.RepositoryID && existing.CommentID == c.CommentID && existing.CommandKey == c.CommandKey {
			return false, nil
		}
	}
	s.commandRecords = append(s.commandRecords, c)
	return true, nil
}

type fakeGitHub struct {
	err      error
	comments []fakeComment
}

type fakeComment struct {
	owner       string
	repo        string
	issueNumber int
	body        string
}

func (g *fakeGitHub) AddIssueComment(_ context.Context, owner, repo string, issueNumber int, body string) (int64, error) {
	if g.err != nil {
		return 0, g.err
	}
	g.comments = append(g.comments, fakeComment{owner: owner, repo: repo, issueNumber: issueNumber, body: body})
	return int64(1000 + len(g.comments)), nil
}

type fakeDispatcher struct {
	dispatched []DispatchCommand
}

func (d *fakeDispatcher) DispatchCommand(_ context.Context, cmd DispatchCommand) error {
	d.dispatched = append(d.dispatched, cmd)
	return nil
}
