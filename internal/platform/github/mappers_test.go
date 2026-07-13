package github

import (
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
)

func TestMapIssue(t *testing.T) {
	ghIssue := &gh.Issue{
		Number:  gh.Ptr(42),
		Title:   gh.Ptr("Add auth"),
		Body:    gh.Ptr("## Task\n\nDo it."),
		State:   gh.Ptr("open"),
		HTMLURL: gh.Ptr("https://github.com/org/repo/issues/42"),
		Labels: []*gh.Label{
			{Name: gh.Ptr("herd/status:ready")},
			{Name: gh.Ptr("herd/type:feature")},
		},
		Assignees: []*gh.User{
			{Login: gh.Ptr("alice")},
		},
		Milestone: &gh.Milestone{
			Number:      gh.Ptr(5),
			Title:       gh.Ptr("M1"),
			Description: gh.Ptr("First milestone"),
			State:       gh.Ptr("open"),
		},
	}

	issue := mapIssue(ghIssue)

	assert.Equal(t, 42, issue.Number)
	assert.Equal(t, "Add auth", issue.Title)
	assert.Equal(t, "## Task\n\nDo it.", issue.Body)
	assert.Equal(t, "open", issue.State)
	assert.Equal(t, []string{"herd/status:ready", "herd/type:feature"}, issue.Labels)
	assert.Equal(t, []string{"alice"}, issue.Assignees)
	assert.Equal(t, "https://github.com/org/repo/issues/42", issue.URL)
	assert.NotNil(t, issue.Milestone)
	assert.Equal(t, 5, issue.Milestone.Number)
	assert.Equal(t, "M1", issue.Milestone.Title)
}

func TestMapIssueNoMilestone(t *testing.T) {
	ghIssue := &gh.Issue{
		Number: gh.Ptr(1),
		Title:  gh.Ptr("Simple"),
		State:  gh.Ptr("open"),
	}

	issue := mapIssue(ghIssue)

	assert.Equal(t, 1, issue.Number)
	assert.Nil(t, issue.Milestone)
	assert.Empty(t, issue.Labels)
	assert.Empty(t, issue.Assignees)
}

func TestMapMilestone(t *testing.T) {
	due := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	ghMilestone := &gh.Milestone{
		Number:       gh.Ptr(5),
		Title:        gh.Ptr("M1: Foundation"),
		Description:  gh.Ptr("Project skeleton"),
		State:        gh.Ptr("open"),
		OpenIssues:   gh.Ptr(3),
		ClosedIssues: gh.Ptr(8),
		DueOn:        &gh.Timestamp{Time: due},
	}

	m := mapMilestone(ghMilestone)

	assert.Equal(t, 5, m.Number)
	assert.Equal(t, "M1: Foundation", m.Title)
	assert.Equal(t, "Project skeleton", m.Description)
	assert.Equal(t, "open", m.State)
	assert.Equal(t, 3, m.OpenIssues)
	assert.Equal(t, 8, m.ClosedIssues)
	assert.NotNil(t, m.DueDate)
	assert.Equal(t, due, *m.DueDate)
}

func TestMapMilestoneNoDueDate(t *testing.T) {
	ghMilestone := &gh.Milestone{
		Number: gh.Ptr(1),
		Title:  gh.Ptr("M1"),
		State:  gh.Ptr("open"),
	}

	m := mapMilestone(ghMilestone)

	assert.Nil(t, m.DueDate)
}

func TestMapRun(t *testing.T) {
	ghRun := &gh.WorkflowRun{
		ID:         gh.Ptr(int64(12345678)),
		Name:       gh.Ptr("CI"),
		Path:       gh.Ptr(".github/workflows/ci.yml"),
		WorkflowID: gh.Ptr(int64(42)),
		HeadBranch: gh.Ptr("herd/worker/42"),
		HeadSHA:    gh.Ptr("abc123"),
		Status:     gh.Ptr("completed"),
		Conclusion: gh.Ptr("success"),
		HTMLURL:    gh.Ptr("https://github.com/org/repo/actions/runs/12345678"),
	}

	run := mapRun(ghRun)

	assert.Equal(t, int64(12345678), run.ID)
	assert.Equal(t, int64(42), run.WorkflowID)
	assert.Equal(t, "CI", run.WorkflowName)
	assert.Equal(t, ".github/workflows/ci.yml", run.WorkflowPath)
	assert.Equal(t, "herd/worker/42", run.HeadBranch)
	assert.Equal(t, "abc123", run.HeadSHA)
	assert.Equal(t, "completed", run.Status)
	assert.Equal(t, "success", run.Conclusion)
	assert.Equal(t, "https://github.com/org/repo/actions/runs/12345678", run.URL)
}

func TestMapRunInProgress(t *testing.T) {
	ghRun := &gh.WorkflowRun{
		ID:     gh.Ptr(int64(99)),
		Status: gh.Ptr("in_progress"),
	}

	run := mapRun(ghRun)

	assert.Equal(t, "in_progress", run.Status)
	assert.Equal(t, "", run.Conclusion)
}

func TestMapRunner(t *testing.T) {
	ghRunner := &gh.Runner{
		ID:     gh.Ptr(int64(1)),
		Name:   gh.Ptr("herd-worker-1"),
		Status: gh.Ptr("online"),
		Busy:   gh.Ptr(true),
		Labels: []*gh.RunnerLabels{
			{Name: gh.Ptr("self-hosted")},
			{Name: gh.Ptr("herd-worker")},
		},
	}

	runner := mapRunner(ghRunner)

	assert.Equal(t, int64(1), runner.ID)
	assert.Equal(t, "herd-worker-1", runner.Name)
	assert.Equal(t, "online", runner.Status)
	assert.True(t, runner.Busy)
	assert.Equal(t, []string{"self-hosted", "herd-worker"}, runner.Labels)
}

func TestMapRunnerIdle(t *testing.T) {
	ghRunner := &gh.Runner{
		ID:     gh.Ptr(int64(2)),
		Name:   gh.Ptr("herd-worker-2"),
		Status: gh.Ptr("offline"),
		Busy:   gh.Ptr(false),
	}

	runner := mapRunner(ghRunner)

	assert.False(t, runner.Busy)
	assert.Equal(t, "offline", runner.Status)
	assert.Empty(t, runner.Labels)
}

func TestMapPullRequest_Labels(t *testing.T) {
	ghPR := &gh.PullRequest{
		Number:         gh.Ptr(7),
		Title:          gh.Ptr("Batch PR"),
		Body:           gh.Ptr("body"),
		State:          gh.Ptr("open"),
		HTMLURL:        gh.Ptr("https://github.com/org/repo/pull/7"),
		Head:           &gh.PullRequestBranch{Ref: gh.Ptr("herd/batch/1-x")},
		Base:           &gh.PullRequestBranch{Ref: gh.Ptr("main")},
		MergeableState: gh.Ptr("clean"),
		Labels: []*gh.Label{
			{Name: gh.Ptr("herd/cascade-failed")},
			{Name: gh.Ptr("herd/ci-fix-pending")},
		},
	}

	pr := mapPullRequest(ghPR)

	assert.Equal(t, 7, pr.Number)
	assert.Equal(t, "Batch PR", pr.Title)
	assert.Equal(t, "open", pr.State)
	assert.Equal(t, "herd/batch/1-x", pr.Head)
	assert.Equal(t, "main", pr.Base)
	assert.Equal(t, []string{"herd/cascade-failed", "herd/ci-fix-pending"}, pr.Labels)
	assert.Equal(t, "clean", pr.MergeStateStatus)
}

func TestMapPullRequest_NoLabels(t *testing.T) {
	ghPR := &gh.PullRequest{
		Number: gh.Ptr(1),
		State:  gh.Ptr("open"),
	}

	pr := mapPullRequest(ghPR)
	assert.Empty(t, pr.Labels)
}

func TestMapPullRequest_NilInput(t *testing.T) {
	// Guards against a panic if a caller hands us a nil pull request.
	assert.Nil(t, mapPullRequest(nil))
}

func TestMapLabel(t *testing.T) {
	ghLabel := &gh.Label{
		Name:        gh.Ptr("herd/status:ready"),
		Color:       gh.Ptr("0e8a16"),
		Description: gh.Ptr("Task is ready for dispatch"),
	}

	label := mapLabel(ghLabel)

	assert.Equal(t, "herd/status:ready", label.Name)
	assert.Equal(t, "0e8a16", label.Color)
	assert.Equal(t, "Task is ready for dispatch", label.Description)
}
