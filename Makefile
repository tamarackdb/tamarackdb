BINDIR := bin
VERSION := $(shell git describe --tags --always --dirty)
LDFLAGS := -X github.com/tamarackdb/tamarackdb/internal/buildinfo.Version=$(VERSION)

.PHONY: build tamarackdb-server tamarackdb-migrate tamarackdb-init tamarackdb-backup tamarackdb-demo test test-race run clean fmt vet tidy

build: tamarackdb-server tamarackdb-migrate tamarackdb-init tamarackdb-backup

tamarackdb-server:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb-server ./cmd/tamarackdb-server

tamarackdb-migrate:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb-migrate ./cmd/tamarackdb-migrate

tamarackdb-init:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb-init ./cmd/tamarackdb-init

tamarackdb-backup:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb-backup ./cmd/tamarackdb-backup

tamarackdb-demo:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb-demo ./cmd/tamarackdb-demo

test:
	go test ./...

test-race:
	go test -race ./...

run: tamarackdb-server
	./$(BINDIR)/tamarackdb-server

clean:
	rm -rf $(BINDIR)

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy
