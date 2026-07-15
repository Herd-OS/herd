package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
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

func (s *PostgresStore) GetWebhookDelivery(ctx context.Context, deliveryID string) (WebhookDelivery, error) {
	var d WebhookDelivery
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, delivery_id, event, action, payload_hash, status, error, metadata, received_at, processed_at
		FROM webhook_deliveries
		WHERE delivery_id = $1`, deliveryID).Scan(
		&d.ID, &d.DeliveryID, &d.Event, &d.Action, &d.PayloadHash, &d.Status, &d.Error, &metadata, &d.ReceivedAt, &d.ProcessedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookDelivery{}, ErrNotFound
	}
	if err != nil {
		return WebhookDelivery{}, err
	}
	d.Metadata = json.RawMessage(metadata)
	return d, nil
}

func (s *PostgresStore) UpdateWebhookDeliveryStatus(ctx context.Context, deliveryID string, status string, errorMessage string, processedAt *time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status = $2, error = $3, processed_at = $4
		WHERE delivery_id = $1`, deliveryID, status, errorMessage, processedAt)
	if err != nil {
		return err
	}
	return requireAffected(result)
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

func (s *PostgresStore) UpsertRepository(ctx context.Context, r Repository) (Repository, error) {
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO repositories (github_id, installation_id, owner, name, default_branch, private, registered_at, updated_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (owner, name) DO UPDATE SET
			github_id = EXCLUDED.github_id,
			installation_id = EXCLUDED.installation_id,
			default_branch = EXCLUDED.default_branch,
			private = EXCLUDED.private,
			updated_at = EXCLUDED.updated_at,
			metadata = EXCLUDED.metadata
		RETURNING id`,
		r.GitHubID, r.InstallationID, r.Owner, r.Name, r.DefaultBranch, r.Private, timeOrNow(r.RegisteredAt), timeOrNow(r.UpdatedAt), metadataOrEmpty(r.Metadata)).Scan(&r.ID)
	if err != nil {
		return Repository{}, err
	}
	return r, nil
}

func (s *PostgresStore) GetRepository(ctx context.Context, owner string, name string) (Repository, error) {
	var r Repository
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, github_id, installation_id, owner, name, default_branch, private, registered_at, updated_at, metadata
		FROM repositories
		WHERE owner = $1 AND name = $2`, owner, name).Scan(
		&r.ID, &r.GitHubID, &r.InstallationID, &r.Owner, &r.Name, &r.DefaultBranch, &r.Private, &r.RegisteredAt, &r.UpdatedAt, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return Repository{}, ErrNotFound
	}
	if err != nil {
		return Repository{}, err
	}
	r.Metadata = json.RawMessage(metadata)
	return r, nil
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

func (s *PostgresStore) GetRunnerBootstrapTokenByHash(ctx context.Context, tokenHash string) (RunnerBootstrapToken, error) {
	var token RunnerBootstrapToken
	err := s.db.QueryRowContext(ctx, `
		SELECT id, repository_id, token_hash, created_at, expires_at, revoked_at, revoked_reason, used_at
		FROM runner_bootstrap_tokens
		WHERE token_hash = $1
		ORDER BY id DESC
		LIMIT 1`, tokenHash).Scan(
		&token.ID, &token.RepositoryID, &token.TokenHash, &token.CreatedAt, &token.ExpiresAt, &token.RevokedAt, &token.RevokedReason, &token.UsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerBootstrapToken{}, ErrNotFound
	}
	if err != nil {
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

func (s *PostgresStore) ListReconcileJobs(ctx context.Context, updatedBefore time.Time, limit int) ([]ReconcileJob, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, j.job_id, j.repository_id, j.installation_id, j.pr_number, j.head_sha, j.base_sha, j.status, j.worker_branch, j.metadata, j.created_at, j.updated_at,
		       r.id, r.github_id, r.installation_id, r.owner, r.name, r.default_branch, r.private, r.registered_at, r.updated_at, r.metadata,
		       COUNT(jr.id)
		FROM jobs j
		JOIN repositories r ON r.id = j.repository_id
		LEFT JOIN job_results jr ON jr.job_id = j.job_id
		WHERE j.status IN ('dispatching', 'dispatched', 'queued', 'in_progress', 'started')
		  AND j.updated_at < $1
		GROUP BY j.id, r.id
		ORDER BY j.updated_at ASC
		LIMIT $2`, updatedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ReconcileJob
	for rows.Next() {
		var item ReconcileJob
		var jobMetadata, repoMetadata []byte
		if err := rows.Scan(
			&item.Job.ID, &item.Job.JobID, &item.Job.RepositoryID, &item.Job.InstallationID, &item.Job.PRNumber, &item.Job.HeadSHA, &item.Job.BaseSHA, &item.Job.Status, &item.Job.WorkerBranch, &jobMetadata, &item.Job.CreatedAt, &item.Job.UpdatedAt,
			&item.Repository.ID, &item.Repository.GitHubID, &item.Repository.InstallationID, &item.Repository.Owner, &item.Repository.Name, &item.Repository.DefaultBranch, &item.Repository.Private, &item.Repository.RegisteredAt, &item.Repository.UpdatedAt, &repoMetadata,
			&item.ResultCount,
		); err != nil {
			return nil, err
		}
		item.Job.Metadata = json.RawMessage(jobMetadata)
		item.Repository.Metadata = json.RawMessage(repoMetadata)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateJobStatus(ctx context.Context, jobID string, status string, metadata json.RawMessage, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE jobs
		SET status = $2, metadata = $3, updated_at = $4
		WHERE job_id = $1`, jobID, status, metadataOrEmpty(metadata), timeOrNow(updatedAt))
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (s *PostgresStore) AcquireIdempotencyKey(ctx context.Context, key IdempotencyKey) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO idempotency_keys (key, scope, status, result_ref, expires_at, metadata, created_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (key) DO UPDATE SET
			scope = EXCLUDED.scope,
			status = EXCLUDED.status,
			result_ref = EXCLUDED.result_ref,
			expires_at = EXCLUDED.expires_at,
			metadata = EXCLUDED.metadata,
			created_at = EXCLUDED.created_at,
			completed_at = EXCLUDED.completed_at
		WHERE idempotency_keys.status <> 'completed'
		  AND idempotency_keys.expires_at IS NOT NULL
		  AND idempotency_keys.expires_at < now()`,
		key.Key, key.Scope, key.Status, key.ResultRef, key.ExpiresAt, metadataOrEmpty(key.Metadata), timeOrNow(key.CreatedAt), key.CompletedAt)
	return createdFromResult(result, err)
}

func (s *PostgresStore) GetIdempotencyKey(ctx context.Context, key string) (IdempotencyKey, error) {
	var record IdempotencyKey
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT key, scope, status, result_ref, expires_at, metadata, created_at, completed_at
		FROM idempotency_keys
		WHERE key = $1`, key).Scan(
		&record.Key, &record.Scope, &record.Status, &record.ResultRef, &record.ExpiresAt, &metadata, &record.CreatedAt, &record.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return IdempotencyKey{}, ErrNotFound
	}
	if err != nil {
		return IdempotencyKey{}, err
	}
	record.Metadata = json.RawMessage(metadata)
	return record, nil
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

func (s *PostgresStore) FailIdempotencyKey(ctx context.Context, key string, errorMessage string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET status = 'failed', result_ref = $2, completed_at = now()
		WHERE key = $1 AND status <> 'completed'`, key, errorMessage)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	record, err := s.GetIdempotencyKey(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if record.Status == "completed" {
		return nil
	}
	return ErrNotFound
}

func (s *PostgresStore) ListStartedIdempotencyKeys(ctx context.Context, scope string, createdBefore time.Time, limit int) ([]IdempotencyKey, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, scope, status, result_ref, expires_at, metadata, created_at, completed_at
		FROM idempotency_keys
		WHERE scope = $1 AND status = 'started' AND created_at < $2
		ORDER BY created_at ASC
		LIMIT $3`, scope, createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []IdempotencyKey
	for rows.Next() {
		var record IdempotencyKey
		var metadata []byte
		if err := rows.Scan(&record.Key, &record.Scope, &record.Status, &record.ResultRef, &record.ExpiresAt, &metadata, &record.CreatedAt, &record.CompletedAt); err != nil {
			return nil, err
		}
		record.Metadata = json.RawMessage(metadata)
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RecordGitHubMutationAttempt(ctx context.Context, a GitHubMutationAttempt) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO github_mutation_attempts (idempotency_key, repository_id, mutation_type, status, request, response, error, created_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		a.IdempotencyKey, nullableInt64(a.RepositoryID), a.MutationType, defaultString(a.Status, "started"), metadataOrEmpty(a.Request), metadataOrEmpty(a.Response), a.Error, timeOrNow(a.CreatedAt), a.CompletedAt)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrAlreadyExists
	}
	return nil
}

func (s *PostgresStore) CompleteGitHubMutationAttempt(ctx context.Context, idempotencyKey string, status string, response json.RawMessage, errorMessage string, completedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE github_mutation_attempts
		SET status = $2, response = $3, error = $4, completed_at = $5
		WHERE idempotency_key = $1`,
		idempotencyKey, status, metadataOrEmpty(response), errorMessage, timeOrNow(completedAt))
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (s *PostgresStore) GetGitHubMutationAttempt(ctx context.Context, idempotencyKey string) (GitHubMutationAttempt, error) {
	var attempt GitHubMutationAttempt
	var request, response []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, idempotency_key, COALESCE(repository_id, 0), mutation_type, status, request, response, error, created_at, completed_at
		FROM github_mutation_attempts
		WHERE idempotency_key = $1`, idempotencyKey).Scan(
		&attempt.ID, &attempt.IdempotencyKey, &attempt.RepositoryID, &attempt.MutationType, &attempt.Status, &request, &response, &attempt.Error, &attempt.CreatedAt, &attempt.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubMutationAttempt{}, ErrNotFound
	}
	if err != nil {
		return GitHubMutationAttempt{}, err
	}
	attempt.Request = json.RawMessage(request)
	attempt.Response = json.RawMessage(response)
	return attempt, nil
}

func (s *PostgresStore) ListStartedGitHubMutationAttempts(ctx context.Context, createdBefore time.Time, limit int) ([]GitHubMutationAttempt, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, idempotency_key, COALESCE(repository_id, 0), mutation_type, status, request, response, error, created_at, completed_at
		FROM github_mutation_attempts
		WHERE status IN ('call_started', 'repair_required', 'started') AND created_at < $1
		ORDER BY created_at ASC
		LIMIT $2`, createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GitHubMutationAttempt
	for rows.Next() {
		var attempt GitHubMutationAttempt
		var request, response []byte
		if err := rows.Scan(&attempt.ID, &attempt.IdempotencyKey, &attempt.RepositoryID, &attempt.MutationType, &attempt.Status, &request, &response, &attempt.Error, &attempt.CreatedAt, &attempt.CompletedAt); err != nil {
			return nil, err
		}
		attempt.Request = json.RawMessage(request)
		attempt.Response = json.RawMessage(response)
		out = append(out, attempt)
	}
	return out, rows.Err()
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

func (s *PostgresStore) ListReconcileReviewStates(ctx context.Context, updatedBefore time.Time, limit int) ([]ReconcileReviewState, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.id, rs.repository_id, rs.pr_number, rs.head_sha, rs.status, rs.last_job_id, rs.metadata, rs.updated_at,
		       r.id, r.github_id, r.installation_id, r.owner, r.name, r.default_branch, r.private, r.registered_at, r.updated_at, r.metadata
		FROM review_states rs
		JOIN repositories r ON r.id = rs.repository_id
		WHERE rs.status <> 'abandoned' AND rs.updated_at < $1
		ORDER BY rs.updated_at ASC
		LIMIT $2`, updatedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ReconcileReviewState
	for rows.Next() {
		var item ReconcileReviewState
		var stateMetadata, repoMetadata []byte
		if err := rows.Scan(
			&item.State.ID, &item.State.RepositoryID, &item.State.PRNumber, &item.State.HeadSHA, &item.State.Status, &item.State.LastJobID, &stateMetadata, &item.State.UpdatedAt,
			&item.Repository.ID, &item.Repository.GitHubID, &item.Repository.InstallationID, &item.Repository.Owner, &item.Repository.Name, &item.Repository.DefaultBranch, &item.Repository.Private, &item.Repository.RegisteredAt, &item.Repository.UpdatedAt, &repoMetadata,
		); err != nil {
			return nil, err
		}
		item.State.Metadata = json.RawMessage(stateMetadata)
		item.Repository.Metadata = json.RawMessage(repoMetadata)
		out = append(out, item)
	}
	return out, rows.Err()
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

func (s *PostgresStore) GetCommandRecord(ctx context.Context, repoID int64, commentID int64, commandKey string) (CommandRecord, error) {
	var record CommandRecord
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, repository_id, comment_id, command_key, command_name, actor, status, metadata, created_at
		FROM command_records
		WHERE repository_id = $1 AND comment_id = $2 AND command_key = $3`,
		repoID, commentID, commandKey).Scan(
		&record.ID, &record.RepositoryID, &record.CommentID, &record.CommandKey, &record.CommandName, &record.Actor, &record.Status, &metadata, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandRecord{}, ErrNotFound
	}
	if err != nil {
		return CommandRecord{}, err
	}
	record.Metadata = json.RawMessage(metadata)
	return record, nil
}

func (s *PostgresStore) ListReconcileCommands(ctx context.Context, createdBefore time.Time, limit int) ([]ReconcileCommand, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.repository_id, c.comment_id, c.command_key, c.command_name, c.actor, c.status, c.metadata, c.created_at,
		       r.id, r.github_id, r.installation_id, r.owner, r.name, r.default_branch, r.private, r.registered_at, r.updated_at, r.metadata,
		       ik.key, ik.scope, ik.status, ik.result_ref, ik.expires_at, ik.metadata, ik.created_at, ik.completed_at
		FROM command_records c
		JOIN repositories r ON r.id = c.repository_id
		LEFT JOIN idempotency_keys ik ON ik.key = ('repo:' || c.repository_id || ':comment:' || c.comment_id || ':command:' || c.command_key)
		WHERE c.status IN ('acknowledged', 'retry_needed') AND c.created_at < $1
		ORDER BY c.created_at ASC
		LIMIT $2`, createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ReconcileCommand
	for rows.Next() {
		var item ReconcileCommand
		var commandMetadata, repoMetadata []byte
		var key, scope, keyStatus, resultRef sql.NullString
		var expiresAt, keyCreatedAt, completedAt sql.NullTime
		var keyMetadata []byte
		if err := rows.Scan(
			&item.Command.ID, &item.Command.RepositoryID, &item.Command.CommentID, &item.Command.CommandKey, &item.Command.CommandName, &item.Command.Actor, &item.Command.Status, &commandMetadata, &item.Command.CreatedAt,
			&item.Repository.ID, &item.Repository.GitHubID, &item.Repository.InstallationID, &item.Repository.Owner, &item.Repository.Name, &item.Repository.DefaultBranch, &item.Repository.Private, &item.Repository.RegisteredAt, &item.Repository.UpdatedAt, &repoMetadata,
			&key, &scope, &keyStatus, &resultRef, &expiresAt, &keyMetadata, &keyCreatedAt, &completedAt,
		); err != nil {
			return nil, err
		}
		item.Command.Metadata = json.RawMessage(commandMetadata)
		item.Repository.Metadata = json.RawMessage(repoMetadata)
		item.IdempotencyKey = commandIdempotencyKey(item.Command.RepositoryID, item.Command.CommentID, item.Command.CommandKey)
		if key.Valid {
			item.IdempotencySeen = true
			item.Idempotency = IdempotencyKey{
				Key:       key.String,
				Scope:     scope.String,
				Status:    keyStatus.String,
				ResultRef: resultRef.String,
				Metadata:  json.RawMessage(keyMetadata),
			}
			if expiresAt.Valid {
				item.Idempotency.ExpiresAt = &expiresAt.Time
			}
			if keyCreatedAt.Valid {
				item.Idempotency.CreatedAt = keyCreatedAt.Time
			}
			if completedAt.Valid {
				item.Idempotency.CompletedAt = &completedAt.Time
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateCommandStatus(ctx context.Context, repoID int64, commentID int64, commandKey string, status string, metadata json.RawMessage) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE command_records
		SET status = $4, metadata = $5
		WHERE repository_id = $1 AND comment_id = $2 AND command_key = $3`,
		repoID, commentID, commandKey, status, metadataOrEmpty(metadata))
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// AcquireReviewLock creates an active lock for a repository, PR, and head SHA.
func (s *PostgresStore) AcquireReviewLock(ctx context.Context, lock ReviewLock) (bool, error) {
	_, err := s.db.ExecContext(ctx, `
		UPDATE review_locks
		SET released_at = $4
		WHERE repository_id = $1
			AND pr_number = $2
			AND head_sha = $3
			AND released_at IS NULL
			AND expires_at <= $4`,
		lock.RepositoryID, lock.PRNumber, lock.HeadSHA, timeOrNow(lock.AcquiredAt))
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO review_locks (repository_id, pr_number, head_sha, holder, expires_at, acquired_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (repository_id, pr_number, head_sha) WHERE released_at IS NULL DO NOTHING`,
		lock.RepositoryID, lock.PRNumber, lock.HeadSHA, lock.Holder, lock.ExpiresAt, timeOrNow(lock.AcquiredAt))
	return createdFromResult(result, err)
}

func (s *PostgresStore) ReleaseReviewLock(ctx context.Context, repoID int64, prNumber int, headSHA string, holder string, releasedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE review_locks
		SET released_at = $5
		WHERE repository_id = $1
			AND pr_number = $2
			AND head_sha = $3
			AND holder = $4
			AND released_at IS NULL`,
		repoID, prNumber, headSHA, holder, timeOrNow(releasedAt))
	if err != nil {
		return err
	}
	return requireAffected(result)
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

func defaultString(v string, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func commandIdempotencyKey(repoID int64, commentID int64, commandKey string) string {
	return "repo:" + strconv.FormatInt(repoID, 10) + ":comment:" + strconv.FormatInt(commentID, 10) + ":command:" + commandKey
}
