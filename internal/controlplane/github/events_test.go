package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvent(t *testing.T) {
	tests := []struct {
		name      string
		eventName string
		payload   string
		assert    func(t *testing.T, event Event)
	}{
		{
			name:      "installation",
			eventName: EventInstallation,
			payload: `{
				"action":"created",
				"installation":{
					"id":42,
					"account":{"login":"octo-org","id":100,"type":"Organization"},
					"target_type":"Organization",
					"repository_selection":"selected",
					"permissions":{"contents":"read"},
					"events":["pull_request"],
					"created_at":"2026-07-11T00:00:00Z",
					"updated_at":"2026-07-11T00:00:01Z"
				},
				"repositories":[{"id":99,"full_name":"octo-org/herd","default_branch":"main","private":true}]
			}`,
			assert: func(t *testing.T, event Event) {
				got := event.(InstallationEvent)
				assert.Equal(t, int64(42), got.InstallationID)
				assert.Equal(t, "octo-org", got.AccountLogin)
				assert.Equal(t, "Organization", got.TargetType)
				require.Len(t, got.Repositories, 1)
				assert.Equal(t, "octo-org", got.Repositories[0].Owner)
				assert.Equal(t, "herd", got.Repositories[0].Name)
				assert.JSONEq(t, `{"contents":"read"}`, string(got.Permissions))
			},
		},
		{
			name:      "installation repositories",
			eventName: EventInstallationRepositories,
			payload: `{
				"action":"removed",
				"installation":{"id":42,"account":{"login":"octo-org","id":100,"type":"Organization"}},
				"repository_selection":"selected",
				"repositories_added":[{"id":1,"name":"added","owner":{"login":"octo-org"}}],
				"repositories_removed":[{"id":2,"full_name":"octo-org/removed"}]
			}`,
			assert: func(t *testing.T, event Event) {
				got := event.(InstallationRepositoriesEvent)
				assert.Equal(t, int64(42), got.InstallationID)
				require.Len(t, got.RepositoriesAdded, 1)
				require.Len(t, got.RepositoriesRemoved, 1)
				assert.Equal(t, "added", got.RepositoriesAdded[0].Name)
				assert.Equal(t, "removed", got.RepositoriesRemoved[0].Name)
			},
		},
		{
			name:      "issue comment",
			eventName: EventIssueComment,
			payload: `{
				"action":"created",
				"installation":{"id":42},
				"repository":{"id":1,"full_name":"octo-org/herd"},
				"issue":{"number":5,"pull_request":{"url":"https://api.github.com/pulls/5"}},
				"comment":{"id":123,"body":"@herd-os review","author_association":"MEMBER","user":{"login":"mona","type":"User"}},
				"sender":{"login":"mona","type":"User"}
			}`,
			assert: func(t *testing.T, event Event) {
				got := event.(IssueCommentEvent)
				assert.Equal(t, 5, got.IssueNumber)
				assert.Equal(t, int64(123), got.CommentID)
				assert.Equal(t, "MEMBER", got.CommentAuthorAssociation)
				assert.Equal(t, "User", got.CommentAuthorType)
				assert.Equal(t, "mona", got.SenderLogin)
				assert.Equal(t, "User", got.SenderType)
			},
		},
		{
			name:      "pull request",
			eventName: EventPullRequest,
			payload: `{
				"action":"opened",
				"installation":{"id":42},
				"repository":{"id":1,"full_name":"octo-org/herd"},
				"pull_request":{"number":7,"head":{"sha":"head"},"base":{"sha":"base"}},
				"sender":{"login":"mona"}
			}`,
			assert: func(t *testing.T, event Event) {
				got := event.(PullRequestEvent)
				assert.Equal(t, 7, got.Number)
				assert.Equal(t, "head", got.HeadSHA)
				assert.Equal(t, "base", got.BaseSHA)
			},
		},
		{
			name:      "pull request review",
			eventName: EventPullRequestReview,
			payload: `{
				"action":"submitted",
				"installation":{"id":42},
				"repository":{"id":1,"full_name":"octo-org/herd"},
				"pull_request":{"number":7},
				"review":{"id":55,"state":"approved"},
				"sender":{"login":"mona"}
			}`,
			assert: func(t *testing.T, event Event) {
				got := event.(PullRequestReviewEvent)
				assert.Equal(t, 7, got.PullRequestNum)
				assert.Equal(t, int64(55), got.ReviewID)
				assert.Equal(t, "approved", got.State)
			},
		},
		{
			name:      "workflow run",
			eventName: EventWorkflowRun,
			payload: `{
				"action":"completed",
				"installation":{"id":42},
				"repository":{"id":1,"full_name":"octo-org/herd"},
				"workflow_run":{"id":77,"head_sha":"abc","status":"completed","conclusion":"success"},
				"sender":{"login":"mona"}
			}`,
			assert: func(t *testing.T, event Event) {
				got := event.(WorkflowRunEvent)
				assert.Equal(t, int64(77), got.WorkflowRunID)
				assert.Equal(t, "abc", got.HeadSHA)
				assert.Equal(t, "success", got.Conclusion)
			},
		},
		{
			name:      "unsupported",
			eventName: "ping",
			payload:   `{"zen":"Keep it logically awesome."}`,
			assert: func(t *testing.T, event Event) {
				assert.Nil(t, event)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ParseEvent(tt.eventName, []byte(tt.payload))
			require.NoError(t, err)
			tt.assert(t, event)
		})
	}
}

func TestParseEventMalformedPayload(t *testing.T) {
	event, err := ParseEvent(EventInstallation, []byte(`{`))
	require.Error(t, err)
	assert.Nil(t, event)
}

func TestPayloadAction(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "action", payload: `{"action":"created"}`, want: "created"},
		{name: "missing action", payload: `{}`, want: ""},
		{name: "invalid json", payload: `{`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, PayloadAction([]byte(tt.payload)))
		})
	}
}
