package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
)

// PostgresStore stores control-plane state in PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

type postgresOptions struct {
	migrateOnStart bool
}

// PostgresOption configures NewPostgresStore.
type PostgresOption func(*postgresOptions)

// WithMigrateOnStart applies embedded migrations during construction. Most
// production deployments should run migrations out-of-band and let
// NewPostgresStore validate them; this option is for controlled single-owner
// deployments and tests.
func WithMigrateOnStart() PostgresOption {
	return func(o *postgresOptions) {
		o.migrateOnStart = true
	}
}

// NewPostgresStore validates that migrations are applied unless
// WithMigrateOnStart is supplied.
func NewPostgresStore(ctx context.Context, db *sql.DB, opts ...PostgresOption) (*PostgresStore, error) {
	options := postgresOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	if options.migrateOnStart {
		if err := ApplyMigrations(ctx, db); err != nil {
			return nil, err
		}
	} else if err := ValidateMigrations(ctx, db); err != nil {
		return nil, err
	}
	return &PostgresStore{db: db}, nil
}

// OpenPostgresStore opens a lib/pq connection and validates migrations.
func OpenPostgresStore(ctx context.Context, dsn string, opts ...PostgresOption) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	store, err := NewPostgresStore(ctx, db, opts...)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) Health(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) RecordWebhookDelivery(ctx context.Context, d WebhookDelivery) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (delivery_id, event, action, payload_hash, status, error, metadata, received_at, processed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (delivery_id) DO NOTHING`,
		d.DeliveryID, d.Event, d.Action, d.PayloadHash, d.Status, d.Error, metadataOrEmpty(d.Metadata), timeOrNow(d.ReceivedAt), d.ProcessedAt)
	return createdFromResult(result, err)
}

func (s *PostgresStore) UpsertInstallation(ctx context.Context, i Installation) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_installations (id, account_login, account_id, target_type, permissions, events, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			account_login = EXCLUDED.account_login,
			account_id = EXCLUDED.account_id,
			target_type = EXCLUDED.target_type,
			permissions = EXCLUDED.permissions,
			events = EXCLUDED.events,
			updated_at = EXCLUDED.updated_at`,
		i.ID, i.AccountLogin, i.AccountID, i.TargetType, metadataOrEmpty(i.Permissions), pq.Array(i.Events), timeOrNow(i.CreatedAt), timeOrNow(i.UpdatedAt))
	return err
}

func (s *PostgresStore) UpsertRepository(ctx context.Context, r Repository) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO repositories (github_id, installation_id, owner, name, default_branch, private, registered_at, updated_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (owner, name) DO UPDATE SET
			github_id = EXCLUDED.github_id,
			installation_id = EXCLUDED.installation_id,
			default_branch = EXCLUDED.default_branch,
			private = EXCLUDED.private,
			updated_at = EXCLUDED.updated_at,
			metadata = EXCLUDED.metadata`,
		r.GitHubID, r.InstallationID, r.Owner, r.Name, r.DefaultBranch, r.Private, timeOrNow(r.RegisteredAt), timeOrNow(r.UpdatedAt), metadataOrEmpty(r.Metadata))
	return err
}

func (s *PostgresStore) CreateRegistrationAttempt(ctx context.Context, a RegistrationAttempt) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO registration_attempts (repository_id, installation_id, owner, name, status, error, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		nullableInt64(a.RepositoryID), nullableInt64(a.InstallationID), a.Owner, a.Name, a.Status, a.Error, metadataOrEmpty(a.Metadata), timeOrNow(a.CreatedAt))
	return err
}

func (s *PostgresStore) CreateRunnerBootstrapToken(ctx context.Context, t RunnerBootstrapToken) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runner_bootstrap_tokens (repository_id, token_hash, created_at, expires_at, revoked_at, revoked_reason, used_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.RepositoryID, t.TokenHash, timeOrNow(t.CreatedAt), t.ExpiresAt, t.RevokedAt, t.RevokedReason, t.UsedAt)
	return err
}

func (s *PostgresStore) RotateRunnerBootstrapToken(ctx context.Context, repoID int64, tokenHash string) (RunnerBootstrapToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunnerBootstrapToken{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE runner_bootstrap_tokens
		SET revoked_at = $1, revoked_reason = 'rotated'
		WHERE repository_id = $2 AND revoked_at IS NULL`, now, repoID); err != nil {
		return RunnerBootstrapToken{}, err
	}
	token := RunnerBootstrapToken{
		RepositoryID: repoID,
		TokenHash:    tokenHash,
		CreatedAt:    now,
		ExpiresAt:    now.Add(24 * time.Hour),
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO runner_bootstrap_tokens (repository_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, token.RepositoryID, token.TokenHash, token.CreatedAt, token.ExpiresAt).Scan(&token.ID); err != nil {
		return RunnerBootstrapToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunnerBootstrapToken{}, err
	}
	return token, nil
}

func (s *PostgresStore) RevokeRunnerBootstrapToken(ctx context.Context, tokenID int64, reason string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE runner_bootstrap_tokens
		SET revoked_at = COALESCE(revoked_at, now()), revoked_reason = $2
		WHERE id = $1`, tokenID, reason)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (s *PostgresStore) MarkRunnerBootstrapTokenUsed(ctx context.Context, tokenID int64, usedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, "UPDATE runner_bootstrap_tokens SET used_at = $2 WHERE id = $1", tokenID, usedAt)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (s *PostgresStore) CreateJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (job_id, repository_id, installation_id, pr_number, head_sha, base_sha, status, worker_branch, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		j.JobID, j.RepositoryID, j.InstallationID, j.PRNumber, j.HeadSHA, j.BaseSHA, j.Status, j.WorkerBranch, metadataOrEmpty(j.Metadata), timeOrNow(j.CreatedAt), timeOrNow(j.UpdatedAt))
	return err
}

func (s *PostgresStore) GetJob(ctx context.Context, jobID string) (Job, error) {
	var j Job
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, job_id, repository_id, installation_id, pr_number, head_sha, base_sha, status, worker_branch, metadata, created_at, updated_at
		FROM jobs WHERE job_id = $1`, jobID).Scan(
		&j.ID, &j.JobID, &j.RepositoryID, &j.InstallationID, &j.PRNumber, &j.HeadSHA, &j.BaseSHA, &j.Status, &j.WorkerBranch, &metadata, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	j.Metadata = json.RawMessage(metadata)
	return j, nil
}

func (s *PostgresStore) RecordJobResult(ctx context.Context, r JobResult) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO job_results (job_id, idempotency_key, status, result_ref, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (job_id, idempotency_key) DO NOTHING`,
		r.JobID, r.IdempotencyKey, r.Status, r.ResultRef, metadataOrEmpty(r.Metadata), timeOrNow(r.CreatedAt))
	return createdFromResult(result, err)
}

func (s *PostgresStore) AcquireIdempotencyKey(ctx context.Context, key IdempotencyKey) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO idempotency_keys (key, scope, status, result_ref, expires_at, metadata, created_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (key) DO NOTHING`,
		key.Key, key.Scope, key.Status, key.ResultRef, key.ExpiresAt, metadataOrEmpty(key.Metadata), timeOrNow(key.CreatedAt), key.CompletedAt)
	return createdFromResult(result, err)
}

func (s *PostgresStore) CompleteIdempotencyKey(ctx context.Context, key string, resultRef string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET status = 'completed', result_ref = $2, completed_at = now()
		WHERE key = $1`, key, resultRef)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (s *PostgresStore) SetReviewState(ctx context.Context, state ReviewState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_states (repository_id, pr_number, head_sha, status, last_job_id, metadata, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (repository_id, pr_number, head_sha) DO UPDATE SET
			status = EXCLUDED.status,
			last_job_id = EXCLUDED.last_job_id,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at`,
		state.RepositoryID, state.PRNumber, state.HeadSHA, state.Status, state.LastJobID, metadataOrEmpty(state.Metadata), timeOrNow(state.UpdatedAt))
	return err
}

func (s *PostgresStore) GetReviewState(ctx context.Context, repoID int64, prNumber int, headSHA string) (ReviewState, error) {
	var state ReviewState
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, repository_id, pr_number, head_sha, status, last_job_id, metadata, updated_at
		FROM review_states
		WHERE repository_id = $1 AND pr_number = $2 AND head_sha = $3`, repoID, prNumber, headSHA).Scan(
		&state.ID, &state.RepositoryID, &state.PRNumber, &state.HeadSHA, &state.Status, &state.LastJobID, &metadata, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewState{}, ErrNotFound
	}
	if err != nil {
		return ReviewState{}, err
	}
	state.Metadata = json.RawMessage(metadata)
	return state, nil
}

// RecordCommand records a command once per repository, comment, and command key.
func (s *PostgresStore) RecordCommand(ctx context.Context, c CommandRecord) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO command_records (repository_id, comment_id, command_key, command_name, actor, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (repository_id, comment_id, command_key) DO NOTHING`,
		c.RepositoryID, c.CommentID, c.CommandKey, c.CommandName, c.Actor, c.Status, metadataOrEmpty(c.Metadata), timeOrNow(c.CreatedAt))
	return createdFromResult(result, err)
}

// AcquireReviewLock creates an active lock for a repository, PR, and head SHA.
func (s *PostgresStore) AcquireReviewLock(ctx context.Context, lock ReviewLock) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO review_locks (repository_id, pr_number, head_sha, holder, expires_at, acquired_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (repository_id, pr_number, head_sha) WHERE released_at IS NULL DO NOTHING`,
		lock.RepositoryID, lock.PRNumber, lock.HeadSHA, lock.Holder, lock.ExpiresAt, timeOrNow(lock.AcquiredAt))
	return createdFromResult(result, err)
}

func createdFromResult(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func requireAffected(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
