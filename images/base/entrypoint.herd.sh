#!/bin/bash
set -euo pipefail

# When started as root (the default for this image), optionally remap the
# `runner` user to a caller-specified UID/GID and then drop privileges via
# gosu before doing anything else. This lets operators match the host's
# UID/GID for bind-mounted volumes (e.g. TrueNAS's `apps` user) without
# rebuilding the image — just set RUNNER_UID / RUNNER_GID in the runner's
# environment.
#
# Backward compat: if a wrapper Dockerfile ends with `USER runner`, this
# script starts non-root and skips the remap path entirely, preserving the
# previous behavior.
if [ "$(id -u)" = "0" ]; then
  # 1001 is the historical default — see images/base/Dockerfile for why.
  target_uid="${RUNNER_UID:-1001}"
  target_gid="${RUNNER_GID:-1001}"
  current_uid="$(id -u runner)"
  current_gid="$(id -g runner)"

  if [ "$target_uid" = "0" ] || [ "$target_gid" = "0" ]; then
    echo "RUNNER_UID/RUNNER_GID must not be 0 — the GitHub Actions runner refuses to run as root." >&2
    exit 1
  fi

  if [ "$target_uid" != "$current_uid" ] || [ "$target_gid" != "$current_gid" ]; then
    echo "Remapping runner from ${current_uid}:${current_gid} to ${target_uid}:${target_gid} (RUNNER_UID / RUNNER_GID)..."
    groupmod -o -g "$target_gid" runner
    usermod  -o -u "$target_uid" -g "$target_gid" runner
    chown -R "${target_uid}:${target_gid}" /runner /opt/herd /home/runner
  fi

  exec gosu runner:runner "$0" "$@"
fi

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

# Keep the Codex OAuth chain warm if subscription auth is in use. Detected by
# the presence of ~/.codex/auth.json, which `codex login` writes into the
# persistent codex-auth volume. Skipped for Enterprise (CODEX_ACCESS_TOKEN
# only — no refresh needed) and API-key (no expiry) setups, which have no
# auth.json.
if [ -f /home/runner/.codex/auth.json ]; then
  # Log to /opt/herd/ rather than /var/log/: the runner user owns /opt/herd
  # (chowned at image build time and re-chowned by the RUNNER_UID remap
  # earlier in this script), but does not own /var/log. A redirect into
  # /var/log fails with "Permission denied" and the keepalive process never
  # actually starts, so the OAuth chain lapses silently after ~8 days idle.
  /opt/herd/bin/herd codex keepalive-loop \
    >>/opt/herd/herd-codex-keepalive.log 2>&1 &
fi

exec ./run.sh
