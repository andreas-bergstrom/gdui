BINARY := gdui
PREFIX ?= $(HOME)/.local
BIN    := $(PREFIX)/bin

.PHONY: build install uninstall test vet clean run

build:
	go build -o $(BINARY) .

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
