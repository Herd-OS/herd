package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	statusKey := statusMutationKey(repo.ID, prNumber, headSHA, state, status.TargetURL)
	idem, ok := s.Store.(StatusIdempotencyStore)
	if !ok {
		return fmt.Errorf("review status idempotency store is required")
	}
	if _, ok := s.Store.(StatusMutationStore); !ok {
		return fmt.Errorf("review status mutation store is required")
	}
	created, err := idem.AcquireIdempotencyKey(ctx, store.IdempotencyKey{
		Key:       statusKey,
		Scope:     "review_status",
		Status:    "started",
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
			return s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
		}
		if record.Status == "started" {
			if repaired, err := s.repairCompletedStatusMutation(ctx, idem, statusKey, repo, prNumber, headSHA, state, description, targetURL, now); repaired || err != nil {
				return err
			}
			if repaired, err := s.repairStartedStatusMutation(ctx, idem, statusKey, repo, prNumber, headSHA, status, state, description, targetURL, now); repaired || err != nil {
				return err
			}
			return fmt.Errorf("herd review status %q is already in progress", statusKey)
		}
	}
	if err := s.ensureStatusMutationAttempt(ctx, statusKey, repo, prNumber, headSHA, status, now); err != nil {
		return err
	}
	if err := s.GitHub.CreateCommitStatus(ctx, repo.InstallationID, repo.Owner, repo.Name, headSHA, status); err != nil {
		_ = s.completeStatusMutation(ctx, statusKey, "failed", nil, err, now)
		_ = idem.FailIdempotencyKey(ctx, statusKey, err.Error())
		return err
	}
	if err := s.completeStatusMutation(ctx, statusKey, "completed", json.RawMessage(`{"status":"created"}`), nil, now); err != nil {
		return err
	}
	if err := idem.CompleteIdempotencyKey(ctx, statusKey, "status:created"); err != nil {
		return fmt.Errorf("complete Herd Review status idempotency: %w", err)
	}
	return s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
}

func (s StatusService) recordStatusMutationAttempt(ctx context.Context, key string, repo Repository, prNumber int, headSHA string, status platform.CommitStatus, now time.Time) error {
	mutations, ok := s.Store.(StatusMutationStore)
	if !ok {
		return nil
	}
	request, err := json.Marshal(map[string]any{
		"owner":     repo.Owner,
		"repo":      repo.Name,
		"pr_number": prNumber,
		"head_sha":  headSHA,
		"status":    status,
	})
	if err != nil {
		return fmt.Errorf("marshal Herd Review status mutation: %w", err)
	}
	if err := mutations.RecordGitHubMutationAttempt(ctx, store.GitHubMutationAttempt{
		IdempotencyKey: key,
		RepositoryID:   repo.ID,
		MutationType:   "review_status",
		Status:         "started",
		Request:        request,
		CreatedAt:      now,
	}); err != nil {
		return fmt.Errorf("record Herd Review status mutation attempt: %w", err)
	}
	return nil
}

func (s StatusService) ensureStatusMutationAttempt(ctx context.Context, key string, repo Repository, prNumber int, headSHA string, status platform.CommitStatus, now time.Time) error {
	mutations, ok := s.Store.(StatusMutationStore)
	if !ok {
		return nil
	}
	attempt, err := mutations.GetGitHubMutationAttempt(ctx, key)
	if err == nil {
		switch attempt.Status {
		case "completed":
			return nil
		case "failed":
			if err := mutations.CompleteGitHubMutationAttempt(ctx, key, "started", nil, "", now); err != nil {
				return fmt.Errorf("reopen Herd Review status mutation attempt: %w", err)
			}
			return nil
		case "started":
			return nil
		default:
			return fmt.Errorf("herd review status mutation attempt %q is %s", key, attempt.Status)
		}
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("get Herd Review status mutation attempt: %w", err)
	}
	return s.recordStatusMutationAttempt(ctx, key, repo, prNumber, headSHA, status, now)
}

func (s StatusService) completeStatusMutation(ctx context.Context, key, status string, response json.RawMessage, resultErr error, now time.Time) error {
	mutations, ok := s.Store.(StatusMutationStore)
	if !ok {
		return nil
	}
	errorMessage := ""
	if resultErr != nil {
		errorMessage = resultErr.Error()
	}
	if err := mutations.CompleteGitHubMutationAttempt(ctx, key, status, response, errorMessage, now); err != nil {
		return fmt.Errorf("complete Herd Review status mutation attempt: %w", err)
	}
	return nil
}

func (s StatusService) repairCompletedStatusMutation(ctx context.Context, idem StatusIdempotencyStore, key string, repo Repository, prNumber int, headSHA string, state ReviewStatusState, description, targetURL string, now time.Time) (bool, error) {
	mutations, ok := s.Store.(StatusMutationStore)
	if !ok {
		return false, nil
	}
	attempt, err := mutations.GetGitHubMutationAttempt(ctx, key)
	if err != nil {
		return false, nil
	}
	if attempt.Status != "completed" {
		return false, nil
	}
	if err := idem.CompleteIdempotencyKey(ctx, key, "status:created"); err != nil {
		return true, fmt.Errorf("repair Herd Review status idempotency: %w", err)
	}
	return true, s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
}

func (s StatusService) repairStartedStatusMutation(ctx context.Context, idem StatusIdempotencyStore, key string, repo Repository, prNumber int, headSHA string, status platform.CommitStatus, state ReviewStatusState, description, targetURL string, now time.Time) (bool, error) {
	mutations, ok := s.Store.(StatusMutationStore)
	if !ok {
		return false, nil
	}
	attempt, err := mutations.GetGitHubMutationAttempt(ctx, key)
	if err != nil || attempt.Status != "started" {
		return false, nil
	}
	lookup, ok := s.GitHub.(StatusLookupClient)
	if !ok {
		return false, nil
	}
	found, err := lookup.FindCommitStatus(ctx, repo.InstallationID, repo.Owner, repo.Name, headSHA, status)
	if err != nil {
		return false, fmt.Errorf("repair Herd Review status lookup: %w", err)
	}
	if !found {
		return false, nil
	}
	response := json.RawMessage(`{"status":"created","repaired":true}`)
	if err := mutations.CompleteGitHubMutationAttempt(ctx, key, "completed", response, "", now); err != nil {
		return true, fmt.Errorf("repair Herd Review status mutation attempt: %w", err)
	}
	if err := idem.CompleteIdempotencyKey(ctx, key, "status:created"); err != nil {
		return true, fmt.Errorf("repair Herd Review status idempotency: %w", err)
	}
	return true, s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
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

func statusMutationKey(repoID int64, prNumber int, headSHA string, state ReviewStatusState, targetURL string) string {
	return fmt.Sprintf("%s:%s:%s", statusIdempotencyKey(repoID, prNumber, headSHA), state, strings.TrimSpace(targetURL))
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
