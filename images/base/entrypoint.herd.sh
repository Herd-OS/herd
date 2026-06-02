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
npm config set prefix /home/runner/.npm-global || true
export PATH="/home/runner/.npm-global/bin:$PATH"
npm install -g --no-audit --no-fund @anthropic-ai/claude-code
echo "Installed claude $(claude --version 2>/dev/null || echo 'unknown')"

# Install or update OpenCode
echo "Installing OpenCode..."
npm install -g --no-audit --no-fund opencode-ai
# Install OpenCode OpenAI subscription auth plugin (kept on the same lifecycle
# as opencode to avoid version skew).
# TODO(verify): confirm plugin package name `opencode-openai-codex-auth` and that
# version 4.4.0 is correct; confirm npm-global install is the right mechanism.
npm install -g --no-audit --no-fund opencode-openai-codex-auth@4.4.0
# Install OpenCode Anthropic (Claude) subscription auth plugin — the Claude
# OAuth bridge — kept on the same lifecycle as opencode to avoid version skew.
# TODO(verify): confirm plugin package name `opencode-claude-auth` (it may be
# scoped) and that the pinned version below is correct; confirm npm-global
# install is the right mechanism. Upstream: griffinmartin/opencode-claude-auth.
npm install -g --no-audit --no-fund opencode-claude-auth@1.5.4
echo "Installed opencode $(opencode --version 2>/dev/null || echo 'unknown')"

REPO_OWNER=$(echo "$REPO_URL" | sed -E 's|.*/([^/]+)/([^/]+)$|\1|')
REPO_NAME=$(echo "$REPO_URL" | sed -E 's|.*/([^/]+)/([^/]+)$|\2|')

# Remove stale config from previous run (ephemeral runners leave config behind on restart)
if [ -f .runner ]; then
  ./config.sh remove --token "$(get_token)" || rm -f .runner .credentials .credentials_rsaparams
fi

./config.sh \
  --url "$REPO_URL" \
  --token "$(get_token)" \
  --name "${RUNNER_NAME:-$(hostname)}" \
  --labels "${RUNNER_LABELS:-herd-worker}" \
  --ephemeral \
  --unattended

exec ./run.sh
