# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=$(cat VERSION)" -o /out/tamarackdb-server ./cmd/tamarackdb-server && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/tamarackdb-migrate ./cmd/tamarackdb-migrate && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/tamarackdb-init ./cmd/tamarackdb-init

FROM alpine:3.20

RUN addgroup -S tamarackdb && adduser -S tamarackdb -G tamarackdb
WORKDIR /app

COPY --from=builder /out/tamarackdb-server /out/tamarackdb-migrate /out/tamarackdb-init ./

RUN mkdir -p /data && chown -R tamarackdb:tamarackdb /data

ENV TAMARACKDB_BIND_ADDRESS=0.0.0.0 \
    TAMARACKDB_PORT=8085 \
    TAMARACKDB_DATABASE_PATH=/data/tamarack.sqlite

VOLUME ["/data"]
EXPOSE 8085

USER tamarackdb

ENTRYPOINT ["./tamarackdb-server"]
