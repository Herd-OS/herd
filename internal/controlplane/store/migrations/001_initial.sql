CREATE TABLE app_installations (
	id BIGINT PRIMARY KEY,
	account_login TEXT NOT NULL,
	account_id BIGINT NOT NULL,
	target_type TEXT NOT NULL,
	permissions JSONB NOT NULL DEFAULT '{}'::jsonb,
	events TEXT[] NOT NULL DEFAULT '{}'::text[],
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE repositories (
	id BIGSERIAL PRIMARY KEY,
	github_id BIGINT NOT NULL UNIQUE,
	installation_id BIGINT NOT NULL REFERENCES app_installations(id) ON DELETE CASCADE,
	owner TEXT NOT NULL,
	name TEXT NOT NULL,
	default_branch TEXT NOT NULL,
	private BOOLEAN NOT NULL DEFAULT false,
	registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	CONSTRAINT repositories_owner_name_key UNIQUE (owner, name)
);

CREATE INDEX repositories_installation_id_idx ON repositories (installation_id);

CREATE TABLE registration_attempts (
	id BIGSERIAL PRIMARY KEY,
	repository_id BIGINT REFERENCES repositories(id) ON DELETE SET NULL,
	installation_id BIGINT REFERENCES app_installations(id) ON DELETE SET NULL,
	owner TEXT NOT NULL,
	name TEXT NOT NULL,
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX registration_attempts_repository_id_idx ON registration_attempts (repository_id);

CREATE TABLE runner_bootstrap_tokens (
	id BIGSERIAL PRIMARY KEY,
	repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	token_hash TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at TIMESTAMPTZ NOT NULL,
	revoked_at TIMESTAMPTZ,
	revoked_reason TEXT NOT NULL DEFAULT '',
	used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX runner_bootstrap_tokens_active_hash_key
	ON runner_bootstrap_tokens (token_hash)
	WHERE revoked_at IS NULL;
CREATE INDEX runner_bootstrap_tokens_repository_id_idx ON runner_bootstrap_tokens (repository_id);

CREATE TABLE webhook_deliveries (
	id BIGSERIAL PRIMARY KEY,
	delivery_id TEXT NOT NULL UNIQUE,
	event TEXT NOT NULL,
	action TEXT NOT NULL DEFAULT '',
	payload_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	processed_at TIMESTAMPTZ
);

CREATE TABLE jobs (
	id BIGSERIAL PRIMARY KEY,
	job_id TEXT NOT NULL UNIQUE,
	repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	installation_id BIGINT NOT NULL REFERENCES app_installations(id) ON DELETE CASCADE,
	pr_number INTEGER NOT NULL,
	head_sha TEXT NOT NULL,
	base_sha TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	worker_branch TEXT NOT NULL DEFAULT '',
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX jobs_repository_pr_idx ON jobs (repository_id, pr_number);

CREATE TABLE job_results (
	id BIGSERIAL PRIMARY KEY,
	job_id TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
	idempotency_key TEXT NOT NULL,
	status TEXT NOT NULL,
	result_ref TEXT NOT NULL DEFAULT '',
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT job_results_job_id_idempotency_key UNIQUE (job_id, idempotency_key)
);

CREATE TABLE review_states (
	id BIGSERIAL PRIMARY KEY,
	repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	pr_number INTEGER NOT NULL,
	head_sha TEXT NOT NULL,
	status TEXT NOT NULL,
	last_job_id TEXT NOT NULL DEFAULT '',
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT review_states_repo_pr_head_key UNIQUE (repository_id, pr_number, head_sha)
);

CREATE TABLE review_locks (
	id BIGSERIAL PRIMARY KEY,
	repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	pr_number INTEGER NOT NULL,
	head_sha TEXT NOT NULL,
	holder TEXT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	released_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX review_locks_active_repo_pr_head_key
	ON review_locks (repository_id, pr_number, head_sha)
	WHERE released_at IS NULL;

CREATE TABLE command_records (
	id BIGSERIAL PRIMARY KEY,
	repository_id BIGINT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	comment_id BIGINT NOT NULL,
	command_key TEXT NOT NULL,
	command_name TEXT NOT NULL,
	actor TEXT NOT NULL,
	status TEXT NOT NULL,
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT command_records_repo_comment_command_key UNIQUE (repository_id, comment_id, command_key)
);

CREATE TABLE idempotency_keys (
	key TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	status TEXT NOT NULL,
	result_ref TEXT NOT NULL DEFAULT '',
	expires_at TIMESTAMPTZ,
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at TIMESTAMPTZ
);

CREATE TABLE github_mutation_attempts (
	id BIGSERIAL PRIMARY KEY,
	idempotency_key TEXT NOT NULL UNIQUE,
	repository_id BIGINT REFERENCES repositories(id) ON DELETE SET NULL,
	mutation_type TEXT NOT NULL,
	status TEXT NOT NULL,
	request JSONB NOT NULL DEFAULT '{}'::jsonb,
	response JSONB NOT NULL DEFAULT '{}'::jsonb,
	error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at TIMESTAMPTZ
);

CREATE TABLE audit_records (
	id BIGSERIAL PRIMARY KEY,
	repository_id BIGINT REFERENCES repositories(id) ON DELETE SET NULL,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
