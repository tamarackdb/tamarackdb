BINDIR := bin
VERSION := $(shell cat VERSION)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build tamarackdb-server tamarackdb-migrate tamarackdb-init tamarackdb-backup tamarackdb-demo test run clean fmt vet tidy

build: tamarackdb-server tamarackdb-migrate tamarackdb-init tamarackdb-backup

tamarackdb-server:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb-server ./cmd/tamarackdb-server

tamarackdb-migrate:
	go build -o $(BINDIR)/tamarackdb-migrate ./cmd/tamarackdb-migrate

tamarackdb-init:
	go build -o $(BINDIR)/tamarackdb-init ./cmd/tamarackdb-init

tamarackdb-backup:
	go build -o $(BINDIR)/tamarackdb-backup ./cmd/tamarackdb-backup

tamarackdb-demo:
	go build -o $(BINDIR)/tamarackdb-demo ./cmd/tamarackdb-demo

test:
	go test ./...

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
