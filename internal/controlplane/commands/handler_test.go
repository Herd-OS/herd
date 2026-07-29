package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/mutations"
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
		{name: "valid command", event: validComment("OWNER", "@herd-os review"), wantCount: 1, wantKey: "review", wantStatus: StatusQueued},
		{name: "legacy migration command", event: validComment("OWNER", "/herd review"), wantCount: 1, wantKey: "migration", wantStatus: StatusQueued},
		{name: "unauthorized ignored", event: validComment("CONTRIBUTOR", "@herd-os review")},
		{name: "non command ignored", event: validComment("OWNER", "hello")},
		{name: "bot ignored", event: func() IssueComment {
			e := validComment("OWNER", "@herd-os review")
			e.CommentAuthorType = "Bot"
			return e
		}()},
		{name: "unknown mention command ignored durably", event: validComment("OWNER", "@herd-os nope"), wantCount: 1, wantKey: "unknown", wantStatus: StatusIgnored},
		{name: "invalid dispatch issue number ignored durably", event: validComment("OWNER", "@herd-os dispatch abc"), wantCount: 1, wantKey: "dispatch", wantStatus: StatusIgnored},
		{name: "invalid retry issue number ignored durably", event: validComment("OWNER", "@herd-os retry abc"), wantCount: 1, wantKey: "retry", wantStatus: StatusIgnored},
		{name: "edited command accepted", event: func() IssueComment { e := validComment("OWNER", "@herd-os fix"); e.Action = "edited"; return e }(), wantCount: 1, wantKey: "fix", wantStatus: StatusQueued},
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

func TestEnqueueIssueCommentCommandInvalidRecoveryCommandDuplicateIsDurableIgnored(t *testing.T) {
	tests := []struct {
		name string
		body string
		key  string
	}{
		{name: "dispatch", body: "@herd-os dispatch abc", key: "dispatch"},
		{name: "retry", body: "@herd-os retry abc", key: "retry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.NewMemoryStore()
			repo, err := st.UpsertRepository(ctx, store.Repository{InstallationID: 77, Owner: "octo-org", Name: "herd"})
			require.NoError(t, err)
			event := validComment("OWNER", tt.body)

			require.NoError(t, EnqueueIssueCommentCommand(ctx, st, "herd-os", event))
			require.NoError(t, EnqueueIssueCommentCommand(ctx, st, "herd-os", event))

			record, err := st.GetCommandRecord(ctx, repo.ID, event.CommentID, tt.key)
			require.NoError(t, err)
			assert.Equal(t, StatusIgnored, record.Status)
			assert.Contains(t, string(record.Metadata), "positive numeric issue number")
			dispatcher := &fakeDispatcher{}
			processor := QueueProcessor{
				Store: st,
				Handler: Handler{
					AppLogin:   "herd-os",
					Store:      st,
					GitHub:     &fakeGitHub{},
					Dispatcher: dispatcher,
				},
				Now: func() time.Time { return time.Now().UTC().Add(time.Hour) },
			}

			processed, err := processor.ProcessOnce(ctx)

			require.NoError(t, err)
			assert.Equal(t, 0, processed)
			assert.Empty(t, dispatcher.dispatched)
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

func TestQueueProcessorProcessesQueuedCommandOnce(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	repo, err := st.UpsertRepository(ctx, store.Repository{InstallationID: 77, Owner: "octo-org", Name: "herd"})
	require.NoError(t, err)
	require.NotZero(t, repo.ID)
	event := validComment("OWNER", "@herd-os review")
	require.NoError(t, EnqueueIssueCommentCommand(ctx, st, "herd-os", event))
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	processor := QueueProcessor{
		Store: st,
		Handler: Handler{
			AppLogin:   "herd-os",
			Store:      st,
			GitHub:     gh,
			Dispatcher: dispatcher,
		},
		Now: func() time.Time { return time.Now().UTC().Add(time.Hour) },
	}

	processed, err := processor.ProcessOnce(ctx)
	require.NoError(t, err)
	processedAgain, retryErr := processor.ProcessOnce(ctx)
	require.NoError(t, retryErr)

	assert.Equal(t, 1, processed)
	assert.Equal(t, 0, processedAgain)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	record, err := st.GetCommandRecord(ctx, repo.ID, event.CommentID, "review")
	require.NoError(t, err)
	assert.Equal(t, "dispatched", record.Status)
}

func TestQueueProcessorReplaysMultilinePrompt(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		kind       CommandKind
		wantPrompt string
	}{
		{
			name:       "fix",
			body:       "@herd-os fix\nplease update auth handling\ninclude regression tests",
			kind:       CommandFix,
			wantPrompt: "please update auth handling\ninclude regression tests",
		},
		{
			name:       "fix ci",
			body:       "@herd-os fix-ci\nplease inspect the failing job\nkeep the retry idempotent",
			kind:       CommandFixCI,
			wantPrompt: "please inspect the failing job\nkeep the retry idempotent",
		},
		{
			name:       "review",
			body:       "@herd-os review\nfocus on callback retry safety\ninclude command queue paths",
			kind:       CommandReview,
			wantPrompt: "focus on callback retry safety\ninclude command queue paths",
		},
		{
			name:       "same line plus multiline",
			body:       "@herd-os fix auth handling\ninclude regression tests",
			kind:       CommandFix,
			wantPrompt: "auth handling\ninclude regression tests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.NewMemoryStore()
			_, err := st.UpsertRepository(ctx, store.Repository{InstallationID: 77, Owner: "octo-org", Name: "herd"})
			require.NoError(t, err)
			event := validComment("OWNER", tt.body)
			require.NoError(t, EnqueueIssueCommentCommand(ctx, st, "herd-os", event))
			dispatcher := &fakeDispatcher{}
			processor := QueueProcessor{
				Store: st,
				Handler: Handler{
					AppLogin:   "herd-os",
					Store:      st,
					GitHub:     &fakeGitHub{},
					Dispatcher: dispatcher,
				},
				Now: func() time.Time { return time.Now().UTC().Add(time.Hour) },
			}

			processed, err := processor.ProcessOnce(ctx)

			require.NoError(t, err)
			assert.Equal(t, 1, processed)
			require.Len(t, dispatcher.dispatched, 1)
			assert.Equal(t, tt.kind, dispatcher.dispatched[0].Command.Kind)
			assert.Equal(t, strings.Split(tt.body, "\n")[0], dispatcher.dispatched[0].Command.Raw)
			assert.Equal(t, tt.wantPrompt, dispatcher.dispatched[0].Command.Prompt)
		})
	}
}

func TestQueueProcessorReplaysAcknowledgedCommandWithCompletedAckIdempotency(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	repo, err := st.UpsertRepository(ctx, store.Repository{InstallationID: 77, Owner: "octo-org", Name: "herd"})
	require.NoError(t, err)
	event := validComment("OWNER", "@herd-os review")
	require.NoError(t, EnqueueIssueCommentCommand(ctx, st, "herd-os", event))
	key := fmt.Sprintf("repo:%d:comment:%d:command:%s", repo.ID, event.CommentID, "review")
	created, err := st.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       key,
		Scope:     "issue_comment_command",
		Status:    "completed",
		ResultRef: "issue_comment:9001",
		CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.True(t, created)

	dispatcher := &fakeDispatcher{}
	processor := QueueProcessor{
		Store: st,
		Handler: Handler{
			AppLogin:   "herd-os",
			Store:      st,
			GitHub:     &fakeGitHub{},
			Dispatcher: dispatcher,
		},
		Now: func() time.Time { return time.Now().UTC().Add(time.Hour) },
	}

	processed, err := processor.ProcessOnce(ctx)

	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Len(t, dispatcher.dispatched, 1)
	record, err := st.GetCommandRecord(ctx, repo.ID, event.CommentID, "review")
	require.NoError(t, err)
	assert.Equal(t, "dispatched", record.Status)
	idem, err := st.GetIdempotencyKey(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "completed", idem.Status)
	assert.Equal(t, "dispatch:completed", idem.ResultRef)
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
	tests := []struct {
		name string
		body string
	}{
		{name: "review", body: "/herd review"},
		{name: "resolve conflicts", body: "/herd resolve-conflicts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			gh := &fakeGitHub{}
			dispatcher := &fakeDispatcher{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

			result, err := h.HandleIssueComment(context.Background(), validComment("OWNER", tt.body))

			require.NoError(t, err)
			assert.Equal(t, StatusIgnored, result.Status)
			require.Len(t, gh.comments, 1)
			assert.Contains(t, gh.comments[0].body, "@herd-os <command>")
			assert.Len(t, st.commandRecords, 1)
			assert.Equal(t, "migration", st.commandRecords[0].CommandKey)
			assert.Empty(t, dispatcher.dispatched)
		})
	}
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
	assert.Len(t, dispatcher.underlyingDispatches, 1)
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
	assert.Len(t, dispatcher.underlyingDispatches, 1)
}

func TestHandlerDispatchesServiceCommandsAfterAcknowledgement(t *testing.T) {
	tests := []struct {
		body      string
		kind      CommandKind
		wantIssue int
		wantPR    int
	}{
		{body: "@herd-os review", kind: CommandReview, wantIssue: 7, wantPR: 7},
		{body: "@herd-os fix update auth error handling", kind: CommandFix, wantIssue: 0, wantPR: 7},
		{body: "@herd-os fix-ci failing tests mention missing env var", kind: CommandFixCI, wantIssue: 0, wantPR: 7},
		{body: "@herd-os resolve-conflicts", kind: CommandResolveConflicts, wantIssue: 7, wantPR: 7},
		{body: "@herd-os retry 42", kind: CommandRetry, wantIssue: 42, wantPR: 7},
		{body: "@herd-os integrate", kind: CommandIntegrate, wantIssue: 7, wantPR: 7},
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
			assert.Equal(t, fmt.Sprintf("Acknowledged `@herd-os %s`.", tt.kind), gh.comments[0].body)
			require.Len(t, dispatcher.dispatched, 1)
			assert.Equal(t, tt.kind, dispatcher.dispatched[0].Command.Kind)
			assert.Equal(t, tt.wantIssue, dispatcher.dispatched[0].IssueNumber)
			assert.Equal(t, tt.wantPR, dispatcher.dispatched[0].PRNumber)
		})
	}
}

func TestHandlerAcknowledgementUsesConfiguredAppLogin(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "custom-herd", Store: st, GitHub: gh, Dispatcher: dispatcher}

	result, err := h.HandleIssueComment(context.Background(), validComment("OWNER", "@custom-herd review"))

	require.NoError(t, err)
	assert.Equal(t, StatusAcknowledged, result.Status)
	require.Len(t, gh.comments, 1)
	assert.Equal(t, "Acknowledged `@custom-herd review`.", gh.comments[0].body)
	require.Len(t, dispatcher.dispatched, 1)
}

func TestHandlerDispatchesDispatchCommandFromIssueComments(t *testing.T) {
	tests := []struct {
		name string
		pr   bool
	}{
		{name: "issue comment"},
		{name: "PR comment", pr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeStore()
			gh := &fakeGitHub{}
			dispatcher := &fakeDispatcher{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
			event := validComment("OWNER", "@herd-os dispatch 42")
			if !tt.pr {
				event.PullRequestURL = ""
			}

			result, err := h.HandleIssueComment(context.Background(), event)
			duplicate, duplicateErr := h.HandleIssueComment(context.Background(), event)

			require.NoError(t, err)
			require.NoError(t, duplicateErr)
			assert.Equal(t, StatusAcknowledged, result.Status)
			assert.Equal(t, StatusAcknowledged, duplicate.Status)
			require.Len(t, dispatcher.dispatched, 1)
			assert.Equal(t, CommandDispatch, dispatcher.dispatched[0].Command.Kind)
			assert.Equal(t, 42, dispatcher.dispatched[0].IssueNumber)
			if tt.pr {
				assert.Equal(t, 7, dispatcher.dispatched[0].PRNumber)
			} else {
				assert.Equal(t, 0, dispatcher.dispatched[0].PRNumber)
			}
		})
	}
}

func TestHandlerDispatchesRetryCommandFromIssueComments(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os retry 42")
	event.PullRequestURL = ""

	result, err := h.HandleIssueComment(context.Background(), event)
	duplicate, duplicateErr := h.HandleIssueComment(context.Background(), event)

	require.NoError(t, err)
	require.NoError(t, duplicateErr)
	assert.Equal(t, StatusAcknowledged, result.Status)
	assert.Equal(t, StatusAcknowledged, duplicate.Status)
	assert.Len(t, gh.comments, 1)
	require.Len(t, dispatcher.dispatched, 1)
	assert.Equal(t, CommandRetry, dispatcher.dispatched[0].Command.Kind)
	assert.Equal(t, 42, dispatcher.dispatched[0].IssueNumber)
	assert.Equal(t, 0, dispatcher.dispatched[0].PRNumber)
}

func TestHandlerRejectsInvalidNumericCommandTarget(t *testing.T) {
	tests := []string{"@herd-os dispatch nope", "@herd-os dispatch 0", "@herd-os dispatch -1", "@herd-os retry nope", "@herd-os retry 0", "@herd-os retry -1"}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			st := newFakeStore()
			gh := &fakeGitHub{}
			dispatcher := &fakeDispatcher{}
			h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

			_, err := h.HandleIssueComment(context.Background(), validComment("OWNER", body))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "positive numeric issue number")
			assert.Empty(t, gh.comments)
			assert.Empty(t, dispatcher.dispatched)
			assert.Empty(t, st.commandRecords)
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
		{body: "@herd-os integrate", kind: CommandIntegrate},
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
	event := validComment("OWNER", "@herd-os review")
	event.PullRequestURL = ""

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "record acknowledgement comment")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Empty(t, dispatcher.dispatched)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "issue_comment:1001", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, StatusAcknowledged, st.commandRecords[0].Status)
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"prompt":"","author_association":"OWNER","raw":"@herd-os review"}`, string(st.commandRecords[0].Metadata))
}

func TestHandlerDispatchFailureOccursAfterAcknowledgement(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{errs: []error{mutations.PreCallError{Op: "build request", Err: errors.New("dispatch down")}, nil}}
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
	assert.Len(t, dispatcher.dispatched, 2)
	assert.Len(t, dispatcher.underlyingDispatches, 1)
	for _, record := range st.idempotencyKeys {
		assert.Equal(t, "completed", record.Status)
	}
}

func TestHandlerDispatchUnknownOutcomeRedeliveryDelegatesToDispatcher(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{dispatchThenErrs: []error{errors.New("workflow dispatch outcome unknown")}}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow dispatch outcome unknown")
	require.Error(t, retryErr)
	assert.Contains(t, retryErr.Error(), "repair required")
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 2)
	assert.Len(t, dispatcher.underlyingDispatches, 1)
	key := "repo:42:comment:123:command:review"
	require.Contains(t, st.idempotencyKeys, key)
	assert.Equal(t, "completed", st.idempotencyKeys[key].Status)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, StatusDispatching, st.commandRecords[0].Status)
}

func TestHandlerDispatchingRecordBeforeDispatcherCallRedeliveryDispatchesOnce(t *testing.T) {
	st := newFakeStore()
	metadata := json.RawMessage(`{"ack_comment_id":1001,"action":"created","args":null,"prompt":"","author_association":"OWNER","issue_number":7,"pr_number":7,"raw":"@herd-os review"}`)
	st.idempotencyKeys["repo:42:comment:123:command:review"] = store.IdempotencyKey{
		Key:       "repo:42:comment:123:command:review",
		Scope:     "issue_comment_command",
		Status:    "completed",
		ResultRef: "issue_comment:1001",
		CreatedAt: time.Now().UTC(),
	}
	st.commandRecords = append(st.commandRecords, store.CommandRecord{
		RepositoryID: 42,
		CommentID:    123,
		CommandKey:   "review",
		CommandName:  "review",
		Actor:        "mona",
		Status:       "dispatching",
		Metadata:     metadata,
		CreatedAt:    time.Now().UTC(),
	})
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

	result, err := h.HandleIssueComment(context.Background(), validComment("OWNER", "@herd-os review"))
	duplicate, duplicateErr := h.HandleIssueComment(context.Background(), validComment("OWNER", "@herd-os review"))

	require.NoError(t, err)
	require.NoError(t, duplicateErr)
	assert.Equal(t, StatusAcknowledged, result.Status)
	assert.Equal(t, StatusAcknowledged, duplicate.Status)
	assert.Empty(t, gh.comments)
	assert.Len(t, dispatcher.dispatched, 1)
	assert.Len(t, dispatcher.underlyingDispatches, 1)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, "dispatched", st.commandRecords[0].Status)
	assert.Equal(t, "completed", st.idempotencyKeys["repo:42:comment:123:command:review"].Status)
	assert.Equal(t, "dispatch:completed", st.idempotencyKeys["repo:42:comment:123:command:review"].ResultRef)
}

func TestHandlerAcknowledgementFailureRedeliveryDoesNotDispatchUntilAckRecorded(t *testing.T) {
	st := newFakeStore()
	gh := &fakeGitHub{createThenErrs: []error{errors.New("github down")}}
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
	assert.Equal(t, "dispatch:completed", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, "dispatched", st.commandRecords[0].Status)
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"prompt":"","author_association":"OWNER","issue_number":7,"pr_number":7,"raw":"@herd-os review"}`, string(st.commandRecords[0].Metadata))
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
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"prompt":"","author_association":"OWNER","issue_number":7,"pr_number":7,"raw":"@herd-os review"}`, string(st.commandRecords[0].Metadata))
}

func TestHandlerFailedPostCallAcknowledgementRecordDoesNotAckAgain(t *testing.T) {
	st := newFakeStore()
	key := "repo:42:comment:123:command:review"
	st.idempotencyKeys[key] = store.IdempotencyKey{
		Key:       key,
		Scope:     "issue_comment_command",
		Status:    mutations.LegacyFailed,
		ResultRef: mutations.PhaseRepairRequired + ":unknown outcome after acknowledgement call",
		CreatedAt: time.Now().UTC(),
	}
	gh := &fakeGitHub{comments: []fakeComment{{
		owner:       "octo",
		repo:        "widgets",
		issueNumber: 7,
		body:        "Acknowledged `@herd-os review`.",
	}}}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}

	_, err := h.HandleIssueComment(context.Background(), validComment("OWNER", "@herd-os review"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "repair required")
	assert.Len(t, gh.comments, 1)
	assert.Empty(t, dispatcher.dispatched)
}

func TestHandlerAcknowledgementRecordAndFallbackCompletionFailureDoesNotAckAgain(t *testing.T) {
	st := newFakeStore()
	st.updateErrs = []error{errors.New("store down"), nil}
	st.completeErrs = []error{errors.New("idempotency down")}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)
	_, finalErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete idempotency key")
	require.Error(t, retryErr)
	assert.Contains(t, retryErr.Error(), "record acknowledgement comment")
	require.NoError(t, finalErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "dispatch:completed", st.idempotencyKeys[key].ResultRef)
}

func TestHandlerAcknowledgementCompletionFailureRedeliveryDoesNotAckAgain(t *testing.T) {
	st := newFakeStore()
	st.completeErrs = []error{errors.New("store down"), nil, nil}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")
	event.PullRequestURL = ""

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete idempotency key")
	require.NoError(t, retryErr)
	assert.Len(t, gh.comments, 1)
	assert.Empty(t, dispatcher.dispatched)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "issue_comment:1001", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.JSONEq(t, `{"ack_comment_id":1001,"action":"created","args":null,"prompt":"","author_association":"OWNER","raw":"@herd-os review"}`, string(st.commandRecords[0].Metadata))
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
	_, finalErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete command idempotency key")
	require.NoError(t, retryErr)
	require.NoError(t, finalErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 2)
	assert.Len(t, dispatcher.underlyingDispatches, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "dispatch:completed", st.idempotencyKeys[key].ResultRef)
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
	st.updateErrs = []error{nil, nil, errors.New("store down"), nil}
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
	st.updateErrs = []error{nil, nil}
	st.completeErrs = []error{nil, errors.New("idempotency down")}
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{}
	h := Handler{AppLogin: "herd-os", Store: st, GitHub: gh, Dispatcher: dispatcher}
	event := validComment("OWNER", "@herd-os review")

	_, err := h.HandleIssueComment(context.Background(), event)
	_, retryErr := h.HandleIssueComment(context.Background(), event)
	_, finalErr := h.HandleIssueComment(context.Background(), event)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete command idempotency key")
	require.NoError(t, retryErr)
	require.NoError(t, finalErr)
	assert.Len(t, gh.comments, 1)
	assert.Len(t, dispatcher.dispatched, 2)
	assert.Len(t, dispatcher.underlyingDispatches, 1)
	key := "repo:42:comment:123:command:review"
	require.Equal(t, "completed", st.idempotencyKeys[key].Status)
	assert.Equal(t, "dispatch:completed", st.idempotencyKeys[key].ResultRef)
	require.Len(t, st.commandRecords, 1)
	assert.Equal(t, "dispatched", st.commandRecords[0].Status)
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
			body:    "@herd-os dispatch",
			store:   func() *fakeStore { s := newFakeStore(); s.completeErr = errors.New("down"); return s }(),
			github:  &fakeGitHub{},
			wantErr: "complete idempotency key",
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

	idempotencyKeys  map[string]store.IdempotencyKey
	mutationAttempts map[string]store.GitHubMutationAttempt
	commandRecords   []store.CommandRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		repo: store.Repository{
			ID:             42,
			InstallationID: 77,
			Owner:          "octo-org",
			Name:           "herd",
		},
		idempotencyKeys:  map[string]store.IdempotencyKey{},
		mutationAttempts: map[string]store.GitHubMutationAttempt{},
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
	record.Status, record.ResultRef = commandIdempotencyFailureStatus(errorMessage)
	record.CompletedAt = &now
	s.idempotencyKeys[key] = record
	return nil
}

func (s *fakeStore) TryStartIdempotencyKey(_ context.Context, key string, toStatus string, resultRef string, retryableFailedPrefix string) (store.IdempotencyStartResult, error) {
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return store.IdempotencyStartResult{}, store.ErrNotFound
	}
	retryableFailed := record.Status == mutations.LegacyFailed && strings.HasPrefix(record.ResultRef, retryableFailedPrefix)
	if record.Status != mutations.PhaseIntentRecorded && record.Status != mutations.PhaseFailedPreCall && !retryableFailed {
		return store.IdempotencyStartResult{Started: false, Record: record}, nil
	}
	record.Status = toStatus
	record.ResultRef = resultRef
	s.idempotencyKeys[key] = record
	return store.IdempotencyStartResult{Started: true, Record: record}, nil
}

func (s *fakeStore) RecordGitHubMutationAttempt(_ context.Context, a store.GitHubMutationAttempt) error {
	if _, ok := s.mutationAttempts[a.IdempotencyKey]; ok {
		return store.ErrAlreadyExists
	}
	s.mutationAttempts[a.IdempotencyKey] = a
	return nil
}

func (s *fakeStore) GetGitHubMutationAttempt(_ context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error) {
	attempt, ok := s.mutationAttempts[idempotencyKey]
	if !ok {
		return store.GitHubMutationAttempt{}, store.ErrNotFound
	}
	return attempt, nil
}

func (s *fakeStore) CompleteGitHubMutationAttempt(_ context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	attempt, ok := s.mutationAttempts[idempotencyKey]
	if !ok {
		return store.ErrNotFound
	}
	attempt.Status = status
	attempt.Response = response
	attempt.Error = errorMessage
	attempt.CompletedAt = &completedAt
	s.mutationAttempts[idempotencyKey] = attempt
	return nil
}

func (s *fakeStore) TryStartGitHubMutationAttempt(_ context.Context, idempotencyKey string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error) {
	attempt, ok := s.mutationAttempts[idempotencyKey]
	if !ok {
		return store.GitHubMutationStartResult{}, store.ErrNotFound
	}
	for _, status := range allowedStatuses {
		if attempt.Status == status {
			attempt.Status = mutations.PhaseCallStarted
			attempt.CompletedAt = &completedAt
			s.mutationAttempts[idempotencyKey] = attempt
			return store.GitHubMutationStartResult{Started: true, Attempt: attempt}, nil
		}
	}
	return store.GitHubMutationStartResult{Started: false, Attempt: attempt}, nil
}

func commandIdempotencyFailureStatus(errorMessage string) (string, string) {
	message := strings.TrimSpace(errorMessage)
	for _, phase := range []string{mutations.PhaseFailedPreCall, mutations.PhaseRepairRequired} {
		prefix := phase + ":"
		if message == phase {
			return phase, ""
		}
		if strings.HasPrefix(message, prefix) {
			return phase, strings.TrimSpace(strings.TrimPrefix(message, prefix))
		}
	}
	return mutations.LegacyFailed, errorMessage
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
	err            error
	errs           []error
	createThenErrs []error
	comments       []fakeComment
}

type fakeComment struct {
	owner       string
	repo        string
	issueNumber int
	body        string
}

func (g *fakeGitHub) AddIssueComment(_ context.Context, owner, repo string, issueNumber int, body string) (int64, error) {
	if len(g.createThenErrs) > 0 {
		err := g.createThenErrs[0]
		g.createThenErrs = g.createThenErrs[1:]
		g.comments = append(g.comments, fakeComment{owner: owner, repo: repo, issueNumber: issueNumber, body: body})
		return int64(1000 + len(g.comments)), err
	}
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

func (g *fakeGitHub) ListIssueComments(_ context.Context, owner, repo string, issueNumber int) ([]IssueCommentSummary, error) {
	var out []IssueCommentSummary
	for i, comment := range g.comments {
		if comment.owner == owner && comment.repo == repo && comment.issueNumber == issueNumber {
			out = append(out, IssueCommentSummary{ID: int64(1001 + i), Body: comment.body})
		}
	}
	return out, nil
}

type fakeDispatcher struct {
	dispatched           []DispatchCommand
	underlyingDispatches []DispatchCommand
	err                  error
	errs                 []error
	dispatchThenErrs     []error
	completed            map[string]bool
	unknown              map[string]bool
}

func (d *fakeDispatcher) DispatchCommand(_ context.Context, cmd DispatchCommand) error {
	d.dispatched = append(d.dispatched, cmd)
	key := fmt.Sprintf("%d:%d:%s", cmd.RepositoryID, cmd.CommentID, cmd.Command.Kind)
	if d.unknown != nil && d.unknown[key] {
		return fmt.Errorf("workflow dispatch %q outcome is unknown after GitHub accepted dispatch; repair required", key)
	}
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
	if d.completed == nil {
		d.completed = map[string]bool{}
	}
	if d.completed[key] {
		return nil
	}
	d.completed[key] = true
	d.underlyingDispatches = append(d.underlyingDispatches, cmd)
	if len(d.dispatchThenErrs) > 0 {
		err := d.dispatchThenErrs[0]
		d.dispatchThenErrs = d.dispatchThenErrs[1:]
		if err != nil {
			if d.unknown == nil {
				d.unknown = map[string]bool{}
			}
			d.unknown[key] = true
			return err
		}
	}
	return nil
}
