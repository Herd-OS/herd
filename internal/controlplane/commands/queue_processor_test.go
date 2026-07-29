package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueuedCommentBodyReplaysPromptsWithoutDuplication(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		prompt     string
		wantPrompt string
		wantArgs   []string
	}{
		{
			name:       "quoted fix prompt with remaining lines",
			raw:        `@herd-os fix "update auth handling"`,
			prompt:     "update auth handling\ninclude regression tests",
			wantPrompt: "update auth handling\ninclude regression tests",
		},
		{
			name:       "quoted fix prompt with trailing same-line text and remaining lines",
			raw:        `@herd-os fix "update auth" include tests`,
			prompt:     "update auth include tests\nkeep multiline context",
			wantPrompt: "update auth include tests\nkeep multiline context",
		},
		{
			name:       "quoted fix-ci prompt with remaining lines",
			raw:        `@herd-os fix-ci "update auth handling"`,
			prompt:     "update auth handling\ninclude regression tests",
			wantPrompt: "update auth handling\ninclude regression tests",
		},
		{
			name:       "quoted fix-ci prompt with trailing same-line text and remaining lines",
			raw:        `@herd-os fix-ci "update auth" include tests`,
			prompt:     "update auth include tests\nkeep multiline context",
			wantPrompt: "update auth include tests\nkeep multiline context",
		},
		{
			name:       "unquoted multiline prompt",
			raw:        "@herd-os fix update auth handling",
			prompt:     "update auth handling\ninclude regression tests",
			wantPrompt: "update auth handling\ninclude regression tests",
			wantArgs:   []string{"update", "auth", "handling"},
		},
		{
			name:       "dispatch numeric argument",
			raw:        "@herd-os dispatch 42",
			prompt:     "42",
			wantPrompt: "42",
			wantArgs:   []string{"42"},
		},
		{
			name:       "retry numeric argument",
			raw:        "@herd-os retry 42",
			prompt:     "42",
			wantPrompt: "42",
			wantArgs:   []string{"42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := queuedCommentBody(tt.raw, tt.prompt)
			cmd, ok, err := ParseMentionCommand("herd-os", body)

			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.wantPrompt, cmd.Prompt)
			assert.Equal(t, tt.wantArgs, cmd.Args)
		})
	}
}

func TestQueueProcessorPreservesCommandMetadataAfterRetryableHandlerError(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	repo, err := st.UpsertRepository(ctx, store.Repository{
		Owner:          "octo",
		Name:           "herd",
		InstallationID: 101,
	})
	require.NoError(t, err)
	metadata := json.RawMessage(`{"action":"created","raw":"@herd-os dispatch 42","prompt":"42","issue_number":42,"author_association":"MEMBER"}`)
	_, err = st.RecordCommand(ctx, store.CommandRecord{
		RepositoryID: repo.ID,
		CommentID:    123,
		CommandKey:   string(CommandDispatch),
		CommandName:  string(CommandDispatch),
		Actor:        "mona",
		Status:       StatusQueued,
		Metadata:     metadata,
		CreatedAt:    time.Now().Add(-time.Minute),
	})
	require.NoError(t, err)
	gh := &fakeGitHub{}
	dispatcher := &fakeDispatcher{
		errs: []error{
			mutations.PreCallError{Op: "mint app token", Err: errors.New("temporary unavailable")},
			nil,
		},
	}
	processor := QueueProcessor{
		Store: st,
		Handler: Handler{
			AppLogin:   "herd-os",
			Store:      st,
			GitHub:     gh,
			Dispatcher: dispatcher,
		},
		Now: func() time.Time { return time.Now().UTC() },
	}

	processed, err := processor.ProcessOnce(ctx)
	require.Error(t, err)
	assert.Equal(t, 0, processed)
	record, err := st.GetCommandRecord(ctx, repo.ID, 123, string(CommandDispatch))
	require.NoError(t, err)
	assert.Equal(t, "retry_needed", record.Status)
	var body map[string]any
	require.NoError(t, json.Unmarshal(record.Metadata, &body))
	assert.Equal(t, "@herd-os dispatch 42", body["raw"])
	assert.Equal(t, "42", body["prompt"])
	assert.InDelta(t, 42, body["issue_number"], 0)
	assert.Contains(t, body["last_error"], "temporary unavailable")
	require.Len(t, gh.comments, 1)
	assert.Empty(t, dispatcher.underlyingDispatches)

	processed, err = processor.ProcessOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	record, err = st.GetCommandRecord(ctx, repo.ID, 123, string(CommandDispatch))
	require.NoError(t, err)
	assert.Equal(t, "dispatched", record.Status)
	assert.Len(t, gh.comments, 1, "acknowledgement comment must not be duplicated during retry")
	require.Len(t, dispatcher.underlyingDispatches, 1)
	assert.Equal(t, 42, dispatcher.underlyingDispatches[0].IssueNumber)
}

func TestQueueProcessorMarksUnreconstructableCommandRepairRequired(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	repo, err := st.UpsertRepository(ctx, store.Repository{
		Owner:          "octo",
		Name:           "herd",
		InstallationID: 101,
	})
	require.NoError(t, err)
	_, err = st.RecordCommand(ctx, store.CommandRecord{
		RepositoryID: repo.ID,
		CommentID:    123,
		CommandKey:   string(CommandDispatch),
		CommandName:  string(CommandDispatch),
		Actor:        "mona",
		Status:       StatusQueued,
		Metadata:     json.RawMessage(`{"raw":`),
		CreatedAt:    time.Now().Add(-time.Minute),
	})
	require.NoError(t, err)
	processor := QueueProcessor{Store: st, Handler: Handler{AppLogin: "herd-os", Store: st, GitHub: &fakeGitHub{}, Dispatcher: &fakeDispatcher{}}}

	processed, err := processor.ProcessOnce(ctx)
	require.Error(t, err)
	assert.Equal(t, 0, processed)
	record, err := st.GetCommandRecord(ctx, repo.ID, 123, string(CommandDispatch))
	require.NoError(t, err)
	assert.Equal(t, "repair_required", record.Status)
	var body map[string]any
	require.NoError(t, json.Unmarshal(record.Metadata, &body))
	assert.Equal(t, `{"raw":`, body["raw_metadata"])
	assert.Contains(t, body["repair_error"], "unmarshal queued command metadata")
}
