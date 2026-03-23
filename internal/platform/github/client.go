package github

import (
	"context"
	"fmt"
	"net/http"
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
	gh         *gh.Client
	owner      string
	repo       string
	httpClient *http.Client
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
	httpClient.Transport = newRetryTransport(httpClient.Transport, time.Second)
	client := gh.NewClient(httpClient)

	return &Client{
		gh:         client,
		owner:      owner,
		repo:       repo,
		httpClient: httpClient,
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

// HTTPClient returns the authenticated HTTP client used by this GitHub client.
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

func (c *Client) Issues() platform.IssueService           { return &issueService{c} }
func (c *Client) PullRequests() platform.PullRequestService { return &pullRequestService{c} }
func (c *Client) Workflows() platform.WorkflowService       { return &workflowService{c} }
func (c *Client) Labels() platform.LabelService             { return &labelService{c} }
func (c *Client) Milestones() platform.MilestoneService     { return &milestoneService{c} }
func (c *Client) Runners() platform.RunnerService           { return &runnerService{c} }
func (c *Client) Repository() platform.RepositoryService   { return &repositoryService{c} }
func (c *Client) Checks() platform.CheckService             { return &checkService{c} }

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

