package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPostgresMigrationsApplyFromEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db := newPostgresDB(t, ctx)

	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, ValidateMigrations(ctx, db))

	for _, table := range []string{
		"app_installations",
		"repositories",
		"registration_attempts",
		"runner_bootstrap_tokens",
		"webhook_deliveries",
		"jobs",
		"job_results",
		"review_states",
		"review_locks",
		"command_records",
		"idempotency_keys",
		"github_mutation_attempts",
		"audit_records",
	} {
		t.Run(table, func(t *testing.T) {
			var exists bool
			err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)", table).Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists)
		})
	}
}

func TestPostgresStoreRequiresMigrationsUnlessExplicitlyEnabled(t *testing.T) {
	ctx := context.Background()
	db := newPostgresDB(t, ctx)

	_, err := NewPostgresStore(ctx, db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migrations")

	store, err := NewPostgresStore(ctx, db, WithMigrateOnStart())
	require.NoError(t, err)
	require.NoError(t, store.Health(ctx))
}

func TestPostgresConstraintBackedIdempotency(t *testing.T) {
	ctx := context.Background()
	db := newMigratedPostgresDB(t, ctx)
	store, err := NewPostgresStore(ctx, db)
	require.NoError(t, err)
	repoID := seedRepository(t, ctx, store, db)

	t.Run("webhook delivery dedupe", func(t *testing.T) {
		delivery := WebhookDelivery{
			DeliveryID:  "delivery-1",
			Event:       "issue_comment",
			Action:      "created",
			PayloadHash: "sha256:payload",
			Status:      "processing",
		}
		created, err := store.RecordWebhookDelivery(ctx, delivery)
		require.NoError(t, err)
		assert.True(t, created)

		created, err = store.RecordWebhookDelivery(ctx, delivery)
		require.NoError(t, err)
		assert.False(t, created)

		got, err := store.GetWebhookDelivery(ctx, "delivery-1")
		require.NoError(t, err)
		assert.Equal(t, "processing", got.Status)

		processedAt := time.Now().UTC()
		require.NoError(t, store.UpdateWebhookDeliveryStatus(ctx, "delivery-1", "processed", "", &processedAt))
		got, err = store.GetWebhookDelivery(ctx, "delivery-1")
		require.NoError(t, err)
		assert.Equal(t, "processed", got.Status)
		require.NotNil(t, got.ProcessedAt)
	})

	t.Run("command repo comment command key", func(t *testing.T) {
		command := CommandRecord{
			RepositoryID: repoID,
			CommentID:    10,
			CommandKey:   "comment-10:/fix",
			CommandName:  "fix",
			Actor:        "octo",
			Status:       "accepted",
		}
		created, err := store.RecordCommand(ctx, command)
		require.NoError(t, err)
		assert.True(t, created)

		created, err = store.RecordCommand(ctx, command)
		require.NoError(t, err)
		assert.False(t, created)
	})

	t.Run("job result callback acceptance", func(t *testing.T) {
		require.NoError(t, store.CreateJob(ctx, Job{
			JobID:          "job-1",
			RepositoryID:   repoID,
			InstallationID: 1001,
			PRNumber:       42,
			HeadSHA:        "abc123",
			Status:         "running",
		}))
		result := JobResult{JobID: "job-1", IdempotencyKey: "callback-1", Status: "succeeded", ResultRef: "checks/1"}
		created, err := store.RecordJobResult(ctx, result)
		require.NoError(t, err)
		assert.True(t, created)

		created, err = store.RecordJobResult(ctx, result)
		require.NoError(t, err)
		assert.False(t, created)
	})

	t.Run("active review lock uniqueness", func(t *testing.T) {
		now := time.Now().UTC()
		lock := ReviewLock{
			RepositoryID: repoID,
			PRNumber:     42,
			HeadSHA:      "abc123",
			Holder:       "worker-a",
			AcquiredAt:   now,
			ExpiresAt:    now.Add(time.Hour),
		}
		created, err := store.AcquireReviewLock(ctx, lock)
		require.NoError(t, err)
		assert.True(t, created)

		lock.Holder = "worker-b"
		created, err = store.AcquireReviewLock(ctx, lock)
		require.NoError(t, err)
		assert.False(t, created)
	})

	t.Run("expired review lock is reclaimed and can be released", func(t *testing.T) {
		past := time.Now().UTC().Add(-2 * time.Hour)
		lock := ReviewLock{
			RepositoryID: repoID,
			PRNumber:     43,
			HeadSHA:      "def456",
			Holder:       "worker-a",
			AcquiredAt:   past,
			ExpiresAt:    past.Add(time.Hour),
		}
		created, err := store.AcquireReviewLock(ctx, lock)
		require.NoError(t, err)
		assert.True(t, created)

		now := time.Now().UTC()
		lock.Holder = "worker-b"
		lock.AcquiredAt = now
		lock.ExpiresAt = now.Add(time.Hour)
		created, err = store.AcquireReviewLock(ctx, lock)
		require.NoError(t, err)
		assert.True(t, created)
		require.NoError(t, store.ReleaseReviewLock(ctx, repoID, 43, "def456", "worker-b", now.Add(time.Minute)))

		lock.Holder = "worker-c"
		created, err = store.AcquireReviewLock(ctx, lock)
		require.NoError(t, err)
		assert.True(t, created)
	})

	t.Run("runner token revocation permits hash reuse", func(t *testing.T) {
		token := RunnerBootstrapToken{
			RepositoryID: repoID,
			TokenHash:    "hash-1",
			ExpiresAt:    time.Now().UTC().Add(time.Hour),
		}
		require.NoError(t, store.CreateRunnerBootstrapToken(ctx, token))

		var tokenID int64
		require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM runner_bootstrap_tokens WHERE token_hash = $1", token.TokenHash).Scan(&tokenID))
		require.NoError(t, store.RevokeRunnerBootstrapToken(ctx, tokenID, "test"))
		require.NoError(t, store.CreateRunnerBootstrapToken(ctx, token))

		err := store.RevokeRunnerBootstrapToken(ctx, 999999, "missing")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestPostgresStoreMethods(t *testing.T) {
	ctx := context.Background()
	db := newMigratedPostgresDB(t, ctx)
	store, err := NewPostgresStore(ctx, db)
	require.NoError(t, err)
	repoID := seedRepository(t, ctx, store, db)

	require.NoError(t, store.CreateRegistrationAttempt(ctx, RegistrationAttempt{
		RepositoryID:   repoID,
		InstallationID: 1001,
		Owner:          "octo",
		Name:           "repo",
		Status:         "created",
	}))

	token, err := store.RotateRunnerBootstrapToken(ctx, repoID, "rotated-hash")
	require.NoError(t, err)
	assert.NotZero(t, token.ID)
	gotToken, err := store.GetRunnerBootstrapTokenByHash(ctx, "rotated-hash")
	require.NoError(t, err)
	assert.Equal(t, token.ID, gotToken.ID)
	_, err = store.GetRunnerBootstrapTokenByHash(ctx, "missing-hash")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.MarkRunnerBootstrapTokenUsed(ctx, token.ID, time.Now().UTC()))

	job := Job{
		JobID:          "job-get",
		RepositoryID:   repoID,
		InstallationID: 1001,
		PRNumber:       7,
		HeadSHA:        "def456",
		BaseSHA:        "base456",
		Status:         "queued",
		WorkerBranch:   "worker/job-get",
		Metadata:       []byte(`{"source":"test"}`),
	}
	require.NoError(t, store.CreateJob(ctx, job))
	gotJob, err := store.GetJob(ctx, job.JobID)
	require.NoError(t, err)
	assert.Equal(t, job.JobID, gotJob.JobID)
	assert.JSONEq(t, string(job.Metadata), string(gotJob.Metadata))

	_, err = store.GetJob(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	created, err := store.AcquireIdempotencyKey(ctx, IdempotencyKey{Key: "key-1", Scope: "commands", Status: "started"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = store.AcquireIdempotencyKey(ctx, IdempotencyKey{Key: "key-1", Scope: "commands", Status: "started"})
	require.NoError(t, err)
	assert.False(t, created)
	require.NoError(t, store.CompleteIdempotencyKey(ctx, "key-1", "result-1"))
	gotKey, err := store.GetIdempotencyKey(ctx, "key-1")
	require.NoError(t, err)
	assert.Equal(t, "completed", gotKey.Status)
	assert.Equal(t, "result-1", gotKey.ResultRef)
	_, err = store.GetIdempotencyKey(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, store.CompleteIdempotencyKey(ctx, "missing", "result"), ErrNotFound)

	state := ReviewState{RepositoryID: repoID, PRNumber: 7, HeadSHA: "def456", Status: "pending", LastJobID: job.JobID}
	require.NoError(t, store.SetReviewState(ctx, state))
	state.Status = "complete"
	require.NoError(t, store.SetReviewState(ctx, state))
	gotState, err := store.GetReviewState(ctx, repoID, 7, "def456")
	require.NoError(t, err)
	assert.Equal(t, "complete", gotState.Status)
	_, err = store.GetReviewState(ctx, repoID, 7, "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPostgresHealthFailure(t *testing.T) {
	ctx := context.Background()
	db := newMigratedPostgresDB(t, ctx)
	store, err := NewPostgresStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	err = store.Health(ctx)
	require.Error(t, err)
}

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	created, err := s.RecordWebhookDelivery(ctx, WebhookDelivery{DeliveryID: "d1"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.RecordWebhookDelivery(ctx, WebhookDelivery{DeliveryID: "d1"})
	require.NoError(t, err)
	assert.False(t, created)
	require.NoError(t, s.UpdateWebhookDeliveryStatus(ctx, "d1", "failed", "boom", nil))
	delivery, err := s.GetWebhookDelivery(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, "failed", delivery.Status)
	assert.Equal(t, "boom", delivery.Error)

	require.NoError(t, s.UpsertInstallation(ctx, Installation{ID: 1, AccountLogin: "octo"}))
	repo, err := s.UpsertRepository(ctx, Repository{Owner: "octo", Name: "repo"})
	require.NoError(t, err)
	require.NotZero(t, repo.ID)
	require.NoError(t, s.CreateRegistrationAttempt(ctx, RegistrationAttempt{Owner: "octo", Name: "repo", Status: "ok"}))

	require.NoError(t, s.CreateRunnerBootstrapToken(ctx, RunnerBootstrapToken{ID: 1, RepositoryID: 2, TokenHash: "h"}))
	gotToken, err := s.GetRunnerBootstrapTokenByHash(ctx, "h")
	require.NoError(t, err)
	assert.Equal(t, int64(1), gotToken.ID)
	_, err = s.GetRunnerBootstrapTokenByHash(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	token, err := s.RotateRunnerBootstrapToken(ctx, 2, "h2")
	require.NoError(t, err)
	require.NoError(t, s.RevokeRunnerBootstrapToken(ctx, token.ID, "done"))
	require.NoError(t, s.MarkRunnerBootstrapTokenUsed(ctx, token.ID, time.Now().UTC()))
	require.ErrorIs(t, s.RevokeRunnerBootstrapToken(ctx, 404, "missing"), ErrNotFound)
	require.ErrorIs(t, s.MarkRunnerBootstrapTokenUsed(ctx, 404, time.Now().UTC()), ErrNotFound)

	require.NoError(t, s.CreateJob(ctx, Job{JobID: "j1"}))
	job, err := s.GetJob(ctx, "j1")
	require.NoError(t, err)
	assert.Equal(t, "j1", job.JobID)
	_, err = s.GetJob(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	created, err = s.RecordJobResult(ctx, JobResult{JobID: "j1", IdempotencyKey: "r1"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.RecordJobResult(ctx, JobResult{JobID: "j1", IdempotencyKey: "r1"})
	require.NoError(t, err)
	assert.False(t, created)

	created, err = s.AcquireIdempotencyKey(ctx, IdempotencyKey{Key: "k1"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.AcquireIdempotencyKey(ctx, IdempotencyKey{Key: "k1"})
	require.NoError(t, err)
	assert.False(t, created)
	require.NoError(t, s.CompleteIdempotencyKey(ctx, "k1", "ref"))
	gotKey, err := s.GetIdempotencyKey(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, "ref", gotKey.ResultRef)
	_, err = s.GetIdempotencyKey(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.CompleteIdempotencyKey(ctx, "missing", "ref"), ErrNotFound)

	require.NoError(t, s.SetReviewState(ctx, ReviewState{RepositoryID: 2, PRNumber: 3, HeadSHA: "sha", Status: "open"}))
	state, err := s.GetReviewState(ctx, 2, 3, "sha")
	require.NoError(t, err)
	assert.Equal(t, "open", state.Status)
	_, err = s.GetReviewState(ctx, 2, 3, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	created, err = s.RecordCommand(ctx, CommandRecord{RepositoryID: 2, CommentID: 5, CommandKey: "cmd"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.RecordCommand(ctx, CommandRecord{RepositoryID: 2, CommentID: 5, CommandKey: "cmd"})
	require.NoError(t, err)
	assert.False(t, created)

	created, err = s.AcquireReviewLock(ctx, ReviewLock{RepositoryID: 2, PRNumber: 3, HeadSHA: "sha"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.AcquireReviewLock(ctx, ReviewLock{RepositoryID: 2, PRNumber: 3, HeadSHA: "sha"})
	require.NoError(t, err)
	assert.False(t, created)
	expiredAt := time.Now().UTC().Add(-time.Hour)
	created, err = s.AcquireReviewLock(ctx, ReviewLock{RepositoryID: 2, PRNumber: 4, HeadSHA: "sha", Holder: "old", ExpiresAt: expiredAt, AcquiredAt: expiredAt.Add(-time.Hour)})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.AcquireReviewLock(ctx, ReviewLock{RepositoryID: 2, PRNumber: 4, HeadSHA: "sha", Holder: "new", ExpiresAt: time.Now().UTC().Add(time.Hour), AcquiredAt: time.Now().UTC()})
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, s.ReleaseReviewLock(ctx, 2, 4, "sha", "new", time.Now().UTC()))
	created, err = s.AcquireReviewLock(ctx, ReviewLock{RepositoryID: 2, PRNumber: 4, HeadSHA: "sha", Holder: "after-release"})
	require.NoError(t, err)
	assert.True(t, created)

	require.NoError(t, s.Close())
	require.Error(t, s.Health(ctx))
}

func TestContainerErrorClassification(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantDocker    bool
		wantTransient bool
	}{
		{name: "nil", err: nil},
		{name: "docker unavailable", err: errors.New("cannot connect to Docker daemon"), wantDocker: true},
		{name: "container runtime unavailable", err: errors.New("container runtime is not reachable"), wantDocker: true},
		{name: "context canceled", err: context.Canceled, wantDocker: true},
		{name: "unexpected EOF", err: errors.New("create container: EOF"), wantTransient: true},
		{name: "connection reset", err: errors.New("read tcp: connection reset by peer"), wantTransient: true},
		{name: "connection refused", err: errors.New("dial unix docker.sock: connection refused"), wantDocker: true, wantTransient: true},
		{name: "sql assertion failure", err: errors.New("duplicate key value violates unique constraint")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantDocker, isDockerUnavailable(tt.err))
			assert.Equal(t, tt.wantTransient, isTransientContainerStartError(tt.err))
		})
	}
}

func newMigratedPostgresDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db := newPostgresDB(t, ctx)
	require.NoError(t, ApplyMigrations(ctx, db))
	return db
}

func newPostgresDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	var container *postgres.PostgresContainer
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		container, err = postgres.Run(
			ctx,
			"postgres:16-alpine",
			postgres.WithDatabase("herd"),
			postgres.WithUsername("herd"),
			postgres.WithPassword("herd"),
			testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
		)
		if err == nil || !isTransientContainerStartError(err) || attempt == 3 {
			break
		}
		time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
	}
	if err != nil {
		if isDockerUnavailable(err) {
			t.Skipf("skipping Postgres integration test: %v", err)
		}
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		require.NoError(t, testcontainers.TerminateContainer(container))
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})
	require.NoError(t, db.PingContext(ctx))
	return db
}

func seedRepository(t *testing.T, ctx context.Context, s *PostgresStore, db *sql.DB) int64 {
	t.Helper()
	require.NoError(t, s.UpsertInstallation(ctx, Installation{
		ID:           1001,
		AccountLogin: "octo",
		AccountID:    2002,
		TargetType:   "Organization",
		Events:       []string{"issue_comment", "pull_request"},
	}))
	repo, err := s.UpsertRepository(ctx, Repository{
		GitHubID:       3003,
		InstallationID: 1001,
		Owner:          "octo",
		Name:           "repo",
		DefaultBranch:  "main",
	})
	require.NoError(t, err)
	require.NotZero(t, repo.ID)
	var repoID int64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM repositories WHERE owner = $1 AND name = $2", "octo", "repo").Scan(&repoID))
	return repoID
}

func isDockerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "docker") || strings.Contains(message, "container runtime") || strings.Contains(message, "cannot connect") {
		return true
	}
	return errors.Is(err, context.Canceled)
}

func isTransientContainerStartError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "eof") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "connection refused")
}
