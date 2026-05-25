ARG BASE_VERSION
FROM ghcr.io/herd-os/herd-runner-base:${BASE_VERSION}
USER root
ARG GO_VERSION=1.24.0
ARG TARGETARCH
RUN ARCH=$(echo "$TARGETARCH" | sed 's/amd64/amd64/;s/arm64/arm64/') \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"
USER runner
