package runner

import "embed"

//go:embed Dockerfile.runner entrypoint.sh docker-compose.herd.yml.tmpl .env.herd.example
var FS embed.FS
