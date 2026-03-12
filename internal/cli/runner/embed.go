package runner

import "embed"

//go:embed Dockerfile.runner entrypoint.sh docker-compose.herd.yml.tmpl .env.example
var FS embed.FS
