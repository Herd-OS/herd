package github

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	EventInstallation             = "installation"
	EventInstallationRepositories = "installation_repositories"
	EventIssueComment             = "issue_comment"
	EventPullRequest              = "pull_request"
	EventPullRequestReview        = "pull_request_review"
	EventWorkflowRun              = "workflow_run"
)

// Event is the normalized shape accepted by the webhook ingester.
type Event interface {
	EventName() string
	EventAction() string
}

type InstallationEvent struct {
	Action                string
	InstallationID        int64
	AccountLogin          string
	AccountID             int64
	AccountType           string
	TargetType            string
	RepositorySelection   string
	Permissions           json.RawMessage
	Events                []string
	Repositories          []Repository
	InstallationCreatedAt string
	InstallationUpdatedAt string
}

func (e InstallationEvent) EventName() string   { return EventInstallation }
func (e InstallationEvent) EventAction() string { return e.Action }

type InstallationRepositoriesEvent struct {
	Action              string
	InstallationID      int64
	AccountLogin        string
	AccountID           int64
	AccountType         string
	RepositorySelection string
	RepositoriesAdded   []Repository
	RepositoriesRemoved []Repository
}

func (e InstallationRepositoriesEvent) EventName() string {
	return EventInstallationRepositories
}
func (e InstallationRepositoriesEvent) EventAction() string { return e.Action }

type IssueCommentEvent struct {
	Action         string
	InstallationID int64
	Repository     Repository
	IssueNumber    int
	PullRequestURL string
	CommentID      int64
	CommentBody    string
	SenderLogin    string
}

func (e IssueCommentEvent) EventName() string   { return EventIssueComment }
func (e IssueCommentEvent) EventAction() string { return e.Action }

type PullRequestEvent struct {
	Action         string
	InstallationID int64
	Repository     Repository
	Number         int
	HeadSHA        string
	BaseSHA        string
	SenderLogin    string
}

func (e PullRequestEvent) EventName() string   { return EventPullRequest }
func (e PullRequestEvent) EventAction() string { return e.Action }

type PullRequestReviewEvent struct {
	Action         string
	InstallationID int64
	Repository     Repository
	PullRequestNum int
	ReviewID       int64
	State          string
	SenderLogin    string
}

func (e PullRequestReviewEvent) EventName() string   { return EventPullRequestReview }
func (e PullRequestReviewEvent) EventAction() string { return e.Action }

type WorkflowRunEvent struct {
	Action         string
	InstallationID int64
	Repository     Repository
	WorkflowRunID  int64
	HeadSHA        string
	Status         string
	Conclusion     string
	SenderLogin    string
}

func (e WorkflowRunEvent) EventName() string   { return EventWorkflowRun }
func (e WorkflowRunEvent) EventAction() string { return e.Action }

type Repository struct {
	ID            int64
	Owner         string
	Name          string
	FullName      string
	DefaultBranch string
	Private       bool
}

func ParseEvent(eventName string, payload []byte) (Event, error) {
	switch eventName {
	case EventInstallation:
		event, err := parseInstallationEvent(payload)
		if err != nil {
			return nil, err
		}
		return event, nil
	case EventInstallationRepositories:
		event, err := parseInstallationRepositoriesEvent(payload)
		if err != nil {
			return nil, err
		}
		return event, nil
	case EventIssueComment:
		event, err := parseIssueCommentEvent(payload)
		if err != nil {
			return nil, err
		}
		return event, nil
	case EventPullRequest:
		event, err := parsePullRequestEvent(payload)
		if err != nil {
			return nil, err
		}
		return event, nil
	case EventPullRequestReview:
		event, err := parsePullRequestReviewEvent(payload)
		if err != nil {
			return nil, err
		}
		return event, nil
	case EventWorkflowRun:
		event, err := parseWorkflowRunEvent(payload)
		if err != nil {
			return nil, err
		}
		return event, nil
	default:
		return nil, nil
	}
}

func PayloadAction(payload []byte) string {
	var envelope struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	return envelope.Action
}

type installationPayload struct {
	Action                string       `json:"action"`
	Installation          installation `json:"installation"`
	Repositories          []repository `json:"repositories"`
	RepositorySelection   string       `json:"repository_selection"`
	InstallationCreatedAt string       `json:"created_at"`
	InstallationUpdatedAt string       `json:"updated_at"`
}

type installationRepositoriesPayload struct {
	Action              string       `json:"action"`
	Installation        installation `json:"installation"`
	RepositorySelection string       `json:"repository_selection"`
	RepositoriesAdded   []repository `json:"repositories_added"`
	RepositoriesRemoved []repository `json:"repositories_removed"`
}

type installation struct {
	ID                  int64           `json:"id"`
	Account             account         `json:"account"`
	TargetType          string          `json:"target_type"`
	RepositorySelection string          `json:"repository_selection"`
	Permissions         json.RawMessage `json:"permissions"`
	Events              []string        `json:"events"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

type account struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

type repository struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	FullName      string  `json:"full_name"`
	Owner         account `json:"owner"`
	DefaultBranch string  `json:"default_branch"`
	Private       bool    `json:"private"`
}

func parseInstallationEvent(payload []byte) (InstallationEvent, error) {
	var raw installationPayload
	if err := json.Unmarshal(payload, &raw); err != nil {
		return InstallationEvent{}, fmt.Errorf("parse installation event: %w", err)
	}
	selection := firstNonEmpty(raw.RepositorySelection, raw.Installation.RepositorySelection)
	return InstallationEvent{
		Action:                raw.Action,
		InstallationID:        raw.Installation.ID,
		AccountLogin:          raw.Installation.Account.Login,
		AccountID:             raw.Installation.Account.ID,
		AccountType:           raw.Installation.Account.Type,
		TargetType:            raw.Installation.TargetType,
		RepositorySelection:   selection,
		Permissions:           raw.Installation.Permissions,
		Events:                raw.Installation.Events,
		Repositories:          normalizeRepositories(raw.Repositories),
		InstallationCreatedAt: firstNonEmpty(raw.InstallationCreatedAt, raw.Installation.CreatedAt),
		InstallationUpdatedAt: firstNonEmpty(raw.InstallationUpdatedAt, raw.Installation.UpdatedAt),
	}, nil
}

func parseInstallationRepositoriesEvent(payload []byte) (InstallationRepositoriesEvent, error) {
	var raw installationRepositoriesPayload
	if err := json.Unmarshal(payload, &raw); err != nil {
		return InstallationRepositoriesEvent{}, fmt.Errorf("parse installation_repositories event: %w", err)
	}
	return InstallationRepositoriesEvent{
		Action:              raw.Action,
		InstallationID:      raw.Installation.ID,
		AccountLogin:        raw.Installation.Account.Login,
		AccountID:           raw.Installation.Account.ID,
		AccountType:         raw.Installation.Account.Type,
		RepositorySelection: firstNonEmpty(raw.RepositorySelection, raw.Installation.RepositorySelection),
		RepositoriesAdded:   normalizeRepositories(raw.RepositoriesAdded),
		RepositoriesRemoved: normalizeRepositories(raw.RepositoriesRemoved),
	}, nil
}

func parseIssueCommentEvent(payload []byte) (IssueCommentEvent, error) {
	var raw struct {
		Action       string       `json:"action"`
		Installation installation `json:"installation"`
		Repository   repository   `json:"repository"`
		Issue        struct {
			Number      int `json:"number"`
			PullRequest struct {
				URL string `json:"url"`
			} `json:"pull_request"`
		} `json:"issue"`
		Comment struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		} `json:"comment"`
		Sender account `json:"sender"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return IssueCommentEvent{}, fmt.Errorf("parse issue_comment event: %w", err)
	}
	return IssueCommentEvent{
		Action:         raw.Action,
		InstallationID: raw.Installation.ID,
		Repository:     normalizeRepository(raw.Repository),
		IssueNumber:    raw.Issue.Number,
		PullRequestURL: raw.Issue.PullRequest.URL,
		CommentID:      raw.Comment.ID,
		CommentBody:    raw.Comment.Body,
		SenderLogin:    raw.Sender.Login,
	}, nil
}

func parsePullRequestEvent(payload []byte) (PullRequestEvent, error) {
	var raw struct {
		Action       string       `json:"action"`
		Installation installation `json:"installation"`
		Repository   repository   `json:"repository"`
		PullRequest  struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
		} `json:"pull_request"`
		Number int     `json:"number"`
		Sender account `json:"sender"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return PullRequestEvent{}, fmt.Errorf("parse pull_request event: %w", err)
	}
	return PullRequestEvent{
		Action:         raw.Action,
		InstallationID: raw.Installation.ID,
		Repository:     normalizeRepository(raw.Repository),
		Number:         firstNonZero(raw.PullRequest.Number, raw.Number),
		HeadSHA:        raw.PullRequest.Head.SHA,
		BaseSHA:        raw.PullRequest.Base.SHA,
		SenderLogin:    raw.Sender.Login,
	}, nil
}

func parsePullRequestReviewEvent(payload []byte) (PullRequestReviewEvent, error) {
	var raw struct {
		Action       string       `json:"action"`
		Installation installation `json:"installation"`
		Repository   repository   `json:"repository"`
		PullRequest  struct {
			Number int `json:"number"`
		} `json:"pull_request"`
		Review struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		} `json:"review"`
		Sender account `json:"sender"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return PullRequestReviewEvent{}, fmt.Errorf("parse pull_request_review event: %w", err)
	}
	return PullRequestReviewEvent{
		Action:         raw.Action,
		InstallationID: raw.Installation.ID,
		Repository:     normalizeRepository(raw.Repository),
		PullRequestNum: raw.PullRequest.Number,
		ReviewID:       raw.Review.ID,
		State:          raw.Review.State,
		SenderLogin:    raw.Sender.Login,
	}, nil
}

func parseWorkflowRunEvent(payload []byte) (WorkflowRunEvent, error) {
	var raw struct {
		Action       string       `json:"action"`
		Installation installation `json:"installation"`
		Repository   repository   `json:"repository"`
		WorkflowRun  struct {
			ID         int64  `json:"id"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_run"`
		Sender account `json:"sender"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WorkflowRunEvent{}, fmt.Errorf("parse workflow_run event: %w", err)
	}
	return WorkflowRunEvent{
		Action:         raw.Action,
		InstallationID: raw.Installation.ID,
		Repository:     normalizeRepository(raw.Repository),
		WorkflowRunID:  raw.WorkflowRun.ID,
		HeadSHA:        raw.WorkflowRun.HeadSHA,
		Status:         raw.WorkflowRun.Status,
		Conclusion:     raw.WorkflowRun.Conclusion,
		SenderLogin:    raw.Sender.Login,
	}, nil
}

func normalizeRepositories(repos []repository) []Repository {
	normalized := make([]Repository, 0, len(repos))
	for _, repo := range repos {
		normalized = append(normalized, normalizeRepository(repo))
	}
	return normalized
}

func normalizeRepository(repo repository) Repository {
	owner := repo.Owner.Login
	name := repo.Name
	if owner == "" || name == "" {
		parts := strings.SplitN(repo.FullName, "/", 2)
		if len(parts) == 2 {
			if owner == "" {
				owner = parts[0]
			}
			if name == "" {
				name = parts[1]
			}
		}
	}
	return Repository{
		ID:            repo.ID,
		Owner:         owner,
		Name:          name,
		FullName:      repo.FullName,
		DefaultBranch: repo.DefaultBranch,
		Private:       repo.Private,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
