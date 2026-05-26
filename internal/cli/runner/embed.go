package runner

import "embed"

//go:embed Dockerfile.herd_runner.tmpl docker-compose.herd.yml.tmpl .env.herd.example
var FS embed.FS
