FROM ubuntu:24.04

ARG RUNNER_VERSION=2.333.1
ARG TARGETARCH

# Dependencies (Node 22 LTS required by Claude Code and OpenCode, libicu for GitHub Actions runner)
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl jq ca-certificates git gh libicu-dev \
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
# TODO(verify): confirm plugin package name `opencode-openai-codex-auth` and that
# version 4.4.0 is correct; confirm npm-global install is the right mechanism.
# TODO(verify): confirm plugin package name `opencode-claude-auth` (it may be
# scoped) and that version 1.5.4 is correct; confirm npm-global install is the
# right mechanism. Upstream: griffinmartin/opencode-claude-auth.
RUN npm config set prefix /home/runner/.npm-global \
    && npm install -g --no-audit --no-fund @anthropic-ai/claude-code \
    && npm install -g --no-audit --no-fund opencode-ai \
    && npm install -g --no-audit --no-fund opencode-openai-codex-auth@4.4.0 \
    && npm install -g --no-audit --no-fund opencode-claude-auth@1.5.4

RUN useradd -m -d /home/runner runner \
    && chown -R runner:runner /runner /opt/herd /home/runner

WORKDIR /runner
COPY images/base/entrypoint.herd.sh ./entrypoint.herd.sh
RUN chmod +x entrypoint.herd.sh

USER runner
ENTRYPOINT ["./entrypoint.herd.sh"]
