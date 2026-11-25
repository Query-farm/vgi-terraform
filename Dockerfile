# syntax=docker/dockerfile:1
# One image, both transports:
#   docker run -p 8000:8000 IMAGE         -> HTTP on $PORT (default 8000; GET /health)
#   docker run -i IMAGE stdio             -> stdio worker, for DuckDB on the same host

# A cgo build: the worker links vgi-go's DuckDB expression evaluator
# (vgi/exprduckdb) as the exact-filter fallback.
FROM golang:1.26-bookworm AS build
ENV CGO_ENABLED=1
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/vgi-terraform-worker ./cmd/vgi-terraform-worker

FROM debian:bookworm-slim

ARG VERSION=0.0.0
ARG GIT_COMMIT=unknown
LABEL org.opencontainers.image.title="vgi-terraform-worker" \
      org.opencontainers.image.description="HCP Terraform runs, workspaces, state and plan files as SQL in DuckDB (VGI worker)" \
      org.opencontainers.image.source="https://github.com/Query-farm/vgi-terraform" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_COMMIT}" \
      org.opencontainers.image.licenses="0BSD"

# ca-certificates for outbound HTTPS, curl for the health check.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

ENV PORT=8000
COPY --from=build --chmod=0755 /out/vgi-terraform-worker /usr/local/bin/vgi-terraform-worker
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# Run as an unprivileged user.
RUN useradd --create-home --uid 10001 worker
USER worker

EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
    CMD curl -fsSI "http://127.0.0.1:${PORT:-8000}/health" || exit 1
ENTRYPOINT ["docker-entrypoint.sh"]
