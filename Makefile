BINDIR := bin
VERSION := $(shell cat VERSION)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build demo test run clean fmt vet tidy

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/tamarackdb ./cmd/tamarackdb
	go build -o $(BINDIR)/tamarackdb-migrate ./cmd/migrate
	go build -o $(BINDIR)/tamarackdb-init ./cmd/init

demo:
	go build -o $(BINDIR)/tamarackdb-demo ./cmd/demo

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
