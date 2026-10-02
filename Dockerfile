# syntax=docker/dockerfile:1

# The builder runs on the build machine's own platform and cross-compiles
# for the target one, so a multi-platform build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH

RUN export CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH && \
    go build -ldflags "-X github.com/tamarackdb/tamarackdb/internal/buildinfo.Version=$VERSION" -o /out/tamarackdb-server ./cmd/tamarackdb-server && \
    go build -ldflags "-X github.com/tamarackdb/tamarackdb/internal/buildinfo.Version=$VERSION" -o /out/tamarackdb-init ./cmd/tamarackdb-init

FROM alpine:3.20

# A fixed UID and GID, so a host directory mounted on /data, or a socket
# shared with another container, can be given to them ahead of time.
RUN addgroup -S -g 10001 tamarackdb && adduser -S -u 10001 -G tamarackdb tamarackdb
WORKDIR /app

COPY --from=builder /out/tamarackdb-server /out/tamarackdb-init ./
COPY --chmod=755 docker-entrypoint.sh ./

RUN mkdir -p /data && chown -R tamarackdb:tamarackdb /data && chmod 700 /data

ENV TAMARACKDB_BIND_ADDRESS=0.0.0.0 \
    TAMARACKDB_PORT=8085 \
    TAMARACKDB_DATA_DIR=/data

VOLUME ["/data"]
EXPOSE 8085

USER tamarackdb

ENTRYPOINT ["./docker-entrypoint.sh"]
