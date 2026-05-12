package planner

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func alreadyExistsErr() error {
	return &gh.ErrorResponse{
		Response: &http.Response{
			StatusCode: 422,
			Request: &http.Request{
				Method: "POST",
				URL:    &url.URL{Path: "/repos/o/r/milestones"},
			},
		},
		Errors: []gh.Error{{Code: "already_exists", Resource: "Milestone", Field: "title"}},
	}
}

func basicPlan() *agent.Plan {
	return &agent.Plan{
		BatchName: "Foo",
		Tasks: []agent.PlannedTask{
			{
				Title:              "Task A",
				Description:        "Do A",
				AcceptanceCriteria: []string{"A works"},
				Scope:              []string{"a.go"},
				Complexity:         "low",
			},
		},
	}
}

func TestCreateFromPlan_MilestoneFirstTryUnique(t *testing.T) {
	plan := basicPlan()
	mock := newMockPlatform()

	result, err := CreateFromPlan(context.Background(), mock, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, mock.milestones.created, 1)
	assert.Equal(t, "Foo", mock.milestones.created[0].title)
	assert.Equal(t, 1, result.MilestoneNumber)
	assert.Equal(t, "Foo", plan.BatchName)
	assert.Equal(t, "herd/batch/1-foo", result.BatchBranch)
}

func TestCreateFromPlan_MilestoneRetryWithSuffix(t *testing.T) {
	plan := basicPlan()
	mock := newMockPlatform()

	callCount := 0
	mock.milestones.CreateFunc = func(_ context.Context, title, _ string, _ *time.Time) (*platform.Milestone, error) {
		callCount++
		if callCount == 1 {
			assert.Equal(t, "Foo", title)
			return nil, alreadyExistsErr()
		}
		assert.Equal(t, "Foo (2)", title)
		return &platform.Milestone{Number: 7, Title: title}, nil
	}

	result, err := CreateFromPlan(context.Background(), mock, plan, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 2, callCount)
	require.Len(t, mock.milestones.created, 2)
	assert.Equal(t, "Foo", mock.milestones.created[0].title)
	assert.Equal(t, "Foo (2)", mock.milestones.created[1].title)
	assert.Equal(t, "Foo (2)", plan.BatchName)
	assert.Equal(t, 7, result.MilestoneNumber)
	assert.Equal(t, "herd/batch/7-foo-2", result.BatchBranch)
}

func TestCreateFromPlan_MilestoneMaxRetriesExceeded(t *testing.T) {
	plan := basicPlan()
	mock := newMockPlatform()

	var titles []string
	mock.milestones.CreateFunc = func(_ context.Context, title, _ string, _ *time.Time) (*platform.Milestone, error) {
		titles = append(titles, title)
		return nil, alreadyExistsErr()
	}

	result, err := CreateFromPlan(context.Background(), mock, plan, nil)
	assert.Nil(t, result)
	require.Error(t, err)

	expected := []string{
		"Foo",
		"Foo (2)",
		"Foo (3)",
		"Foo (4)",
		"Foo (5)",
		"Foo (6)",
		"Foo (7)",
		"Foo (8)",
		"Foo (9)",
		"Foo (10)",
	}
	assert.Equal(t, expected, titles)
	assert.Len(t, mock.milestones.created, 10)

	var gerr *gh.ErrorResponse
	require.True(t, errors.As(err, &gerr), "expected wrapped *gh.ErrorResponse, got %v", err)
	require.Len(t, gerr.Errors, 1)
	assert.Equal(t, "already_exists", gerr.Errors[0].Code)
}

func TestCreateFromPlan_MilestoneOtherErrorPropagates(t *testing.T) {
	plan := basicPlan()
	mock := newMockPlatform()

	boom := errors.New("500 internal server error")
	callCount := 0
	mock.milestones.CreateFunc = func(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
		callCount++
		return nil, boom
	}

	result, err := CreateFromPlan(context.Background(), mock, plan, nil)
	assert.Nil(t, result)
	require.Error(t, err)

	assert.Equal(t, 1, callCount)
	assert.Len(t, mock.milestones.created, 1)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, "Foo", plan.BatchName)
}
