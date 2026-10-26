FROM golang:1.26-alpine AS build

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/indexarr ./cmd/indexarr

FROM alpine:3.21

ARG VERSION=dev
LABEL org.opencontainers.image.title="indexarr" \
      org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 indexarr \
    && mkdir -p /data/.config \
    && chown -R indexarr:indexarr /data

USER indexarr
# Persist the vault key inside the data volume unless the operator supplies
# INDEXARR_MASTER_KEY_FILE/INDEXARR_MASTER_KEY (recommended for production).
ENV HOME=/data
WORKDIR /data
COPY --from=build /out/indexarr /usr/local/bin/indexarr

EXPOSE 8788
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8788/healthz >/dev/null || exit 1

ENTRYPOINT ["indexarr", "-config", "/data/indexarr.json", "-listen", ":8788"]
