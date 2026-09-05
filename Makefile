BINDIR := bin
VERSION := $(shell cat VERSION)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build demo test run clean fmt vet tidy \
	build-linux build-windows build-macos build-all

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb ./cmd/tamarackdb
	go build -o $(BINDIR)/tamarackdb-migrate ./cmd/migrate
	go build -o $(BINDIR)/tamarackdb-init ./cmd/init

demo:
	go build -o $(BINDIR)/tamarackdb-demo ./cmd/demo

# Cross-compilation targets. CGO_ENABLED=0 because modernc.org/sqlite is
# pure Go, so no C toolchain is needed for any target platform.

define build_target
	mkdir -p $(BINDIR)/$(1)-$(2)
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(1)-$(2)/tamarackdb$(3) ./cmd/tamarackdb
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -o $(BINDIR)/$(1)-$(2)/tamarackdb-migrate$(3) ./cmd/migrate
	GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -o $(BINDIR)/$(1)-$(2)/tamarackdb-init$(3) ./cmd/init
endef

build-linux:
	$(call build_target,linux,amd64,)
	$(call build_target,linux,arm64,)

build-windows:
	$(call build_target,windows,amd64,.exe)
	$(call build_target,windows,arm64,.exe)

build-macos:
	$(call build_target,darwin,amd64,)
	$(call build_target,darwin,arm64,)

build-all: build-linux build-windows build-macos

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
