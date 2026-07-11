package review

import (
	"context"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetHerdReviewStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		repo       Repository
		state      ReviewStatusState
		wantStatus bool
		wantErr    string
	}{
		{name: "pending on dispatch", repo: testRepo(true), state: ReviewStatusPending, wantStatus: true},
		{name: "success", repo: testRepo(true), state: ReviewStatusSuccess, wantStatus: true},
		{name: "failure", repo: testRepo(true), state: ReviewStatusFailure, wantStatus: true},
		{name: "review disabled", repo: testRepo(false), state: ReviewStatusPending},
		{name: "invalid state", repo: testRepo(true), state: "neutral", wantErr: "unsupported Herd Review status state"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStatusStore{}
			gh := &fakeStatusGitHub{}
			svc := StatusService{Store: st, GitHub: gh, Now: func() time.Time { return now }}

			err := svc.SetHerdReviewStatus(ctx, tt.repo, 42, "head-sha", tt.state, "desc", "https://example.test/run")

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if !tt.wantStatus {
				assert.Empty(t, gh.statuses)
				assert.Empty(t, st.states)
				return
			}
			require.Len(t, gh.statuses, 1)
			assert.Equal(t, int64(99), gh.statuses[0].installationID)
			assert.Equal(t, "octo", gh.statuses[0].owner)
			assert.Equal(t, "widgets", gh.statuses[0].repo)
			assert.Equal(t, "head-sha", gh.statuses[0].sha)
			assert.Equal(t, string(tt.state), gh.statuses[0].status.State)
			assert.Equal(t, HerdReviewContext, gh.statuses[0].status.Context)
			assert.Equal(t, "https://example.test/run", gh.statuses[0].status.TargetURL)
			require.Len(t, st.states, 1)
			assert.Equal(t, string(tt.state), st.states[0].Status)
			assert.Equal(t, now, st.states[0].UpdatedAt)
			assert.Contains(t, string(st.states[0].Metadata), "herd_review_status:7:42:head-sha:Herd Review")
		})
	}
}

func TestSetHerdReviewStatusAllowsNewHeadPendingAfterSuccess(t *testing.T) {
	ctx := context.Background()
	st := &fakeStatusStore{}
	gh := &fakeStatusGitHub{}
	svc := StatusService{Store: st, GitHub: gh}

	require.NoError(t, svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "old-head", ReviewStatusSuccess, "approved", ""))
	require.NoError(t, svc.SetHerdReviewStatus(ctx, testRepo(true), 42, "new-head", ReviewStatusPending, "new commit", ""))

	require.Len(t, gh.statuses, 2)
	assert.Equal(t, "old-head", gh.statuses[0].sha)
	assert.Equal(t, "success", gh.statuses[0].status.State)
	assert.Equal(t, "new-head", gh.statuses[1].sha)
	assert.Equal(t, "pending", gh.statuses[1].status.State)
}

type fakeStatusStore struct {
	states []store.ReviewState
}

func (s *fakeStatusStore) SetReviewState(_ context.Context, state store.ReviewState) error {
	s.states = append(s.states, state)
	return nil
}

type fakeStatusGitHub struct {
	statuses []capturedStatus
	err      error
}

type capturedStatus struct {
	installationID int64
	owner          string
	repo           string
	sha            string
	status         platform.CommitStatus
}

func (g *fakeStatusGitHub) CreateCommitStatus(_ context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) error {
	if g.err != nil {
		return g.err
	}
	g.statuses = append(g.statuses, capturedStatus{installationID: installationID, owner: owner, repo: repo, sha: sha, status: status})
	return nil
}

func testRepo(enabled bool) Repository {
	return Repository{ID: 7, InstallationID: 99, Owner: "octo", Name: "widgets", ReviewEnabled: enabled}
}
