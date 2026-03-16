package orchestration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/monitor"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Stateful mock platform ---

type statefulPlatform struct {
	issues     *statefulIssueService
	prs        *statefulPRService
	workflows  *statefulWorkflowService
	repo       *statefulRepoService
	milestones *statefulMilestoneService
}

func (p *statefulPlatform) Issues() platform.IssueService            { return p.issues }
func (p *statefulPlatform) PullRequests() platform.PullRequestService { return p.prs }
func (p *statefulPlatform) Workflows() platform.WorkflowService      { return p.workflows }
func (p *statefulPlatform) Labels() platform.LabelService             { return nil }
func (p *statefulPlatform) Milestones() platform.MilestoneService     { return p.milestones }
func (p *statefulPlatform) Runners() platform.RunnerService           { return nil }
func (p *statefulPlatform) Repository() platform.RepositoryService    { return p.repo }
func (p *statefulPlatform) Checks() platform.CheckService            { return nil }

// --- Stateful Issue Service ---

type statefulIssueService struct {
	issues     map[int]*platform.Issue
	nextNumber int
	comments   map[int][]string
}

func newStatefulIssueService(initial ...*platform.Issue) *statefulIssueService {
	s := &statefulIssueService{
		issues:     make(map[int]*platform.Issue),
		nextNumber: 200,
		comments:   make(map[int][]string),
	}
	for _, iss := range initial {
		s.issues[iss.Number] = iss
	}
	return s
}

func (s *statefulIssueService) Create(_ context.Context, title, body string, labels []string, milestone *int) (*platform.Issue, error) {
	iss := &platform.Issue{
		Number: s.nextNumber,
		Title:  title,
		Body:   body,
		Labels: labels,
		State:  "open",
	}
	if milestone != nil {
		iss.Milestone = &platform.Milestone{Number: *milestone}
	}
	s.issues[iss.Number] = iss
	s.nextNumber++
	return iss, nil
}

func (s *statefulIssueService) Get(_ context.Context, number int) (*platform.Issue, error) {
	if iss, ok := s.issues[number]; ok {
		return iss, nil
	}
	return nil, fmt.Errorf("issue #%d not found", number)
}

func (s *statefulIssueService) List(_ context.Context, f platform.IssueFilters) ([]*platform.Issue, error) {
	var result []*platform.Issue
	for _, iss := range s.issues {
		if f.Milestone != nil && (iss.Milestone == nil || iss.Milestone.Number != *f.Milestone) {
			continue
		}
		if f.State != "" && f.State != "all" && iss.State != f.State {
			continue
		}
		if len(f.Labels) > 0 {
			found := false
			for _, fl := range f.Labels {
				for _, il := range iss.Labels {
					if fl == il {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				continue
			}
		}
		result = append(result, iss)
	}
	return result, nil
}

func (s *statefulIssueService) Update(_ context.Context, number int, changes platform.IssueUpdate) (*platform.Issue, error) {
	iss, ok := s.issues[number]
	if !ok {
		return nil, fmt.Errorf("issue #%d not found", number)
	}
	if changes.State != nil {
		iss.State = *changes.State
	}
	return iss, nil
}

func (s *statefulIssueService) AddLabels(_ context.Context, number int, labels []string) error {
	if iss, ok := s.issues[number]; ok {
		iss.Labels = append(iss.Labels, labels...)
	}
	return nil
}

func (s *statefulIssueService) RemoveLabels(_ context.Context, number int, labels []string) error {
	iss, ok := s.issues[number]
	if !ok {
		return nil
	}
	removeSet := make(map[string]bool)
	for _, l := range labels {
		removeSet[l] = true
	}
	filtered := make([]string, 0)
	for _, l := range iss.Labels {
		if !removeSet[l] {
			filtered = append(filtered, l)
		}
	}
	iss.Labels = filtered
	return nil
}

func (s *statefulIssueService) AddComment(_ context.Context, number int, body string) error {
	s.comments[number] = append(s.comments[number], body)
	return nil
}
func (s *statefulIssueService) ListComments(_ context.Context, _ int) ([]*platform.Comment, error) {
	return nil, nil
}
func (s *statefulIssueService) CreateCommentReaction(_ context.Context, _ int64, _ string) error {
	return nil
}

func (s *statefulIssueService) hasLabel(number int, label string) bool {
	iss, ok := s.issues[number]
	if !ok {
		return false
	}
	for _, l := range iss.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// --- Stateful PR Service ---

type statefulPRService struct {
	prs    map[int]*platform.PullRequest
	nextPR int
	merged bool
}

func newStatefulPRService() *statefulPRService {
	return &statefulPRService{prs: make(map[int]*platform.PullRequest), nextPR: 500}
}

func (s *statefulPRService) Create(_ context.Context, title, body, head, base string) (*platform.PullRequest, error) {
	pr := &platform.PullRequest{Number: s.nextPR, Title: title, Body: body, Head: head, Base: base, State: "open"}
	s.prs[pr.Number] = pr
	s.nextPR++
	return pr, nil
}
func (s *statefulPRService) Get(_ context.Context, number int) (*platform.PullRequest, error) {
	if pr, ok := s.prs[number]; ok {
		return pr, nil
	}
	return nil, fmt.Errorf("PR #%d not found", number)
}
func (s *statefulPRService) List(_ context.Context, f platform.PRFilters) ([]*platform.PullRequest, error) {
	var result []*platform.PullRequest
	for _, pr := range s.prs {
		if f.State != "" && pr.State != f.State {
			continue
		}
		if f.Head != "" && pr.Head != f.Head {
			continue
		}
		result = append(result, pr)
	}
	return result, nil
}
func (s *statefulPRService) Update(_ context.Context, number int, title, body *string) (*platform.PullRequest, error) {
	pr, ok := s.prs[number]
	if !ok {
		return nil, fmt.Errorf("PR #%d not found", number)
	}
	if title != nil {
		pr.Title = *title
	}
	if body != nil {
		pr.Body = *body
	}
	return pr, nil
}
func (s *statefulPRService) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	s.merged = true
	return &platform.MergeResult{Merged: true, SHA: "abc123"}, nil
}
func (s *statefulPRService) UpdateBranch(_ context.Context, _ int) error { return nil }
func (s *statefulPRService) AddComment(_ context.Context, _ int, _ string) error {
	return nil
}
func (s *statefulPRService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return nil
}

// --- Stateful Workflow Service ---

type statefulWorkflowService struct {
	runs       map[int64]*platform.Run
	dispatched []map[string]string
	cancelled  []int64
	nextRunID  int64
}

func newStatefulWorkflowService() *statefulWorkflowService {
	return &statefulWorkflowService{runs: make(map[int64]*platform.Run), nextRunID: 1000}
}

func (s *statefulWorkflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 1, nil
}
func (s *statefulWorkflowService) Dispatch(_ context.Context, _, _ string, inputs map[string]string) (*platform.Run, error) {
	s.dispatched = append(s.dispatched, inputs)
	return nil, nil
}
func (s *statefulWorkflowService) GetRun(_ context.Context, id int64) (*platform.Run, error) {
	if r, ok := s.runs[id]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("run %d not found", id)
}
func (s *statefulWorkflowService) ListRuns(_ context.Context, f platform.RunFilters) ([]*platform.Run, error) {
	var result []*platform.Run
	for _, r := range s.runs {
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}
func (s *statefulWorkflowService) CancelRun(_ context.Context, id int64) error {
	s.cancelled = append(s.cancelled, id)
	return nil
}

func (s *statefulWorkflowService) addRun(issueNumber int, conclusion string) int64 {
	id := s.nextRunID
	s.nextRunID++
	status := "completed"
	if conclusion == "" {
		status = "in_progress"
	}
	s.runs[id] = &platform.Run{
		ID:         id,
		Status:     status,
		Conclusion: conclusion,
		Inputs:     map[string]string{"issue_number": fmt.Sprintf("%d", issueNumber)},
		CreatedAt:  time.Now(),
	}
	return id
}

// --- Stateful Repo Service ---

type statefulRepoService struct {
	defaultBranch  string
	branches       map[string]string // name → SHA
	deletedBranch  string
}

func newStatefulRepoService() *statefulRepoService {
	return &statefulRepoService{
		defaultBranch: "main",
		branches:      make(map[string]string),
	}
}

func (s *statefulRepoService) GetInfo(_ context.Context) (*platform.RepoInfo, error) { return nil, nil }
func (s *statefulRepoService) GetDefaultBranch(_ context.Context) (string, error) {
	return s.defaultBranch, nil
}
func (s *statefulRepoService) CreateBranch(_ context.Context, name, sha string) error {
	s.branches[name] = sha
	return nil
}
func (s *statefulRepoService) DeleteBranch(_ context.Context, name string) error {
	s.deletedBranch = name
	delete(s.branches, name)
	return nil
}
func (s *statefulRepoService) GetBranchSHA(_ context.Context, name string) (string, error) {
	if sha, ok := s.branches[name]; ok {
		return sha, nil
	}
	return "abc123", nil // default — branch exists
}

// --- Stateful Milestone Service ---

type statefulMilestoneService struct {
	milestones map[int]*platform.Milestone
	closed     []int
}

func newStatefulMilestoneService(ms ...*platform.Milestone) *statefulMilestoneService {
	s := &statefulMilestoneService{milestones: make(map[int]*platform.Milestone)}
	for _, m := range ms {
		s.milestones[m.Number] = m
	}
	return s
}

func (s *statefulMilestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, nil
}
func (s *statefulMilestoneService) Get(_ context.Context, number int) (*platform.Milestone, error) {
	if ms, ok := s.milestones[number]; ok {
		return ms, nil
	}
	return nil, fmt.Errorf("milestone #%d not found", number)
}
func (s *statefulMilestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	var result []*platform.Milestone
	for _, ms := range s.milestones {
		result = append(result, ms)
	}
	return result, nil
}
func (s *statefulMilestoneService) Update(_ context.Context, number int, changes platform.MilestoneUpdate) (*platform.Milestone, error) {
	if changes.State != nil && *changes.State == "closed" {
		s.closed = append(s.closed, number)
	}
	return nil, nil
}

// --- Test Helpers ---

func initOrchestrationRepo(t *testing.T) (string, *git.Git) {
	t.Helper()
	bareDir := t.TempDir()
	runCmd(t, "", "git", "init", "--bare", "-b", "main", bareDir)

	dir := t.TempDir()
	runCmd(t, "", "git", "clone", bareDir, dir)
	runCmd(t, dir, "git", "config", "user.email", "test@test.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", "init")
	runCmd(t, dir, "git", "push", "origin", "main")

	return dir, git.New(dir)
}

func runCmd(t *testing.T, dir string, name string, args ...string) { //nolint:unparam // always "git" but kept for readability at call sites
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s %v failed: %s", name, args, string(out))
}

func simulateWorkerOutput(t *testing.T, dir string, g *git.Git, batchBranch string, issueNumber int, issueTitle, filename, content string) {
	t.Helper()
	workerBranch := fmt.Sprintf("herd/worker/%d-%s", issueNumber, planner.Slugify(issueTitle))
	require.NoError(t, g.Checkout(batchBranch))
	require.NoError(t, g.CreateBranch(workerBranch, batchBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644))
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", fmt.Sprintf("Complete #%d", issueNumber))
	require.NoError(t, g.Push("origin", workerBranch))
	require.NoError(t, g.Checkout(batchBranch))
}

// --- Tests ---

func TestOrchestration_FullLoop(t *testing.T) {
	dir, g := initOrchestrationRepo(t)
	batchBranch := "herd/batch/1-test-batch"

	// Create batch branch
	runCmd(t, dir, "git", "checkout", "-b", batchBranch)
	runCmd(t, dir, "git", "push", "origin", batchBranch)

	ms := &platform.Milestone{Number: 1, Title: "Test Batch", State: "open"}

	// 3 issues in 2 tiers: #10 (tier 0), #11 and #12 depend on #10 (tier 1)
	issueSvc := newStatefulIssueService(
		&platform.Issue{Number: 10, Title: "Task A", State: "open",
			Labels:    []string{issues.StatusDone},
			Milestone: ms,
			Body:      "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		&platform.Issue{Number: 11, Title: "Task B", State: "open",
			Labels:    []string{issues.StatusBlocked},
			Milestone: ms,
			Body:      "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo B\n"},
		&platform.Issue{Number: 12, Title: "Task C", State: "open",
			Labels:    []string{issues.StatusBlocked},
			Milestone: ms,
			Body:      "---\nherd:\n  version: 1\n  batch: 1\n  depends_on: [10]\n---\n\n## Task\nDo C\n"},
	)

	wf := newStatefulWorkflowService()
	prSvc := newStatefulPRService()
	repoSvc := newStatefulRepoService()
	msSvc := newStatefulMilestoneService(ms)

	mock := &statefulPlatform{
		issues:     issueSvc,
		prs:        prSvc,
		workflows:  wf,
		repo:       repoSvc,
		milestones: msSvc,
	}

	cfg := &config.Config{
		Workers:      config.Workers{MaxConcurrent: 3, TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
		Integrator:   config.Integrator{Review: true, Strategy: "squash", ReviewMaxFixCycles: 3},
		PullRequests: config.PullRequests{AutoMerge: true},
	}

	ctx := context.Background()

	// Step 1: Simulate worker #10 output
	simulateWorkerOutput(t, dir, g, batchBranch, 10, "Task A", "task-10.txt", "done by worker 10")

	// Step 2: Consolidate worker #10
	runID10 := wf.addRun(10, "success")
	consResult, err := integrator.Consolidate(ctx, mock, g, cfg, integrator.ConsolidateParams{RunID: runID10})
	require.NoError(t, err)
	assert.True(t, consResult.Merged)

	// Step 3: Advance — tier 0 complete, should dispatch tier 1
	advResult, err := integrator.Advance(ctx, mock, g, cfg, integrator.AdvanceParams{RunID: runID10})
	require.NoError(t, err)
	assert.True(t, advResult.TierComplete)
	assert.Equal(t, 2, advResult.DispatchedCount)
	// Verify label transitions: #11 and #12 should be in-progress
	assert.True(t, issueSvc.hasLabel(11, issues.StatusInProgress))
	assert.True(t, issueSvc.hasLabel(12, issues.StatusInProgress))

	// Step 4: Simulate worker #11 and #12 output
	simulateWorkerOutput(t, dir, g, batchBranch, 11, "Task B", "task-11.txt", "done by worker 11")
	issueSvc.issues[11].Labels = []string{issues.StatusDone}

	simulateWorkerOutput(t, dir, g, batchBranch, 12, "Task C", "task-12.txt", "done by worker 12")
	issueSvc.issues[12].Labels = []string{issues.StatusDone}

	// Step 5: Consolidate #11 and #12
	runID11 := wf.addRun(11, "success")
	_, err = integrator.Consolidate(ctx, mock, g, cfg, integrator.ConsolidateParams{RunID: runID11})
	require.NoError(t, err)

	runID12 := wf.addRun(12, "success")
	_, err = integrator.Consolidate(ctx, mock, g, cfg, integrator.ConsolidateParams{RunID: runID12})
	require.NoError(t, err)

	// Step 6: Advance — all tiers complete, should open batch PR
	advResult, err = integrator.Advance(ctx, mock, g, cfg, integrator.AdvanceParams{RunID: runID12})
	require.NoError(t, err)
	assert.True(t, advResult.AllComplete)
	assert.NotZero(t, advResult.BatchPRNumber)

	// Step 7: Review — should approve and auto-merge
	reviewResult, err := integrator.Review(ctx, mock, &mockAgent{approved: true}, g, cfg, integrator.ReviewParams{
		RunID:    runID12,
		RepoRoot: dir,
	})
	require.NoError(t, err)
	assert.True(t, reviewResult.Approved)
	assert.True(t, prSvc.merged)

	// Verify cleanup: milestone closed
	assert.Contains(t, msSvc.closed, 1)
}

func TestOrchestration_WorkerFailure_MonitorRedispatch(t *testing.T) {
	ms := &platform.Milestone{Number: 1, Title: "Test", State: "open"}

	issueSvc := newStatefulIssueService(
		&platform.Issue{Number: 10, Title: "Task A", State: "open",
			Labels:    []string{issues.StatusInProgress},
			Milestone: ms,
		},
	)

	wf := newStatefulWorkflowService()
	// Add a completed failed run for the issue
	failedRunID := wf.addRun(10, "failure")
	_ = failedRunID

	mock := &statefulPlatform{
		issues:     issueSvc,
		prs:        newStatefulPRService(),
		workflows:  wf,
		repo:       newStatefulRepoService(),
		milestones: newStatefulMilestoneService(ms),
	}

	ctx := context.Background()

	// Simulate monitor detecting the failed issue
	// First, label it as failed (as worker would do on failure)
	_ = issueSvc.RemoveLabels(ctx, 10, []string{issues.StatusInProgress})
	_ = issueSvc.AddLabels(ctx, 10, []string{issues.StatusFailed})

	cfg := &config.Config{
		Monitor: config.Monitor{
			AutoRedispatch:        true,
			MaxRedispatchAttempts: 3,
		},
		Workers: config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	result, err := monitor.Patrol(ctx, mock, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FailedIssues)
	assert.Equal(t, 1, result.RedispatchedCount)

	// Verify label transition: failed → in-progress
	assert.True(t, issueSvc.hasLabel(10, issues.StatusInProgress))
	assert.False(t, issueSvc.hasLabel(10, issues.StatusFailed))
}

func TestOrchestration_ConflictResolution(t *testing.T) {
	dir, g := initOrchestrationRepo(t)
	batchBranch := "herd/batch/1-test-batch"

	// Create batch branch with a shared file
	runCmd(t, dir, "git", "checkout", "-b", batchBranch)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("original"), 0644))
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", "add shared file")
	runCmd(t, dir, "git", "push", "origin", batchBranch)

	ms := &platform.Milestone{Number: 1, Title: "Test Batch", State: "open"}

	issueSvc := newStatefulIssueService(
		&platform.Issue{Number: 10, Title: "Task A", State: "open",
			Labels:    []string{issues.StatusDone},
			Milestone: ms,
			Body:      "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo A\n"},
		&platform.Issue{Number: 11, Title: "Task B", State: "open",
			Labels:    []string{issues.StatusDone},
			Milestone: ms,
			Body:      "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo B\n"},
	)

	wf := newStatefulWorkflowService()
	repoSvc := newStatefulRepoService()

	mock := &statefulPlatform{
		issues:     issueSvc,
		prs:        newStatefulPRService(),
		workflows:  wf,
		repo:       repoSvc,
		milestones: newStatefulMilestoneService(ms),
	}

	cfg := &config.Config{
		Integrator: config.Integrator{OnConflict: "dispatch-resolver", MaxConflictResolutionAttempts: 3},
		Workers:    config.Workers{TimeoutMinutes: 30, RunnerLabel: "herd-worker"},
	}

	ctx := context.Background()

	// Worker 10: modify shared.txt
	require.NoError(t, g.Checkout(batchBranch))
	require.NoError(t, g.CreateBranch("herd/worker/10-task-a", batchBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("from worker 10"), 0644))
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", "Complete #10")
	require.NoError(t, g.Push("origin", "herd/worker/10-task-a"))

	// Worker 11: modify same file differently (from batch, not worker 10)
	require.NoError(t, g.Checkout(batchBranch))
	require.NoError(t, g.CreateBranch("herd/worker/11-task-b", batchBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("from worker 11"), 0644))
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", "Complete #11")
	require.NoError(t, g.Push("origin", "herd/worker/11-task-b"))

	// Consolidate #10 — should succeed (first merge is clean)
	runID10 := wf.addRun(10, "success")
	result10, err := integrator.Consolidate(ctx, mock, g, cfg, integrator.ConsolidateParams{RunID: runID10})
	require.NoError(t, err)
	assert.True(t, result10.Merged)
	assert.False(t, result10.ConflictDetected)

	// Consolidate #11 — should detect conflict
	runID11 := wf.addRun(11, "success")
	result11, err := integrator.Consolidate(ctx, mock, g, cfg, integrator.ConsolidateParams{RunID: runID11})
	require.NoError(t, err) // dispatch-resolver doesn't return error
	assert.True(t, result11.ConflictDetected)
	assert.NotZero(t, result11.ConflictIssue)

	// Verify a conflict-resolution issue was created
	resolverIssue, ok := issueSvc.issues[result11.ConflictIssue]
	assert.True(t, ok)
	assert.Contains(t, resolverIssue.Title, "Resolve conflict")

	// Verify a worker was dispatched for the resolver
	assert.Len(t, wf.dispatched, 1)
}

// mockAgent implements agent.Agent for testing
type mockAgent struct {
	approved bool
}

func (a *mockAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}
func (a *mockAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}
func (a *mockAgent) Review(_ context.Context, _ string, _ agent.ReviewOptions) (*agent.ReviewResult, error) {
	return &agent.ReviewResult{Approved: a.approved, Summary: "LGTM"}, nil
}
