# syntax=docker/dockerfile:1

# Build stage: pure-Go binary (modernc.org/sqlite needs no CGO), so the image
# can be built for any TARGETOS/TARGETARCH with buildx.
FROM golang:1.26-alpine AS build

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -buildvcs=false -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/indexarr ./cmd/indexarr

# Runtime stage: no compiler, no shell tools beyond the BusyBox applets the
# health check needs, and a dedicated non-root account.
FROM alpine:3.21

ARG VERSION=dev
LABEL org.opencontainers.image.title="indexarr" \
      org.opencontainers.image.description="Torznab indexer with a native catalog, FlareSolverr sessions and app sync" \
      org.opencontainers.image.source="https://github.com/buzzqw/indexarr" \
      org.opencontainers.image.url="https://github.com/buzzqw/indexarr" \
      org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 indexarr \
    && adduser -S -D -H -u 10001 -G indexarr indexarr \
    && mkdir -p /data/.config \
    && chown -R indexarr:indexarr /data \
    && chmod 700 /data /data/.config

# Keep the vault key and the database in one volume by default. For production
# mount the key with INDEXARR_MASTER_KEY_FILE instead of storing it in /data.
ENV HOME=/data
WORKDIR /data
COPY --from=build /out/indexarr /usr/local/bin/indexarr
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

USER indexarr
EXPOSE 8788
VOLUME ["/data"]
STOPSIGNAL SIGTERM

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8788/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD []
