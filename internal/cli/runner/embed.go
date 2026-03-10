package runner

import "embed"

//go:embed Dockerfile.runner entrypoint.sh docker-compose.herd.yml.tmpl
var FS embed.FS
