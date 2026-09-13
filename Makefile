BINDIR := bin
VERSION := $(shell cat VERSION)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build demo test run clean fmt vet tidy build-linux

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb ./cmd/tamarackdb
	go build -o $(BINDIR)/tamarackdb-migrate ./cmd/migrate
	go build -o $(BINDIR)/tamarackdb-init ./cmd/init
	go build -o $(BINDIR)/tamarackdb-backup ./cmd/tamarackdb-backup

demo:
	go build -o $(BINDIR)/tamarackdb-demo ./cmd/demo

# Cross-arch build target. CGO_ENABLED=0 because modernc.org/sqlite is pure
# Go, so no C toolchain is needed. Linux only: Docker covers every other
# platform.

define build_target
	mkdir -p $(BINDIR)/$(1)-$(2)
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(1)-$(2)/tamarackdb$(3) ./cmd/tamarackdb
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -o $(BINDIR)/$(1)-$(2)/tamarackdb-migrate$(3) ./cmd/migrate
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -o $(BINDIR)/$(1)-$(2)/tamarackdb-init$(3) ./cmd/init
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -o $(BINDIR)/$(1)-$(2)/tamarackdb-backup$(3) ./cmd/tamarackdb-backup
endef

build-linux:
	$(call build_target,linux,amd64,)
	$(call build_target,linux,arm64,)

test:
	go test ./...

run: build
	./$(BINDIR)/tamarackdb

clean:
	rm -rf $(BINDIR)

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy
