CREATE UNIQUE INDEX runner_bootstrap_tokens_one_active_per_repository_key
	ON runner_bootstrap_tokens (repository_id)
	WHERE revoked_at IS NULL;
