# BASE_VERSION defaults to `latest` for ad-hoc local builds; CI passes the release tag.
ARG BASE_VERSION=latest
FROM ghcr.io/herd-os/herd-runner-base:${BASE_VERSION}
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
USER runner
