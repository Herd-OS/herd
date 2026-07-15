package commands

import (
	"context"
	"encoding/json"
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
			dispatcher := &fakeDispatcher{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

			result, err := h.HandleIssueComment(context.Background(), validComment(tt.association, "@herd-os review"))

			require.NoError(t, err)
			if tt.wantAck {
				assert.Equal(t, StatusAcknowledged, result.Status)
				assert.Len(t, gh.comments, 1)
				assert.Len(t, st.commandRecords, 1)
				assert.Len(t, st.idempotencyKeys, 1)
				assert.Len(t, dispatcher.dispatched, 1)
				return
			}
			assert.Equal(t, StatusIgnored, result.Status)
			assert.Empty(t, gh.comments)
			assert.Empty(t, st.commandRecords)
			assert.Empty(t, st.idempotencyKeys)
		})
	}
}

func TestEnqueueIssueCommentCommand(t *testing.T) {
	tests := []struct {
		name       string
		event      IssueComment
		wantCount  int
		wantKey    string
		wantStatus string
		wantErr    string
	}{
		{name: "valid command", event: validComment("OWNER", "@herd-os review"), wantCount: 1, wantKey: "review", wantStatus: StatusAcknowledged},
		{name: "legacy migration command", event: validComment("OWNER", "/herd review"), wantCount: 1, wantKey: "migration", wantStatus: StatusAcknowledged},
		{name: "unauthorized ignored", event: validComment("CONTRIBUTOR", "@herd-os review")},
		{name: "non command ignored", event: validComment("OWNER", "hello")},
		{name: "bot ignored", event: func() IssueComment {
			e := validComment("OWNER", "@herd-os review")
			e.CommentAuthorType = "Bot"
			return e
		}()},
		{name: "unknown mention command ignored durably", event: validComment("OWNER", "@herd-os nope"), wantCount: 1, wantKey: "unknown", wantStatus: StatusIgnored},
		{name: "edited command accepted", event: func() IssueComment { e := validComment("OWNER", "@herd-os fix"); e.Action = "edited"; return e }(), wantCount: 1, wantKey: "fix", wantStatus: StatusAcknowledged},
		{name: "deleted command ignored", event: func() IssueComment { e := validComment("OWNER", "@herd-os fix"); e.Action = "deleted"; return e }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()

			err := EnqueueIssueCommentCommand(context.Background(), st, "herd-os", tt.event)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, st.commandRecords, tt.wantCount)
			if tt.wantCount > 0 {
				assert.Equal(t, tt.wantKey, st.commandRecords[0].CommandKey)
				assert.Equal(t, tt.wantStatus, st.commandRecords[0].Status)
			}
		})
	}
}

func TestEnqueueIssueCommentCommandIgnoresPRCommandsFromIssueComments(t *testing.T) {
	tests := []struct {
		body string
		kind CommandKind
	}{
		{body: "@herd-os review", kind: CommandReview},
		{body: "@herd-os fix", kind: CommandFix},
		{body: "@herd-os fix-ci", kind: CommandFixCI},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			st := newFakeStore()
			event := validComment("OWNER", tt.body)
			event.PullRequestURL = ""

			err := EnqueueIssueCommentCommand(context.Background(), st, "herd-os", event)

			require.NoError(t, err)
			assert.Empty(t, st.commandRecords)
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
	assert.Len(t, dispatcher.dispatched, 1)
	assert.Equal(t, int64(42), dispatcher.dispatched[0].RepositoryID)
	assert.Equal(t, int64(77), dispatcher.dispatched[0].InstallationID)
	assert.Equal(t, 7, dispatcher.dispatched[0].PRNumber)
}

func TestHandlerEditedCommentIdempotent(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	require.NoError(t, err)
	event.Action = "edited"
	_, err = h.HandleIssueComment(context.Background(), event)
	require.NoError(t, err)

	assert.Len(t, gh.comments, 1)
	assert.Len(t, st.commandRecords, 1)
	assert.Len(t, dispatcher.dispatched, 1)
}

func TestHandlerDispatchesServiceCommandsAfterAcknowledgement(t *testing.T) {
	tests := []struct {
		body string
		kind CommandKind
	}{
		{body: "@herd-os review", kind: CommandReview},
		{body: "@herd-os fix", kind: CommandFix},
		{body: "@herd-os fix-ci", kind: CommandFixCI},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			st := newFakeStore()
			gh := &fakeGitHub{}
			dispatcher := &fakeDispatcher{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

			result, err := h.HandleIssueComment(context.Background(), validComment("OWNER", tt.body))

			require.NoError(t, err)
			assert.Equal(t, StatusAcknowledged, result.Status)
			require.Len(t, gh.comments, 1)
			require.Len(t, dispatcher.dispatched, 1)
			assert.Equal(t, tt.kind, dispatcher.dispatched[0].Command.Kind)
		})
	}
}

func TestHandlerDoesNotDispatchPRCommandsFromIssueComments(t *testing.T) {
	tests := []struct {
		body string
		kind CommandKind
	}{
		{body: "@herd-os review", kind: CommandReview},
		{body: "@herd-os fix", kind: CommandFix},
		{body: "@herd-os fix-ci", kind: CommandFixCI},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			st := newFakeStore()
			gh := &fakeGitHub{}
			dispatcher := &fakeDispatcher{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
			event := validComment("OWNER", tt.body)
			event.PullRequestURL = ""

			result, err := h.HandleIssueComment(context.Background(), event)
			duplicate, duplicateErr := h.HandleIssueComment(context.Background(), event)

			require.NoError(t, err)
			require.NoError(t, duplicateErr)
			assert.Equal(t, StatusAcknowledged, result.Status)
			assert.Equal(t, StatusAcknowledged, duplicate.Status)
			assert.Len(t, gh.comments, 1)
			assert.Len(t, st.commandRecords, 1)
			assert.Empty(t, dispatcher.dispatched)
			key := "repo:42:comment:123:command:" + string(tt.kind)
			require.Contains(t, st.idempotencyKeys, key)
			assert.Equal(t, "completed", st.idempotencyKeys[key].Status)
		})
	}
}

func TestHandlerNonDispatchableAcknowledgementRecordFailureRedeliveryDoesNotAckAgain(t *testing.T) {
	st := newFakeStore()
	st.updateErrs = []error{errors.New("store down"), nil}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os plan")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "record acknowledgement comment")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Empty(t, dispatcher.dispatched)
	key := "repo:42:comment:123:command:plan"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "issue_comment:1001", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, StatusAcknowledged, st.commandRecords[0].Status)
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"author_association":"OWNER","raw":"@herd-os plan"}`, string(st.commandRecords[0].Metadata))
}

func TestHandlerDispatchFailureOccursAfterAcknowledgement(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{errs: []error{errors.New("dispatch down"), nil}}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)
	_, duplicateErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	require.NoError(t, retryErr)
	require.NoError(t, duplicateErr)
	assert.Contains(t, err.Error(), "dispatch command")
	assert.Len(t, gh.comments, 1)
	assert.Len(t, st.commandRecords, 1)
	assert.Len(t, st.idempotencyKeys, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	for _, record := range st.idempotencyKeys {
		assert.Equal(t, "completed", record.Status)
	}
}

func TestHandlerAcknowledgementFailureRedeliveryDoesNotDispatchUntilAckRecorded(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{errs: []error{errors.New("github down"), nil}}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	assert.Empty(t, dispatcher.dispatched)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "add acknowledgement comment")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	require.Len(t, st.commandRecords, 1)
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"author_association":"OWNER","issue_number":7,"pr_number":7,"raw":"@herd-os review"}`, string(st.commandRecords[0].Metadata))
}

func TestHandlerAcknowledgementRecordFailureRedeliveryDoesNotAckAgain(t *testing.T) {
	st := newFakeStore()
	st.updateErrs = []error{errors.New("store down"), nil, nil, nil}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "record acknowledgement comment")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "dispatch:completed", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, "dispatched", st.commandRecords[0].Status)
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"author_association":"OWNER","issue_number":7,"pr_number":7,"raw":"@herd-os review"}`, string(st.commandRecords[0].Metadata))
}

func TestHandlerAcknowledgementRecordAndFallbackCompletionFailureDoesNotAckAgain(t *testing.T) {
	st := newFakeStore()
	st.updateErrs = []error{errors.New("store down")}
	st.completeErrs = []error{errors.New("idempotency down")}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "record acknowledgement comment")
	require.Error(t, retryErr)
	assert.Contains(t, retryErr.Error(), "repair required")
	assert.Len(t, gh.comments, 1)
	assert.Empty(t, dispatcher.dispatched)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "started", st.idempotencyKeys[key].Status)
}

func TestHandlerAcknowledgementCompletionFailureRedeliveryDoesNotAckAgain(t *testing.T) {
	st := newFakeStore()
	st.completeErrs = []error{errors.New("store down"), nil, nil}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os plan")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "record acknowledgement intent")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Empty(t, dispatcher.dispatched)
	key := "repo:42:comment:123:command:plan"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "issue_comment:1001", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"author_association":"OWNER","raw":"@herd-os plan"}`, string(st.commandRecords[0].Metadata))
}

func TestHandlerDispatchCompletionFailureRedeliveryDoesNotDispatchAgain(t *testing.T) {
	st := newFakeStore()
	st.completeErrs = []error{nil, errors.New("store down"), nil}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete command idempotency key")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, "dispatched", st.commandRecords[0].Status)
}

func TestHandlerRecordFailureRetryPostsOneAcknowledgement(t *testing.T) {
	st := newFakeStore()
	st.recordErr = errors.New("store down")
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	st.recordErr = nil
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "record command")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, st.commandRecords, 1)
	assert.Len(t, dispatcher.dispatched, 1)
}

func TestHandlerDispatchStatusFailureRedeliveryRepairsWithoutDispatchingAgain(t *testing.T) {
	st := newFakeStore()
	st.updateErrs = []error{nil, nil, errors.New("store down")}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "mark command dispatched")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, "dispatched", st.commandRecords[0].Status)
}

func TestHandlerDispatchStatusAndFallbackFailureRedeliveryDoesNotDispatchAgain(t *testing.T) {
	st := newFakeStore()
	st.updateErrs = []error{nil, nil, errors.New("store down")}
	st.completeErrs = []error{nil, errors.New("idempotency down")}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "mark command dispatched")
	require.Error(t, retryErr)
	assert.Contains(t, retryErr.Error(), "unknown outcome")
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "issue_comment:1001", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, "dispatching", st.commandRecords[0].Status)
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
		body    string
		store   *fakeStore
		github  *fakeGitHub
		wantErr string
	}{
		{
			name:    "repository lookup",
			body:    "@herd-os review",
			store:   &fakeStore{getRepoErr: store.ErrNotFound},
			github:  &fakeGitHub{},
			wantErr: "get repository",
		},
		{
			name:    "idempotency",
			body:    "@herd-os review",
			store:   func() *fakeStore { s := newFakeStore(); s.acquireErr = errors.New("down"); return s }(),
			github:  &fakeGitHub{},
			wantErr: "acquire command idempotency key",
		},
		{
			name:    "ack",
			body:    "@herd-os review",
			store:   newFakeStore(),
			github:  &fakeGitHub{err: errors.New("down")},
			wantErr: "add acknowledgement comment",
		},
		{
			name:    "record",
			body:    "@herd-os review",
			store:   func() *fakeStore { s := newFakeStore(); s.recordErr = errors.New("down"); return s }(),
			github:  &fakeGitHub{},
			wantErr: "record command",
		},
		{
			name:    "complete",
			body:    "@herd-os plan",
			store:   func() *fakeStore { s := newFakeStore(); s.completeErr = errors.New("down"); return s }(),
			github:  &fakeGitHub{},
			wantErr: "record acknowledgement intent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Handler{AppLogin: "herd-os", Store: tt.store, GitHub: tt.github}

			_, err := h.HandleIssueComment(context.Background(), validComment("OWNER", tt.body))

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

	getRepoErr   error
	acquireErr   error
	recordErr    error
	completeErr  error
	completeErrs []error
	updateErrs   []error

	idempotencyKeys map[string]store.IdempotencyKey
	commandRecords  []store.CommandRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		repo: store.Repository{
			ID:             42,
			InstallationID: 77,
			Owner:          "octo-org",
			Name:           "herd",
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
	if len(s.completeErrs) > 0 {
		err := s.completeErrs[0]
		s.completeErrs = s.completeErrs[1:]
		if err != nil {
			return err
		}
	}
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

func (s *fakeStore) GetIdempotencyKey(_ context.Context, key string) (store.IdempotencyKey, error) {
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return store.IdempotencyKey{}, store.ErrNotFound
	}
	return record, nil
}

func (s *fakeStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "failed"
	record.ResultRef = errorMessage
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

func (s *fakeStore) GetCommandRecord(_ context.Context, repoID int64, commentID int64, commandKey string) (store.CommandRecord, error) {
	for _, existing := range s.commandRecords {
		if existing.RepositoryID == repoID && existing.CommentID == commentID && existing.CommandKey == commandKey {
			return existing, nil
		}
	}
	return store.CommandRecord{}, store.ErrNotFound
}

func (s *fakeStore) UpdateCommandStatus(_ context.Context, repoID int64, commentID int64, commandKey string, status string, metadata json.RawMessage) error {
	if len(s.updateErrs) > 0 {
		err := s.updateErrs[0]
		s.updateErrs = s.updateErrs[1:]
		if err != nil {
			return err
		}
	}
	for i, existing := range s.commandRecords {
		if existing.RepositoryID == repoID && existing.CommentID == commentID && existing.CommandKey == commandKey {
			existing.Status = status
			existing.Metadata = metadata
			s.commandRecords[i] = existing
			return nil
		}
	}
	return store.ErrNotFound
}

type fakeGitHub struct {
	err      error
	errs     []error
	comments []fakeComment
}

type fakeComment struct {
	owner       string
	repo        string
	issueNumber int
	body        string
}

func (g *fakeGitHub) AddIssueComment(_ context.Context, owner, repo string, issueNumber int, body string) (int64, error) {
	if len(g.errs) > 0 {
		err := g.errs[0]
		g.errs = g.errs[1:]
		if err != nil {
			return 0, err
		}
	}
	if g.err != nil {
		return 0, g.err
	}
	g.comments = append(g.comments, fakeComment{owner: owner, repo: repo, issueNumber: issueNumber, body: body})
	return int64(1000 + len(g.comments)), nil
}

type fakeDispatcher struct {
	dispatched []DispatchCommand
	err        error
	errs       []error
}

func (d *fakeDispatcher) DispatchCommand(_ context.Context, cmd DispatchCommand) error {
	if len(d.errs) > 0 {
		err := d.errs[0]
		d.errs = d.errs[1:]
		if err != nil {
			return err
		}
	}
	if d.err != nil {
		return d.err
	}
	d.dispatched = append(d.dispatched, cmd)
	return nil
}
