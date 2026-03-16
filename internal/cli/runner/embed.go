package runner

import "embed"

//go:embed Dockerfile.herd_runner_base Dockerfile.herd_runner.tmpl entrypoint.herd.sh docker-compose.herd.yml.tmpl .env.herd.example
var FS embed.FS
