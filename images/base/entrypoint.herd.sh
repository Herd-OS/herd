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

normalize_control_plane_url() {
  local url="${HERD_CONTROL_PLANE_URL:-https://api.herd-os.com}"
  url="${url%"${url##*[![:space:]]}"}"
  url="${url#"${url%%[![:space:]]*}"}"
  url="${url%/}"
  if [[ ! "$url" =~ ^https?://[^/@?#[:space:]]+(:[0-9]+)?(/[^?#[:space:]]*)?$ ]]; then
    echo "HERD_CONTROL_PLANE_URL must be an absolute http or https URL without userinfo, query, or fragment" >&2
    exit 1
  fi
  printf '%s\n' "$url"
}

require_runner_bootstrap_token() {
  if [ -z "${HERD_RUNNER_BOOTSTRAP_TOKEN:-}" ]; then
    echo "HERD_RUNNER_BOOTSTRAP_TOKEN is required for runner registration. Run 'herd init' again to register this repository with the Herd control plane." >&2
    exit 1
  fi
}

get_token() {
  local response token
  response="$(jq -n \
    --arg repository "${REPO_OWNER}/${REPO_NAME}" \
    --arg owner "$REPO_OWNER" \
    --arg name "$REPO_NAME" \
    --arg runner_name "$RUNNER_NAME_RESOLVED" \
    --arg labels "$RUNNER_LABELS_RESOLVED" \
    --arg request_nonce "$HERD_RUNNER_REQUEST_NONCE" \
    '{
      repository: $repository,
      owner: $owner,
      name: $name,
      runner_name: $runner_name,
      runner_labels: ($labels | split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(. != ""))),
      bootstrap_token: env.HERD_RUNNER_BOOTSTRAP_TOKEN,
      request_nonce: $request_nonce
    }' \
    | curl -fsSL -X POST \
      -H "Content-Type: application/json" \
      -H "Accept: application/json" \
      --data-binary @- \
      "${HERD_CONTROL_PLANE_URL_RESOLVED}/api/v1/runners/registration-token")"
  token="$(printf '%s' "$response" | jq -r '.token // empty')"
  if [ -z "$token" ] || [ "$token" = "null" ]; then
    echo "Herd control plane did not return a runner registration token" >&2
    exit 1
  fi
  printf '%s\n' "$token"
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
RUNNER_NAME_RESOLVED="${RUNNER_NAME:-$(hostname)}"
RUNNER_LABELS_RESOLVED="${RUNNER_LABELS:-herd-worker}"
HERD_RUNNER_REQUEST_NONCE="${HERD_RUNNER_REQUEST_NONCE:-$(date +%s)-${HOSTNAME:-runner}-$$}"
HERD_CONTROL_PLANE_URL_RESOLVED="$(normalize_control_plane_url)"
require_runner_bootstrap_token

# Remove stale config from previous run (ephemeral runners leave config behind on restart)
if [ -f .runner ]; then
  ./config.sh remove --token "$(get_token)" || rm -f .runner .credentials .credentials_rsaparams
fi

./config.sh \
  --url "$REPO_URL" \
  --token "$(get_token)" \
  --name "$RUNNER_NAME_RESOLVED" \
  --labels "$RUNNER_LABELS_RESOLVED" \
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
