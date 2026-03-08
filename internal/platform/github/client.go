package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"golang.org/x/oauth2"
)

// Client implements platform.Platform for GitHub.
type Client struct {
	gh    *gh.Client
	owner string
	repo  string
}

// Compile-time check that Client implements platform.Platform.
var _ platform.Platform = (*Client)(nil)

// New creates a new GitHub client.
// Auth priority: GITHUB_TOKEN env var > gh CLI auth token.
func New(owner, repo string) (*Client, error) {
	token, err := resolveToken()
	if err != nil {
		return nil, err
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(context.Background(), ts)
	client := gh.NewClient(httpClient)

	return &Client{
		gh:    client,
		owner: owner,
		repo:  repo,
	}, nil
}

func resolveToken() (string, error) {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token, nil
	}

	// Fall back to gh CLI auth token
	token, err := ghAuthToken()
	if err != nil {
		return "", fmt.Errorf("no GitHub token found: set GITHUB_TOKEN, GH_TOKEN, or run 'gh auth login'")
	}
	return token, nil
}

func ghAuthToken() (string, error) {
	cmd := exec.Command("gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("gh auth token returned empty")
	}
	return token, nil
}

func (c *Client) Issues() platform.IssueService           { return &issueService{c} }
func (c *Client) PullRequests() platform.PullRequestService { return &pullRequestService{c} }
func (c *Client) Workflows() platform.WorkflowService       { return &workflowService{c} }
func (c *Client) Labels() platform.LabelService             { return &labelService{c} }
func (c *Client) Milestones() platform.MilestoneService     { return &milestoneService{c} }
func (c *Client) Runners() platform.RunnerService           { return &runnerService{c} }
func (c *Client) Repository() platform.RepositoryService   { return &repositoryService{c} }

// repositoryService implements platform.RepositoryService.
type repositoryService struct{ c *Client }

func (s *repositoryService) GetInfo(ctx context.Context) (*platform.RepoInfo, error) {
	repo, _, err := s.c.gh.Repositories.Get(ctx, s.c.owner, s.c.repo)
	if err != nil {
		return nil, fmt.Errorf("getting repo info: %w", err)
	}
	return &platform.RepoInfo{
		Owner:         s.c.owner,
		Name:          s.c.repo,
		DefaultBranch: repo.GetDefaultBranch(),
		Private:       repo.GetPrivate(),
		URL:           repo.GetHTMLURL(),
	}, nil
}

func (s *repositoryService) GetDefaultBranch(ctx context.Context) (string, error) {
	info, err := s.GetInfo(ctx)
	if err != nil {
		return "", err
	}
	return info.DefaultBranch, nil
}

func (s *repositoryService) CreateBranch(ctx context.Context, name, fromSHA string) error {
	ref := &gh.Reference{
		Ref:    gh.Ptr("refs/heads/" + name),
		Object: &gh.GitObject{SHA: gh.Ptr(fromSHA)},
	}
	_, _, err := s.c.gh.Git.CreateRef(ctx, s.c.owner, s.c.repo, ref)
	if err != nil {
		return fmt.Errorf("creating branch %s: %w", name, err)
	}
	return nil
}

func (s *repositoryService) DeleteBranch(ctx context.Context, name string) error {
	_, err := s.c.gh.Git.DeleteRef(ctx, s.c.owner, s.c.repo, "refs/heads/"+name)
	if err != nil {
		return fmt.Errorf("deleting branch %s: %w", name, err)
	}
	return nil
}

func (s *repositoryService) GetBranchSHA(ctx context.Context, name string) (string, error) {
	ref, _, err := s.c.gh.Git.GetRef(ctx, s.c.owner, s.c.repo, "refs/heads/"+name)
	if err != nil {
		return "", fmt.Errorf("getting branch SHA for %s: %w", name, err)
	}
	return ref.GetObject().GetSHA(), nil
}

// Stubs — keep these unexported types to avoid import cycle issues later.
// They exist only so that Client satisfies platform.Platform at compile time.
// Each will be filled in by subsequent issues.

var errNotImpl = fmt.Errorf("not implemented")

// issueService

type issueService struct{ c *Client }

func (s *issueService) Create(_ context.Context, _, _ string, _ []string, _ *int) (*platform.Issue, error) {
	return nil, errNotImpl
}
func (s *issueService) Get(_ context.Context, _ int) (*platform.Issue, error) { return nil, errNotImpl }
func (s *issueService) List(_ context.Context, _ platform.IssueFilters) ([]*platform.Issue, error) {
	return nil, errNotImpl
}
func (s *issueService) Update(_ context.Context, _ int, _ platform.IssueUpdate) (*platform.Issue, error) {
	return nil, errNotImpl
}
func (s *issueService) AddLabels(_ context.Context, _ int, _ []string) error    { return errNotImpl }
func (s *issueService) RemoveLabels(_ context.Context, _ int, _ []string) error { return errNotImpl }
func (s *issueService) AddComment(_ context.Context, _ int, _ string) error     { return errNotImpl }

// pullRequestService

type pullRequestService struct{ c *Client }

func (s *pullRequestService) Create(_ context.Context, _, _, _, _ string) (*platform.PullRequest, error) {
	return nil, errNotImpl
}
func (s *pullRequestService) Get(_ context.Context, _ int) (*platform.PullRequest, error) {
	return nil, errNotImpl
}
func (s *pullRequestService) List(_ context.Context, _ platform.PRFilters) ([]*platform.PullRequest, error) {
	return nil, errNotImpl
}
func (s *pullRequestService) Update(_ context.Context, _ int, _, _ *string) (*platform.PullRequest, error) {
	return nil, errNotImpl
}
func (s *pullRequestService) Merge(_ context.Context, _ int, _ platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, errNotImpl
}
func (s *pullRequestService) UpdateBranch(_ context.Context, _ int) error { return errNotImpl }
func (s *pullRequestService) CreateReview(_ context.Context, _ int, _ string, _ platform.ReviewEvent) error {
	return errNotImpl
}

// workflowService

type workflowService struct{ c *Client }

func (s *workflowService) GetWorkflow(_ context.Context, _ string) (int64, error) {
	return 0, errNotImpl
}
func (s *workflowService) Dispatch(_ context.Context, _, _ string, _ map[string]string) (*platform.Run, error) {
	return nil, errNotImpl
}
func (s *workflowService) GetRun(_ context.Context, _ int64) (*platform.Run, error) {
	return nil, errNotImpl
}
func (s *workflowService) ListRuns(_ context.Context, _ platform.RunFilters) ([]*platform.Run, error) {
	return nil, errNotImpl
}
func (s *workflowService) CancelRun(_ context.Context, _ int64) error { return errNotImpl }

// labelService

type labelService struct{ c *Client }

func (s *labelService) Create(_ context.Context, _, _, _ string) error { return errNotImpl }
func (s *labelService) List(_ context.Context) ([]*platform.Label, error) {
	return nil, errNotImpl
}
func (s *labelService) Delete(_ context.Context, _ string) error { return errNotImpl }

// milestoneService

type milestoneService struct{ c *Client }

func (s *milestoneService) Create(_ context.Context, _, _ string, _ *time.Time) (*platform.Milestone, error) {
	return nil, errNotImpl
}
func (s *milestoneService) Get(_ context.Context, _ int) (*platform.Milestone, error) {
	return nil, errNotImpl
}
func (s *milestoneService) List(_ context.Context) ([]*platform.Milestone, error) {
	return nil, errNotImpl
}
func (s *milestoneService) Update(_ context.Context, _ int, _ platform.MilestoneUpdate) (*platform.Milestone, error) {
	return nil, errNotImpl
}

// runnerService

type runnerService struct{ c *Client }

func (s *runnerService) List(_ context.Context) ([]*platform.Runner, error) { return nil, errNotImpl }
func (s *runnerService) Get(_ context.Context, _ int64) (*platform.Runner, error) {
	return nil, errNotImpl
}
