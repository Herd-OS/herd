package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is an in-process Store for orchestration tests that do not need
// SQL constraint behavior.
type MemoryStore struct {
	mu sync.Mutex

	closed bool

	webhookDeliveries map[string]WebhookDelivery
	installations     map[int64]Installation
	repositories      map[string]Repository
	regAttempts       []RegistrationAttempt
	tokens            map[int64]RunnerBootstrapToken
	jobs              map[string]Job
	jobResults        map[string]JobResult
	idempotencyKeys   map[string]IdempotencyKey
	mutationAttempts  map[string]GitHubMutationAttempt
	reviewStates      map[string]ReviewState
	commandRecords    map[string]CommandRecord
	reviewLocks       map[string]ReviewLock

	nextTokenID int64
	nextRepoID  int64
}

// NewMemoryStore returns an empty in-memory control-plane store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		webhookDeliveries: map[string]WebhookDelivery{},
		installations:     map[int64]Installation{},
		repositories:      map[string]Repository{},
		tokens:            map[int64]RunnerBootstrapToken{},
		jobs:              map[string]Job{},
		jobResults:        map[string]JobResult{},
		idempotencyKeys:   map[string]IdempotencyKey{},
		mutationAttempts:  map[string]GitHubMutationAttempt{},
		reviewStates:      map[string]ReviewState{},
		commandRecords:    map[string]CommandRecord{},
		reviewLocks:       map[string]ReviewLock{},
		nextTokenID:       1,
		nextRepoID:        1,
	}
}

func (s *MemoryStore) Health(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrNotFound
	}
	return nil
}

func (s *MemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *MemoryStore) RecordWebhookDelivery(_ context.Context, d WebhookDelivery) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.webhookDeliveries[d.DeliveryID]; ok {
		return false, nil
	}
	s.webhookDeliveries[d.DeliveryID] = d
	return true, nil
}

func (s *MemoryStore) GetWebhookDelivery(_ context.Context, deliveryID string) (WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery, ok := s.webhookDeliveries[deliveryID]
	if !ok {
		return WebhookDelivery{}, ErrNotFound
	}
	return delivery, nil
}

func (s *MemoryStore) UpdateWebhookDeliveryStatus(_ context.Context, deliveryID string, status string, errorMessage string, processedAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery, ok := s.webhookDeliveries[deliveryID]
	if !ok {
		return ErrNotFound
	}
	delivery.Status = status
	delivery.Error = errorMessage
	delivery.ProcessedAt = processedAt
	s.webhookDeliveries[deliveryID] = delivery
	return nil
}

func (s *MemoryStore) UpsertInstallation(_ context.Context, i Installation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installations[i.ID] = i
	return nil
}

func (s *MemoryStore) UpsertRepository(_ context.Context, r Repository) (Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := repoKey(r.Owner, r.Name)
	if existing, ok := s.repositories[key]; ok && r.ID == 0 {
		r.ID = existing.ID
	}
	if r.ID == 0 {
		r.ID = s.nextRepoID
		s.nextRepoID++
	}
	s.repositories[key] = r
	return r, nil
}

func (s *MemoryStore) GetRepository(_ context.Context, owner string, name string) (Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, ok := s.repositories[repoKey(owner, name)]
	if !ok {
		return Repository{}, ErrNotFound
	}
	return repo, nil
}

func (s *MemoryStore) CreateRegistrationAttempt(_ context.Context, a RegistrationAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regAttempts = append(s.regAttempts, a)
	return nil
}

func (s *MemoryStore) CreateRunnerBootstrapToken(_ context.Context, t RunnerBootstrapToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == 0 {
		t.ID = s.nextTokenID
		s.nextTokenID++
	}
	s.tokens[t.ID] = t
	return nil
}

func (s *MemoryStore) RotateRunnerBootstrapToken(_ context.Context, repoID int64, tokenHash string) (RunnerBootstrapToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for id, token := range s.tokens {
		if token.RepositoryID == repoID && token.RevokedAt == nil {
			token.RevokedAt = &now
			token.RevokedReason = "rotated"
			s.tokens[id] = token
		}
	}
	token := RunnerBootstrapToken{
		ID:           s.nextTokenID,
		RepositoryID: repoID,
		TokenHash:    tokenHash,
		CreatedAt:    now,
		ExpiresAt:    now.Add(24 * time.Hour),
	}
	s.nextTokenID++
	s.tokens[token.ID] = token
	return token, nil
}

func (s *MemoryStore) GetRunnerBootstrapTokenByHash(_ context.Context, tokenHash string) (RunnerBootstrapToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range s.tokens {
		if token.TokenHash == tokenHash {
			return token, nil
		}
	}
	return RunnerBootstrapToken{}, ErrNotFound
}

func (s *MemoryStore) RevokeRunnerBootstrapToken(_ context.Context, tokenID int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[tokenID]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	token.RevokedAt = &now
	token.RevokedReason = reason
	s.tokens[tokenID] = token
	return nil
}

func (s *MemoryStore) MarkRunnerBootstrapTokenUsed(_ context.Context, tokenID int64, usedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[tokenID]
	if !ok {
		return ErrNotFound
	}
	token.UsedAt = &usedAt
	s.tokens[tokenID] = token
	return nil
}

func (s *MemoryStore) CreateJob(_ context.Context, j Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.JobID] = j
	return nil
}

func (s *MemoryStore) GetJob(_ context.Context, jobID string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return Job{}, ErrNotFound
	}
	return job, nil
}

func (s *MemoryStore) RecordJobResult(_ context.Context, r JobResult) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.JobID + "\x00" + r.IdempotencyKey
	if _, ok := s.jobResults[key]; ok {
		return false, nil
	}
	s.jobResults[key] = r
	return true, nil
}

func (s *MemoryStore) ListReconcileJobs(_ context.Context, updatedBefore time.Time, limit int) ([]ReconcileJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ReconcileJob
	for _, job := range s.jobs {
		if limit > 0 && len(out) >= limit {
			break
		}
		if !job.UpdatedAt.IsZero() && !job.UpdatedAt.Before(updatedBefore) {
			continue
		}
		if !reconcileJobStatus(job.Status) {
			continue
		}
		repo := s.repositoryByID(job.RepositoryID)
		out = append(out, ReconcileJob{
			Job:         job,
			Repository:  repo,
			ResultCount: s.jobResultCount(job.JobID),
		})
	}
	return out, nil
}

func (s *MemoryStore) UpdateJobStatus(_ context.Context, jobID string, status string, metadata json.RawMessage, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return ErrNotFound
	}
	job.Status = status
	job.Metadata = metadataOrEmpty(metadata)
	job.UpdatedAt = timeOrNow(updatedAt)
	s.jobs[jobID] = job
	return nil
}

func (s *MemoryStore) AcquireIdempotencyKey(_ context.Context, key IdempotencyKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idempotencyKeys[key.Key]; ok {
		return false, nil
	}
	s.idempotencyKeys[key.Key] = key
	return true, nil
}

func (s *MemoryStore) GetIdempotencyKey(_ context.Context, key string) (IdempotencyKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return IdempotencyKey{}, ErrNotFound
	}
	return record, nil
}

func (s *MemoryStore) CompleteIdempotencyKey(_ context.Context, key string, resultRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "completed"
	record.ResultRef = resultRef
	record.CompletedAt = &now
	s.idempotencyKeys[key] = record
	return nil
}

func (s *MemoryStore) FailIdempotencyKey(_ context.Context, key string, errorMessage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idempotencyKeys[key]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	record.Status = "failed"
	record.ResultRef = errorMessage
	record.CompletedAt = &now
	s.idempotencyKeys[key] = record
	return nil
}

func (s *MemoryStore) ListStartedIdempotencyKeys(_ context.Context, scope string, createdBefore time.Time, limit int) ([]IdempotencyKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []IdempotencyKey
	for _, record := range s.idempotencyKeys {
		if limit > 0 && len(out) >= limit {
			break
		}
		if record.Scope != scope || record.Status != "started" {
			continue
		}
		if !record.CreatedAt.IsZero() && !record.CreatedAt.Before(createdBefore) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *MemoryStore) RecordGitHubMutationAttempt(_ context.Context, a GitHubMutationAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mutationAttempts[a.IdempotencyKey]; ok {
		return nil
	}
	if a.Status == "" {
		a.Status = "started"
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	s.mutationAttempts[a.IdempotencyKey] = a
	return nil
}

func (s *MemoryStore) CompleteGitHubMutationAttempt(_ context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt, ok := s.mutationAttempts[idempotencyKey]
	if !ok {
		return ErrNotFound
	}
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	attempt.Status = status
	attempt.Response = metadataOrEmpty(response)
	attempt.Error = errorMessage
	attempt.CompletedAt = &completedAt
	s.mutationAttempts[idempotencyKey] = attempt
	return nil
}

func (s *MemoryStore) ListStartedGitHubMutationAttempts(_ context.Context, createdBefore time.Time, limit int) ([]GitHubMutationAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GitHubMutationAttempt
	for _, attempt := range s.mutationAttempts {
		if limit > 0 && len(out) >= limit {
			break
		}
		if attempt.Status != "started" {
			continue
		}
		if !attempt.CreatedAt.IsZero() && !attempt.CreatedAt.Before(createdBefore) {
			continue
		}
		out = append(out, attempt)
	}
	return out, nil
}

func (s *MemoryStore) SetReviewState(_ context.Context, state ReviewState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviewStates[reviewStateKey(state.RepositoryID, state.PRNumber, state.HeadSHA)] = state
	return nil
}

func (s *MemoryStore) ListReconcileReviewStates(_ context.Context, updatedBefore time.Time, limit int) ([]ReconcileReviewState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ReconcileReviewState
	for _, state := range s.reviewStates {
		if limit > 0 && len(out) >= limit {
			break
		}
		if !state.UpdatedAt.IsZero() && !state.UpdatedAt.Before(updatedBefore) {
			continue
		}
		if state.Status == "abandoned" {
			continue
		}
		repo := s.repositoryByID(state.RepositoryID)
		out = append(out, ReconcileReviewState{State: state, Repository: repo})
	}
	return out, nil
}

func (s *MemoryStore) GetReviewState(_ context.Context, repoID int64, prNumber int, headSHA string) (ReviewState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.reviewStates[reviewStateKey(repoID, prNumber, headSHA)]
	if !ok {
		return ReviewState{}, ErrNotFound
	}
	return state, nil
}

func (s *MemoryStore) RecordCommand(_ context.Context, c CommandRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := commandKey(c.RepositoryID, c.CommentID, c.CommandKey)
	if _, ok := s.commandRecords[key]; ok {
		return false, nil
	}
	s.commandRecords[key] = c
	return true, nil
}

func (s *MemoryStore) ListReconcileCommands(_ context.Context, createdBefore time.Time, limit int) ([]ReconcileCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ReconcileCommand
	for _, command := range s.commandRecords {
		if limit > 0 && len(out) >= limit {
			break
		}
		if command.Status != "acknowledged" && command.Status != "retry_needed" {
			continue
		}
		if !command.CreatedAt.IsZero() && !command.CreatedAt.Before(createdBefore) {
			continue
		}
		repo := s.repositoryByID(command.RepositoryID)
		key := fmt.Sprintf("repo:%d:comment:%d:command:%s", command.RepositoryID, command.CommentID, command.CommandKey)
		record, seen := s.idempotencyKeys[key]
		out = append(out, ReconcileCommand{
			Command:         command,
			Repository:      repo,
			IdempotencyKey:  key,
			Idempotency:     record,
			IdempotencySeen: seen,
		})
	}
	return out, nil
}

func (s *MemoryStore) UpdateCommandStatus(_ context.Context, repoID int64, commentID int64, commandKey string, status string, metadata json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := commandKeyFunc(repoID, commentID, commandKey)
	command, ok := s.commandRecords[key]
	if !ok {
		return ErrNotFound
	}
	command.Status = status
	command.Metadata = metadataOrEmpty(metadata)
	s.commandRecords[key] = command
	return nil
}

func (s *MemoryStore) AcquireReviewLock(_ context.Context, lock ReviewLock) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := reviewStateKey(lock.RepositoryID, lock.PRNumber, lock.HeadSHA)
	if active, ok := s.reviewLocks[key]; ok {
		now := timeOrNow(lock.AcquiredAt)
		if active.ExpiresAt.IsZero() || active.ExpiresAt.After(now) {
			return false, nil
		}
	}
	s.reviewLocks[key] = lock
	return true, nil
}

func (s *MemoryStore) ReleaseReviewLock(_ context.Context, repoID int64, prNumber int, headSHA string, holder string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := reviewStateKey(repoID, prNumber, headSHA)
	active, ok := s.reviewLocks[key]
	if !ok {
		return ErrNotFound
	}
	if active.Holder != holder {
		return ErrNotFound
	}
	delete(s.reviewLocks, key)
	return nil
}

func repoKey(owner, name string) string {
	return owner + "/" + name
}

func reviewStateKey(repoID int64, prNumber int, headSHA string) string {
	return fmt.Sprintf("%d/%d/%s", repoID, prNumber, headSHA)
}

func commandKey(repoID, commentID int64, key string) string {
	return commandKeyFunc(repoID, commentID, key)
}

func commandKeyFunc(repoID, commentID int64, key string) string {
	return fmt.Sprintf("%d/%d/%s", repoID, commentID, key)
}

func reconcileJobStatus(status string) bool {
	switch status {
	case "dispatching", "dispatched", "queued", "in_progress", "started":
		return true
	default:
		return false
	}
}

func (s *MemoryStore) repositoryByID(id int64) Repository {
	for _, repo := range s.repositories {
		if repo.ID == id {
			return repo
		}
	}
	return Repository{}
}

func (s *MemoryStore) jobResultCount(jobID string) int {
	count := 0
	prefix := jobID + "\x00"
	for key := range s.jobResults {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}
