BINARY := gdui
PREFIX ?= $(HOME)/.local
BIN    := $(PREFIX)/bin

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build install uninstall test vet clean run

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) .

install: build
	install -d $(BIN)
	install -m 0755 $(BINARY) $(BIN)/$(BINARY)
	@echo "Installed $(BIN)/$(BINARY)"

uninstall:
	rm -f $(BIN)/$(BINARY)
	@echo "Removed $(BIN)/$(BINARY)"

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
