package planner

import (
	"context"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "Add JWT authentication", "add-jwt-authentication"},
		{"special chars", "Fix bug #123: login (broken)", "fix-bug-123-login-broken"},
		{"underscores", "my_feature_name", "my-feature-name"},
		{"multiple spaces", "too   many   spaces", "too-many-spaces"},
		{"empty", "", ""},
		{"already slug", "already-a-slug", "already-a-slug"},
		{"long string", "this is a very long feature name that exceeds the fifty character limit and should be truncated", "this-is-a-very-long-feature-name-that-exceeds-the"},
		{"trailing dash after truncation", "this is a very long feature name that exceeds the-fifty character limit", "this-is-a-very-long-feature-name-that-exceeds-the"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, Slugify(tt.input))
		})
	}
}

func TestTierForIndex(t *testing.T) {
	tiers := [][]int{{0, 1}, {2, 3}, {4}}
	assert.Equal(t, 0, tierForIndex(0, tiers))
	assert.Equal(t, 0, tierForIndex(1, tiers))
	assert.Equal(t, 1, tierForIndex(2, tiers))
	assert.Equal(t, 2, tierForIndex(4, tiers))
}

func TestBuildLabels(t *testing.T) {
	tiers := [][]int{{0, 1}, {2}}

	// Tier 0 feature
	labels := buildLabels(agent.PlannedTask{Type: "feature"}, 0, tiers)
	assert.Contains(t, labels, issues.TypeFeature)
	assert.Contains(t, labels, issues.StatusReady)

	// Tier 0 bugfix
	labels = buildLabels(agent.PlannedTask{Type: "bugfix"}, 1, tiers)
	assert.Contains(t, labels, issues.TypeBugfix)
	assert.Contains(t, labels, issues.StatusReady)

	// Tier 1 (blocked)
	labels = buildLabels(agent.PlannedTask{Type: "feature"}, 2, tiers)
	assert.Contains(t, labels, issues.TypeFeature)
	assert.Contains(t, labels, issues.StatusBlocked)

	// Default type is feature
	labels = buildLabels(agent.PlannedTask{}, 0, tiers)
	assert.Contains(t, labels, issues.TypeFeature)
}

func TestCreateFromPlan(t *testing.T) {
	plan := &agent.Plan{
		BatchName: "Test Feature",
		Tasks: []agent.PlannedTask{
			{
				Title:              "Task A",
				Description:        "Do A",
				AcceptanceCriteria: []string{"A works"},
				Scope:              []string{"a.go"},
				Complexity:         "low",
				DependsOn:          []int{},
			},
			{
				Title:                   "Task B",
				Description:             "Do B",
				ImplementationDetails:   "Build B using A",
				AcceptanceCriteria:      []string{"B works"},
				Scope:                   []string{"b.go"},
				Conventions:             []string{"Use testify"},
				ContextFromDependencies: []string{"A produces a.go"},
				Complexity:              "medium",
				DependsOn:               []int{0},
			},
		},
	}

	mock := newMockPlatform()
	result, err := CreateFromPlan(context.Background(), mock, plan, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, result.MilestoneNumber)
	assert.Len(t, result.IssueNumbers, 2)
	assert.Equal(t, "herd/batch/1-test-feature", result.BatchBranch)
	assert.Len(t, result.Tiers, 2)

	// Verify milestone was created
	assert.Equal(t, "Test Feature", mock.milestones.created[0].title)

	// Verify issues were created with correct labels
	assert.Contains(t, mock.issues.created[0].labels, issues.StatusReady)
	assert.Contains(t, mock.issues.created[1].labels, issues.StatusBlocked)

	// Verify second issue was updated with real depends_on
	require.Len(t, mock.issues.updates, 1)
	assert.Contains(t, *mock.issues.updates[0].body, "depends_on")

	// Verify branch was created
	assert.Equal(t, "herd/batch/1-test-feature", mock.repo.branchesCreated[0].name)
}

func TestCreateFromPlan_CycleError(t *testing.T) {
	plan := &agent.Plan{
		BatchName: "Cyclic",
		Tasks: []agent.PlannedTask{
			{Title: "A", DependsOn: []int{1}},
			{Title: "B", DependsOn: []int{0}},
		},
	}

	mock := newMockPlatform()
	_, err := CreateFromPlan(context.Background(), mock, plan, nil)
	assert.ErrorContains(t, err, "cycle detected")
}

func TestBuildIssueBody(t *testing.T) {
	task := agent.PlannedTask{
		Title:                   "Create model",
		Description:             "Build the user model",
		ImplementationDetails:   "Use bcrypt for hashing",
		AcceptanceCriteria:      []string{"Model exists", "Tests pass"},
		Scope:                   []string{"model.go"},
		Conventions:             []string{"Use testify"},
		ContextFromDependencies: []string{"Auth package available"},
		Complexity:              "medium",
		DependsOn:               []int{0},
	}
	issueNumbers := []int{42, 0}

	body := buildIssueBody(task, 5, issueNumbers)
	assert.Contains(t, body, "batch: 5")
	assert.Contains(t, body, "depends_on: [42]")
	assert.Contains(t, body, "estimated_complexity: medium")
	assert.Contains(t, body, "## Task")
	assert.Contains(t, body, "Build the user model")
	assert.Contains(t, body, "## Implementation Details")
	assert.Contains(t, body, "Use bcrypt for hashing")
	assert.Contains(t, body, "## Conventions")
	assert.Contains(t, body, "- Use testify")
	assert.Contains(t, body, "## Context from Dependencies")
	assert.Contains(t, body, "- Auth package available")
	assert.Contains(t, body, "## Acceptance Criteria")
	assert.Contains(t, body, "- [ ] Model exists")
	assert.Contains(t, body, "## Files to Modify")
	assert.Contains(t, body, "- `model.go`")
}

func TestRenderBodyWithNewFields(t *testing.T) {
	body := issues.IssueBody{
		FrontMatter: issues.FrontMatter{Version: 1},
		Task:        "Do something",
		ImplementationDetails: "Step 1, Step 2",
		Conventions:           []string{"Pattern A", "Pattern B"},
		ContextFromDeps:       []string{"Dep 1 provides X"},
		Criteria:              []string{"It works"},
	}

	rendered := issues.RenderBody(body)
	assert.Contains(t, rendered, "## Implementation Details\n\nStep 1, Step 2")
	assert.Contains(t, rendered, "## Conventions\n\n- Pattern A\n- Pattern B")
	assert.Contains(t, rendered, "## Context from Dependencies\n\n- Dep 1 provides X")

	// Verify section ordering: Task < Implementation Details < Conventions < Context from Deps < Acceptance Criteria
	taskIdx := indexOf(rendered, "## Task")
	implIdx := indexOf(rendered, "## Implementation Details")
	convIdx := indexOf(rendered, "## Conventions")
	ctxIdx := indexOf(rendered, "## Context from Dependencies")
	critIdx := indexOf(rendered, "## Acceptance Criteria")
	assert.Less(t, taskIdx, implIdx)
	assert.Less(t, implIdx, convIdx)
	assert.Less(t, convIdx, ctxIdx)
	assert.Less(t, ctxIdx, critIdx)
}

func TestRenderBodyOmitsEmptyNewFields(t *testing.T) {
	body := issues.IssueBody{
		FrontMatter: issues.FrontMatter{Version: 1},
		Task:        "Simple task",
		Criteria:    []string{"Done"},
	}

	rendered := issues.RenderBody(body)
	assert.NotContains(t, rendered, "## Implementation Details")
	assert.NotContains(t, rendered, "## Conventions")
	assert.NotContains(t, rendered, "## Context from Dependencies")
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestBuildLabels_ManualTask(t *testing.T) {
	tiers := [][]int{{0}}
	labels := buildLabels(agent.PlannedTask{Manual: true}, 0, tiers)
	assert.Contains(t, labels, issues.TypeManual)
	assert.NotContains(t, labels, issues.TypeFeature)
	assert.Contains(t, labels, issues.StatusReady)
}

func TestCreateFromPlan_ManualTaskNotifyUsers(t *testing.T) {
	plan := &agent.Plan{
		BatchName: "Test",
		Tasks: []agent.PlannedTask{
			{Title: "Setup repo", Description: "Create the repo", Manual: true},
		},
	}

	mock := newMockPlatform()
	cfg := &config.Config{
		Monitor: config.Monitor{NotifyUsers: []string{"alice", "bob"}},
	}

	_, err := CreateFromPlan(context.Background(), mock, plan, cfg)
	require.NoError(t, err)

	// Should have created the issue with TypeManual
	assert.Contains(t, mock.issues.created[0].labels, issues.TypeManual)

	// Should have added a comment mentioning users
	require.Len(t, mock.issues.comments, 1)
	assert.Contains(t, mock.issues.comments[0], "@alice")
	assert.Contains(t, mock.issues.comments[0], "@bob")
}

func TestBuildMentions(t *testing.T) {
	assert.Equal(t, "@alice @bob", buildMentions([]string{"alice", "bob"}))
	assert.Equal(t, "@solo", buildMentions([]string{"solo"}))
}

// --- Mock Platform ---

type mockPlatform struct {
	issues     *mockIssueService
	milestones *mockMilestoneService
	repo       *mockRepoService
}

func newMockPlatform() *mockPlatform {
	return &mockPlatform{
		issues:     &mockIssueService{nextNumber: 100},
		milestones: &mockMilestoneService{nextNumber: 1},
		repo:       &mockRepoService{},
	}
}

func (m *mockPlatform) Issues() platform.IssueService             { return m.issues }
func (m *mockPlatform) PullRequests() platform.PullRequestService  { return nil }
func (m *mockPlatform) Workflows() platform.WorkflowService        { return nil }
func (m *mockPlatform) Labels() platform.LabelService              { return nil }
func (m *mockPlatform) Milestones() platform.MilestoneService      { return m.milestones }
func (m *mockPlatform) Runners() platform.RunnerService            { return nil }
func (m *mockPlatform) Repository() platform.RepositoryService     { return m.repo }
func (m *mockPlatform) Checks() platform.CheckService             { return nil }

// mockIssueService

type createdIssue struct {
	title     string
	body      string
	labels    []string
	milestone *int
}

type issueUpdate struct {
	number int
	body   *string
}

type mockIssueService struct {
	nextNumber int
	created    []createdIssue
	updates    []issueUpdate
	comments   []string
}

func (m *mockIssueService) Create(_ context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	num := m.nextNumber
	m.nextNumber++
	m.created = append(m.created, createdIssue{title, body, labels, milestone})
	return &platform.Issue{Number: num, Title: title, Body: body, Labels: labels}, nil
}

func (m *mockIssueService) Get(_ context.Context, _ int) (*platform.Issue, error) {
	return nil, nil
}

func (m *mockIssueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return nil, nil
}

func (m *mockIssueService) Update(_ context.Context, number int, changes platform.IssueUpdate) (*platform.Issue, error) {
	m.updates = append(m.updates, issueUpdate{number, changes.Body})
	return &platform.Issue{Number: number}, nil
}

func (m *mockIssueService) AddLabels(_ context.Context, _ int, _ []string) error    { return nil }
func (m *mockIssueService) RemoveLabels(_ context.Context, _ int, _ []string) error { return nil }
func (m *mockIssueService) AddComment(_ context.Context, _ int, body string) error {
	m.comments = append(m.comments, body)
	return nil
}
func (m *mockIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (m *mockIssueService) CreateReaction(_ context.Context, _ int64, _ string) error { return nil }

// mockMilestoneService

type createdMilestone struct {
	title       string
	description string
}

type mockMilestoneService struct {
	nextNumber int
	created    []createdMilestone
}

func (m *mockMilestoneService) Create(_ context.Context, title, description string, _ *time.Time) (*platform.Milestone, error) {
	num := m.nextNumber
	m.nextNumber++
	m.created = append(m.created, createdMilestone{title, description})
	return &platform.Milestone{Number: num, Title: title}, nil
}

func (m *mockMilestoneService) Get(_ context.Context, _ int) (*platform.Milestone, error) {
	return nil, nil
}

func (m *mockMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, nil
}

func (m *mockMilestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, nil
}

// mockRepoService

type createdBranch struct {
	name    string
	fromSHA string
}

type mockRepoService struct {
	branchesCreated []createdBranch
}

func (m *mockRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) {
	return &platform.RepoInfo{DefaultBranch: "main"}, nil
}

func (m *mockRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return "main", nil
}

func (m *mockRepoService) CreateBranch(_ context.Context, name, fromSHA string) error {
	m.branchesCreated = append(m.branchesCreated, createdBranch{name, fromSHA})
	return nil
}

func (m *mockRepoService) DeleteBranch(_ context.Context, _ string) error {
	return nil
}

func (m *mockRepoService) GetBranchSHA(_ context.Context, _ string) (string, error) {
	return "abc123", nil
}
