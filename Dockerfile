FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/indexarr ./cmd/indexarr

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 indexarr \
    && mkdir -p /data \
    && chown indexarr:indexarr /data

USER indexarr
WORKDIR /data
COPY --from=build /out/indexarr /usr/local/bin/indexarr

EXPOSE 8484
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8484/healthz >/dev/null || exit 1

ENTRYPOINT ["indexarr", "-config", "/data/indexarr.json", "-listen", ":8484"]
