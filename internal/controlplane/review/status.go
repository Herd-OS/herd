package review

import (
	"context"
	"encoding/json"
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

type StatusClient interface {
	CreateCommitStatus(ctx context.Context, installationID int64, owner, repo, sha string, status platform.CommitStatus) error
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
	if idem, ok := s.Store.(StatusIdempotencyStore); ok {
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
				return fmt.Errorf("herd review status %q is already in progress", statusKey)
			}
		}
		if err := s.GitHub.CreateCommitStatus(ctx, repo.InstallationID, repo.Owner, repo.Name, headSHA, status); err != nil {
			_ = idem.FailIdempotencyKey(ctx, statusKey, err.Error())
			return err
		}
		if err := idem.CompleteIdempotencyKey(ctx, statusKey, "status:created"); err != nil {
			return fmt.Errorf("complete Herd Review status idempotency: %w", err)
		}
		return s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
	}
	if err := s.GitHub.CreateCommitStatus(ctx, repo.InstallationID, repo.Owner, repo.Name, headSHA, status); err != nil {
		return err
	}
	return s.recordReviewState(ctx, repo, prNumber, headSHA, state, description, targetURL, now)
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
