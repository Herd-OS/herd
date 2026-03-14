#!/bin/bash
set -euo pipefail

cleanup() {
  echo "Removing runner..."
  ./config.sh remove --token "$(get_token)"
  exit 0
}
trap cleanup SIGTERM SIGINT

get_token() {
  curl -s -X POST \
    -H "Authorization: token ${GITHUB_TOKEN}" \
    "https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/actions/runners/registration-token" \
    | jq -r .token
}

REPO_OWNER=$(echo "$REPO_URL" | sed -E 's|.*/([^/]+)/([^/]+)$|\1|')
REPO_NAME=$(echo "$REPO_URL" | sed -E 's|.*/([^/]+)/([^/]+)$|\2|')

# Remove stale config from previous run (ephemeral runners leave config behind on restart)
if [ -f .runner ]; then
  ./config.sh remove --token "$(get_token)" || true
fi

./config.sh \
  --url "$REPO_URL" \
  --token "$(get_token)" \
  --name "${RUNNER_NAME:-$(hostname)}" \
  --labels "${RUNNER_LABELS:-herd-worker}" \
  --ephemeral \
  --unattended

exec ./run.sh
