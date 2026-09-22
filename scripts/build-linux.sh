#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
go_binary="${GO_BIN:-go}"
output="${1:-artifacts/workbuddy.so}"
mkdir -p "$(dirname "$output")"
CGO_ENABLED=1 "$go_binary" test -race ./...
CGO_ENABLED=1 "$go_binary" build -mod=readonly -trimpath -buildmode=c-shared -o "$output" .
