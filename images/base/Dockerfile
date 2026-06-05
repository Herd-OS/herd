FROM ubuntu:24.04

ARG RUNNER_VERSION=2.333.1
ARG TARGETARCH

# Dependencies (Node 22 LTS required by Claude Code and OpenCode, libicu for
# GitHub Actions runner, gosu for the entrypoint's privilege drop).
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl jq ca-certificates git gh libicu-dev gosu \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# GitHub Actions runner
RUN ARCH=$(echo "$TARGETARCH" | sed 's/amd64/x64/' | sed 's/arm64/arm64/') \
    && mkdir /runner && cd /runner \
    && curl -sL "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-${ARCH}-${RUNNER_VERSION}.tar.gz" \
    | tar xz

RUN mkdir -p /opt/herd/bin /home/runner/.npm-global
ENV PATH="/home/runner/.npm-global/bin:/opt/herd/bin:${PATH}"

# Agent CLIs (baked at build time; previously installed in entrypoint.herd.sh).
# Pinned versions preserved from the old entrypoint.
RUN npm config set prefix /home/runner/.npm-global \
    && npm install -g --no-audit --no-fund @anthropic-ai/claude-code \
    && npm install -g --no-audit --no-fund opencode-ai \
    && npm install -g --no-audit --no-fund @openai/codex

# Pin the runner UID/GID at build time so the image's owned paths have a
# deterministic owner that matches every previously-published version of
# this image. The entrypoint can remap to a different UID/GID at container
# start via the RUNNER_UID / RUNNER_GID env vars — see entrypoint.herd.sh
# and docs/runners.md ("Matching host UID/GID").
#
# UID/GID 1001 (NOT 1000): ubuntu:24.04 ships with a default `ubuntu` user
# already at UID/GID 1000, and the previous image used `useradd` without
# `-u`, so runner has historically been 1001:1001 (the next free slot). We
# keep 1001 here to preserve backward compatibility — bind mounts and
# named volumes (notably codex-auth) created by older containers are
# chowned to 1001 on the host, and changing the default would break those
# without a manual chown step.
RUN groupadd -g 1001 runner \
    && useradd -m -d /home/runner -u 1001 -g 1001 runner \
    && chown -R runner:runner /runner /opt/herd /home/runner

WORKDIR /runner
COPY images/base/entrypoint.herd.sh ./entrypoint.herd.sh
RUN chmod +x entrypoint.herd.sh

# Intentionally NOT setting `USER runner` — the entrypoint starts as root so it
# can optionally remap the runner UID/GID, then drops to runner via gosu before
# invoking ./run.sh. Wrapper Dockerfiles (Dockerfile.herd_runner) should follow
# the same pattern: do their installs as root and not end with `USER runner`.
ENTRYPOINT ["./entrypoint.herd.sh"]
