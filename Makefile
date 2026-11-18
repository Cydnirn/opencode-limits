# opencode-limits — build, test and install
#
# Cross-platform by design: the same Makefile works on Linux, macOS and Windows.
# On Windows run it from Git Bash, MSYS2, WSL, or any shell with GNU make
# (for example `choco install make` or `winget install GnuWin32.Make`).
#
#   make            build for the host platform into ./bin
#   make install    build and copy the binary onto your PATH
#   make release    cross-compile every supported target into ./dist
#   make test       vet + race tests
#   make help       list every target
#
# Override anything on the command line, e.g.
#   make install PREFIX=$HOME/.local
#   make VERSION=0.2.0

BINARY  := opencode-limits
MODULE  := github.com/cydnirn/opencode-limits
CMD     := ./cmd/opencode-limits
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Template the linker stamps into the binary. Override PREFIX to install
# somewhere other than /usr/local.
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(DATE)

CGO_ENABLED ?= 0
PREFIX      ?= /usr/local
BINDIR      ?= $(PREFIX)/bin
GOBIN_DIR   := $(shell go env GOPATH)/bin

# Colour only when writing to a terminal, so CI logs stay clean.
ifneq (,$(findstring xterm,$(TERM)))
	BOLD := $(shell printf '\033[1m')
	DIM  := $(shell printf '\033[2m')
	OFF  := $(shell printf '\033[0m')
else
	BOLD :=
	DIM  :=
	OFF  :=
endif

# Detect the host so `make` and `make install` do the right thing everywhere.
ifeq ($(OS),Windows_NT)
	HOST_OS   := windows
	EXE       := .exe
	INSTALL_TO := $(GOBIN_DIR)
else
	UNAME_S := $(shell uname -s)
	ifeq ($(UNAME_S),Darwin)
		HOST_OS := darwin
	else
		HOST_OS := linux
	endif
	EXE       :=
	# Prefer a writable prefix: ~/.local/bin needs no sudo.
	INSTALL_TO := $(BINDIR)
endif
HOST_ARCH := $(shell go env GOARCH)
HOST_BIN  := bin/$(BINARY)-$(HOST_OS)-$(HOST_ARCH)$(EXE)

# Every target shipped in a release.
PLATFORMS := \
	linux/amd64 linux/arm64 \
	darwin/amd64 darwin/arm64 \
	windows/amd64 windows/arm64

.DEFAULT_GOAL := build
.PHONY: all build install uninstall release test test-cover vet fmt lint clean run once init help check-deps

all: build

## build: compile for the host platform into ./bin
build: check-deps
	@mkdir -p bin
	@printf '%s==> building %s %s/%s%s\n' '$(BOLD)' '$(BINARY)' '$(HOST_OS)' '$(HOST_ARCH)' '$(OFF)'
	@CGO_ENABLED=$(CGO_ENABLED) go build -trimpath -ldflags '$(LDFLAGS)' -o '$(HOST_BIN)' $(CMD)
	@printf '%s    %s  (%s)%s\n' '$(DIM)' '$(HOST_BIN)' '$(VERSION)' '$(OFF)'
	@# Convenience copy with no platform suffix, so `./bin/opencode-limits` works.
	@cp '$(HOST_BIN)' 'bin/$(BINARY)$(EXE)'

## install: build and install onto PATH (uses GOBIN on Windows, $(BINDIR) elsewhere)
install: build
	@mkdir -p '$(INSTALL_TO)'
	@# A running copy holds the file open (ETXTBSY), so write then rename into
	@# place; the rename replaces the inode and works while the old one runs.
	@cp -f '$(HOST_BIN)' '$(INSTALL_TO)/$(BINARY)$(EXE).new'
	@chmod +x '$(INSTALL_TO)/$(BINARY)$(EXE).new' 2>/dev/null || true
	@mv -f '$(INSTALL_TO)/$(BINARY)$(EXE).new' '$(INSTALL_TO)/$(BINARY)$(EXE)'
	@if pgrep -x '$(BINARY)' >/dev/null 2>&1; then \
		printf '%s    note: an older copy is still running; restart it to use this build%s\n' '$(DIM)' '$(OFF)'; \
	fi
	@printf '%s==> installed %s%s\n' '$(BOLD)' '$(INSTALL_TO)/$(BINARY)$(EXE)' '$(OFF)'
	@case ":$$PATH:" in *":$(INSTALL_TO):"*) ;; *) \
		printf '%s    note: $(INSTALL_TO) is not on your PATH. Add it:%s\n' '$(DIM)' '$(OFF)'; \
		printf '          export PATH="$(INSTALL_TO):$$PATH"\n'; \
		printf '          (Windows: setx PATH "%%PATH%%;$(INSTALL_TO)")\n' ;; esac

## uninstall: remove the installed binary
uninstall:
	@rm -f '$(INSTALL_TO)/$(BINARY)$(EXE)' && \
		printf '%s==> removed %s%s\n' '$(BOLD)' '$(INSTALL_TO)/$(BINARY)$(EXE)' '$(OFF)' || \
		printf 'nothing to remove at %s\n' '$(INSTALL_TO)/$(BINARY)$(EXE)'

## release: cross-compile all six supported targets into ./dist
release: check-deps
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out="dist/$(BINARY)-$(VERSION)-$$os-$$arch"; \
		if [ "$$os" = windows ]; then out="$$out.exe"; fi; \
		printf '%s==> %s/%s%s\n' '$(BOLD)' "$$os" "$$arch" '$(OFF)'; \
		CGO_ENABLED=$(CGO_ENABLED) GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags '$(LDFLAGS)' -o "$$out" $(CMD) || exit 1; \
	done
	@printf '%s==> %d binaries in dist/%s\n' '$(BOLD)' 6 '$(OFF)'
	@ls -1 dist

## test: vet, then run the suite with the race detector
test: check-deps
	go vet ./...
	CGO_ENABLED=1 go test -race -count=1 ./...

## test-cover: run tests and print a coverage summary
test-cover: check-deps
	CGO_ENABLED=1 go test -race -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

## vet: run go vet only
vet:
	go vet ./...

## fmt: format every Go file in place
fmt:
	gofmt -w ./cmd ./internal

## lint: fail if anything is unformatted or vet is unhappy
lint:
	@files=$$(gofmt -l ./cmd ./internal); \
	if [ -n "$$files" ]; then printf 'unformatted:\n%s\n' "$$files"; exit 1; fi
	go vet ./...

## run: build and run the TUI
run: build
	@./bin/$(BINARY)$(EXE)

## once: print the text report from the live API and exit
once: build
	@./bin/$(BINARY)$(EXE) --once

## init: write a starter config from the plan's models
init: build
	@./bin/$(BINARY)$(EXE) --init

## clean: remove build output
clean:
	@rm -rf bin dist coverage.out
	@printf '%s==> cleaned%s\n' '$(BOLD)' '$(OFF)'

## check-deps: verify the Go toolchain is present
check-deps:
	@command -v go >/dev/null 2>&1 || { \
		printf 'go not found on PATH. Install Go 1.24+ from https://go.dev/dl/\n' >&2; exit 1; }
	@go version >/dev/null 2>&1 || { printf 'go is not runnable\n' >&2; exit 1; }

## help: list targets
help:
	@printf '%sopencode-limits%s %s(%s)%s\n\n' '$(BOLD)' '$(OFF)' '$(DIM)' '$(VERSION)' '$(OFF)'
	@printf 'Usage: make <target>\n\n'
	@printf 'Host:    %s/%s\n' '$(HOST_OS)' '$(HOST_ARCH)'
	@printf 'Install: %s\n\n' '$(INSTALL_TO)'
	@printf 'Targets:\n'
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | awk -F': ' '{ printf "  \033[36m%-13s\033[0m %s\n", $$1, $$2 }'
