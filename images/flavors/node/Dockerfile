# BASE_VERSION defaults to `latest` for ad-hoc local builds; CI passes the release tag.
ARG BASE_VERSION=latest
FROM ghcr.io/herd-os/herd-runner-base:${BASE_VERSION}
USER root
# Node current LTS (matches base; explicit here so the node flavor is self-contained)
RUN curl -fsSL https://deb.nodesource.com/setup_lts.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*
USER runner
