package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane/mutationguard"
	mutationspkg "github.com/herd-os/herd/internal/controlplane/mutations"
	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/herd-os/herd/internal/platform"
)

const HerdReviewContext = "Herd Review"

type ReviewStatusState string

const (
	ReviewStatusPending ReviewStatusState = "pending"
	ReviewStatusSuccess ReviewStatusState = "success"
	ReviewStatusFailure ReviewStatusState = "failure"
)

type Repository struct {
	ID                 int64
	InstallationID     int64
	Owner              string
	Name               string
	DefaultBranch      string
	ReviewEnabled      bool
	ReviewFixEnabled   bool
	ReviewMaxFixCycles int
	ReviewFixSeverity  string
}

type StatusStore interface {
	SetReviewState(ctx context.Context, state store.ReviewState) error
}

type StatusIdempotencyStore interface {
	AcquireIdempotencyKey(ctx context.Context, key store.IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (store.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error
}

type StatusMutationStore interface {
	RecordGitHubMutationAttempt(ctx context.Context, a store.GitHubMutationAttempt) error
	CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error
	GetGitHubMutationAttempt(ctx context.Context, idempotencyKey string) (store.GitHubMutationAttempt, error)
	TryStartGitHubMutationAttempt(ctx context.Context, idempotencyKey string, allowedStatuses []string, completedAt time.Time) (store.GitHubMutationStartResult, error)
}

type StatusClient interface {
	CreateCommitStatus(ctx context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) error
}

type StatusLookupClient interface {
	FindCommitStatus(ctx context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) (bool, error)
}

type StatusService struct {
	Store  StatusStore
	GitHub StatusClient
	Now    func() time.Time
}

func (s StatusService) SetHerdReviewStatus(ctx context.Context, repo Repository, prNumber int, headSHA string, state ReviewStatusState, description, targetURL string) error {
	if !repo.ReviewEnabled {
		return nil
	}
	if err := validateStatusInput(repo, prNumber, headSHA, state); err != nil {
		return err
	}
	if s.GitHub == nil {
		return fmt.Errorf("review status GitHub client is required")
	}
	now := s.now()
	status := platform.CommitStatus{
		State:       string(state),
		Context:     HerdReviewContext,
		Description: strings.TrimSpace(description),
		TargetURL:   strings.TrimSpace(targetURL),
	}
	statusKey := statusMutationKey(repo.ID, prNumber, headSHA, state, status.TargetURL, status.Description)
	idem, ok := s.Store.(StatusIdempotencyStore)
	if !ok {
		return fmt.Errorf("review status idempotency store is required")
	}
	mutationStore, ok := s.Store.(mutationguard.Store)
	if !ok {
		return fmt.Errorf("review status mutation store is required")
	}
	created, err := idem.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       statusKey,
		Scope:     "review_status",
		Status:    mutationspkg.PhaseIntentRecorded,
		Metadata:  mustStatusMetadata(repo, prNumber, headSHA, state, description, targetURL),
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("acquire Herd Review status idempotency: %w", err)
	}
	if !created {
		record, err := idem.GetIdempotencyKey(ctx, statusKey)
		if err != nil {
			return fmt.Errorf("get Herd Review status idempotency: %w", err)
		}
		if record.Status == "completed" {
			if err := s.repairStatusMutationFromCompletedIdempotency(ctx, mutationStore, statusKey, responseStatusCreated, now); err != nil {
				return err
			}
			return s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
		}
	}
	request := statusMutationRequest(repo, prNumber, headSHA, status)
	_, err = mutationguard.Run(ctx, mutationStore, mutationguard.RunRequest{
		Key:          statusKey,
		RepositoryID: repo.ID,
		MutationType: "review_status",
		Request:      request,
		ResultRef: func(raw json.RawMessage) string {
			var body struct {
				Status string `json:"status"`
			}
			if len(raw) == 0 || json.Unmarshal(raw, &body) != nil || body.Status != "created" {
				return ""
			}
			return "status:created"
		},
		Response: func(string) json.RawMessage {
			return responseStatusCreated
		},
		Mutate: func() (string, error) {
			if err := s.GitHub.CreateCommitStatus(ctx, repo.InstallationID, repo.Owner, repo.Name, headSHA, status); err != nil {
				return "", err
			}
			return "status:created", nil
		},
		Repair: func() (string, bool, error) {
			lookup, ok := s.GitHub.(StatusLookupClient)
			if !ok {
				return "", false, nil
			}
			found, err := lookup.FindCommitStatus(ctx, repo.InstallationID, repo.Owner, repo.Name, headSHA, status)
			if err != nil {
				return "", false, fmt.Errorf("repair Herd Review status lookup: %w", err)
			}
			return "status:created", found, nil
		},
		Now: s.now,
	})
	if err != nil {
		return wrapStatusMutationError(err)
	}
	return s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
}

var responseStatusCreated = json.RawMessage(`{"status":"created"}`)

func statusMutationRequest(repo Repository, prNumber int, headSHA string, status platform.CommitStatus) json.RawMessage {
	request, err := json.Marshal(map[string]any{
		"owner":     repo.Owner,
		"repo":      repo.Name,
		"pr_number": prNumber,
		"head_sha":  headSHA,
		"status":    status,
	})
	if err != nil {
		panic(err)
	}
	return request
}

func wrapStatusMutationError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "record mutation attempt:"):
		return fmt.Errorf("record Herd Review status mutation attempt: %w", err)
	case strings.Contains(msg, "complete mutation attempt:"):
		return fmt.Errorf("complete Herd Review status mutation attempt: %w", err)
	case strings.Contains(msg, "complete idempotency key:"):
		return fmt.Errorf("complete Herd Review status idempotency: %w", err)
	default:
		return err
	}
}

func (s StatusService) repairStatusMutationFromCompletedIdempotency(ctx context.Context, mutationStore mutationguard.Store, key string, response json.RawMessage, now time.Time) error {
	attempt, err := mutationStore.GetGitHubMutationAttempt(ctx, key)
	if err == nil {
		if mutationspkg.IsCompleted(attempt.Status) {
			return nil
		}
		if err := mutationStore.CompleteGitHubMutationAttempt(ctx, key, mutationspkg.PhaseCompleted, response, "", now); err != nil {
			return fmt.Errorf("repair Herd Review status mutation attempt: %w", err)
		}
		return nil
	}
	if err == store.ErrNotFound {
		return nil
	}
	return fmt.Errorf("get Herd Review status mutation attempt: %w", err)
}

func (s StatusService) recordReviewState(ctx context.Context, repo Repository, prNumber int, headSHA string, state ReviewStatusState, description, targetURL string, now time.Time) error {
	if s.Store != nil {
		if err := s.Store.SetReviewState(ctx, store.ReviewState{
			RepositoryID: repo.ID,
			PRNumber:     prNumber,
			HeadSHA:      headSHA,
			Status:       string(state),
			Metadata:     mustStatusMetadata(repo, prNumber, headSHA, state, description, targetURL),
			UpdatedAt:    now,
		}); err != nil {
			return fmt.Errorf("record Herd Review state: %w", err)
		}
	}
	return nil
}

func (s StatusService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func validateStatusInput(repo Repository, prNumber int, headSHA string, state ReviewStatusState) error {
	if repo.ID == 0 {
		return fmt.Errorf("repository ID is required")
	}
	if repo.InstallationID == 0 {
		return fmt.Errorf("installation ID is required")
	}
	if strings.TrimSpace(repo.Owner) == "" || strings.TrimSpace(repo.Name) == "" {
		return fmt.Errorf("repository owner and name are required")
	}
	if prNumber <= 0 {
		return fmt.Errorf("PR number is required")
	}
	if strings.TrimSpace(headSHA) == "" {
		return fmt.Errorf("head SHA is required")
	}
	switch state {
	case ReviewStatusPending, ReviewStatusSuccess, ReviewStatusFailure:
		return nil
	default:
		return fmt.Errorf("unsupported Herd Review status state %q", state)
	}
}

func statusIdempotencyKey(repoID int64, prNumber int, headSHA string) string {
	return fmt.Sprintf("herd_review_status:%d:%d:%s:%s", repoID, prNumber, headSHA, HerdReviewContext)
}

func statusMutationKey(repoID int64, prNumber int, headSHA string, state ReviewStatusState, targetURL string, description string) string {
	normalizedDescription := strings.Join(strings.Fields(description), " ")
	return fmt.Sprintf("%s:%s:%s:%s", statusIdempotencyKey(repoID, prNumber, headSHA), state, strings.TrimSpace(targetURL), normalizedDescription)
}

func mustStatusMetadata(repo Repository, prNumber int, headSHA string, state ReviewStatusState, description, targetURL string) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"repository_id":   repo.ID,
		"pr_number":       prNumber,
		"head_sha":        headSHA,
		"context":         HerdReviewContext,
		"idempotency_key": statusIdempotencyKey(repo.ID, prNumber, headSHA),
		"state":           state,
		"description":     description,
		"target_url":      targetURL,
	})
	if err != nil {
		panic(err)
	}
	return raw
}
