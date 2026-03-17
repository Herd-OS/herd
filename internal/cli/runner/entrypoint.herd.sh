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

# Install or update herd CLI
ARCH=$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')
if [ -n "${HERD_VERSION:-}" ] && [ "$HERD_VERSION" != "latest" ]; then
  HERD_URL="https://github.com/Herd-OS/herd/releases/download/${HERD_VERSION}/herd-linux-${ARCH}"
else
  HERD_URL="https://github.com/Herd-OS/herd/releases/latest/download/herd-linux-${ARCH}"
fi
echo "Installing herd from ${HERD_URL}..."
curl -fsSL "$HERD_URL" -o /opt/herd/bin/herd
chmod +x /opt/herd/bin/herd
echo "Installed herd $(herd --version 2>/dev/null || echo 'unknown')"

# Install or update Claude Code
echo "Installing Claude Code..."
npm config set prefix /home/runner/.npm-global 2>/dev/null
export PATH="/home/runner/.npm-global/bin:$PATH"
npm install -g @anthropic-ai/claude-code
echo "Installed claude $(claude --version 2>/dev/null || echo 'unknown')"

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
