ALTER TABLE repositories DROP CONSTRAINT IF EXISTS repositories_github_id_key;
DROP INDEX IF EXISTS repositories_github_id_key;
ALTER TABLE repositories ALTER COLUMN github_id DROP NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS repositories_github_id_key ON repositories (github_id) WHERE github_id IS NOT NULL;
