# IgnisForum — embedded IgnisDB-Go build
# Requires: Go 1.22+, gcc/make, rust/cargo (engine links rust-core static lib).
GO ?= go
RUST_LIB := ../IgnisDB-Go/rust-core/target/release/libignisdb_core.a
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION) -X main.buildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)"

.PHONY: all build rust run test bench clean tidy

all: build

# Build the Rust core static library the engine links against (required).
rust:
	cd ../IgnisDB-Go/rust-core && cargo build --release

# go.mod replace expects ../IgnisDB-Go to exist and be built first.
tidy:
	CGO_ENABLED=1 $(GO) mod tidy

build: rust
	CGO_ENABLED=1 $(GO) build $(LDFLAGS) -o bin/ignisforum ./cmd/ignisforum

run: build
	IGNISFORUM_DATA_DIR=./data ./bin/ignisforum

test:
	CGO_ENABLED=1 $(GO) test -race ./...

bench:
	CGO_ENABLED=1 $(GO) test -bench=. -benchmem ./...

clean:
	rm -rf bin/ data/
