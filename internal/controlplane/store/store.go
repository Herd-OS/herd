package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested control-plane record does not exist.
var ErrNotFound = errors.New("control-plane store record not found")

// Store persists durable control-plane state. Implementations must use database
// uniqueness constraints for idempotent methods so concurrent callers observe
// the same created=false result instead of racing in application code.
type Store interface {
	Health(ctx context.Context) error
	Close() error

	RecordWebhookDelivery(ctx context.Context, d WebhookDelivery) (created bool, err error)
	UpsertInstallation(ctx context.Context, i Installation) error
	UpsertRepository(ctx context.Context, r Repository) (Repository, error)
	GetRepository(ctx context.Context, owner string, name string) (Repository, error)
	CreateRegistrationAttempt(ctx context.Context, a RegistrationAttempt) error
	CreateRunnerBootstrapToken(ctx context.Context, t RunnerBootstrapToken) error
	RotateRunnerBootstrapToken(ctx context.Context, repoID int64, tokenHash string) (RunnerBootstrapToken, error)
	GetRunnerBootstrapTokenByHash(ctx context.Context, tokenHash string) (RunnerBootstrapToken, error)
	RevokeRunnerBootstrapToken(ctx context.Context, tokenID int64, reason string) error
	MarkRunnerBootstrapTokenUsed(ctx context.Context, tokenID int64, usedAt time.Time) error
	CreateJob(ctx context.Context, j Job) error
	GetJob(ctx context.Context, jobID string) (Job, error)
	RecordJobResult(ctx context.Context, r JobResult) (created bool, err error)
	AcquireIdempotencyKey(ctx context.Context, key IdempotencyKey) (created bool, err error)
	GetIdempotencyKey(ctx context.Context, key string) (IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error
	RecordCommand(ctx context.Context, c CommandRecord) (created bool, err error)
	SetReviewState(ctx context.Context, s ReviewState) error
	GetReviewState(ctx context.Context, repoID int64, prNumber int, headSHA string) (ReviewState, error)
	AcquireReviewLock(ctx context.Context, lock ReviewLock) (created bool, err error)
	ReleaseReviewLock(ctx context.Context, repoID int64, prNumber int, headSHA string, holder string, releasedAt time.Time) error
}

// Installation mirrors a GitHub App installation in app_installations.
type Installation struct {
	ID           int64
	AccountLogin string
	AccountID    int64
	TargetType   string
	Permissions  json.RawMessage
	Events       []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Repository stores a registered GitHub repository in repositories.
type Repository struct {
	ID             int64
	GitHubID       int64
	InstallationID int64
	Owner          string
	Name           string
	DefaultBranch  string
	Private        bool
	RegisteredAt   time.Time
	UpdatedAt      time.Time
	Metadata       json.RawMessage
}

// RegistrationAttempt records a repository registration workflow attempt.
type RegistrationAttempt struct {
	ID             int64
	RepositoryID   int64
	InstallationID int64
	Owner          string
	Name           string
	Status         string
	Error          string
	Metadata       json.RawMessage
	CreatedAt      time.Time
}

// RunnerBootstrapToken stores a hashed one-time credential for runner bootstrap.
type RunnerBootstrapToken struct {
	ID            int64
	RepositoryID  int64
	TokenHash     string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
	UsedAt        *time.Time
}

// WebhookDelivery stores GitHub webhook delivery processing state.
type WebhookDelivery struct {
	ID          int64
	DeliveryID  string
	Event       string
	Action      string
	PayloadHash string
	Status      string
	Error       string
	Metadata    json.RawMessage
	ReceivedAt  time.Time
	ProcessedAt *time.Time
}

// Job stores a durable unit of control-plane work in jobs.
type Job struct {
	ID             int64
	JobID          string
	RepositoryID   int64
	InstallationID int64
	PRNumber       int
	HeadSHA        string
	BaseSHA        string
	Status         string
	WorkerBranch   string
	Metadata       json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// JobResult stores an idempotent callback/result for a job in job_results.
type JobResult struct {
	ID             int64
	JobID          string
	IdempotencyKey string
	Status         string
	ResultRef      string
	Metadata       json.RawMessage
	CreatedAt      time.Time
}

// IdempotencyKey records command/API idempotency state in idempotency_keys.
type IdempotencyKey struct {
	Key         string
	Scope       string
	Status      string
	ResultRef   string
	ExpiresAt   *time.Time
	Metadata    json.RawMessage
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// GitHubMutationAttempt stores an outbound GitHub API mutation attempt.
type GitHubMutationAttempt struct {
	ID             int64
	IdempotencyKey string
	RepositoryID   int64
	MutationType   string
	Status         string
	Request        json.RawMessage
	Response       json.RawMessage
	Error          string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

// ReviewState stores the latest review state for a repo, PR, and head SHA.
type ReviewState struct {
	ID           int64
	RepositoryID int64
	PRNumber     int
	HeadSHA      string
	Status       string
	LastJobID    string
	Metadata     json.RawMessage
	UpdatedAt    time.Time
}

// CommandRecord stores command comment idempotency in command_records.
type CommandRecord struct {
	ID           int64
	RepositoryID int64
	CommentID    int64
	CommandKey   string
	CommandName  string
	Actor        string
	Status       string
	Metadata     json.RawMessage
	CreatedAt    time.Time
}

// ReviewLock stores active review lock ownership in review_locks.
type ReviewLock struct {
	ID           int64
	RepositoryID int64
	PRNumber     int
	HeadSHA      string
	Holder       string
	ExpiresAt    time.Time
	AcquiredAt   time.Time
}

func metadataOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func timeOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}
