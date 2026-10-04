#!/usr/bin/env sh
# Build a Go WASM extension (wasip1) for the ShitIDC extension host.
# Usage: ./scripts/build-extension.sh extensions/demo-logger
set -e
DIR="${1:?usage: build-extension.sh <extension-dir>}"
cd "$DIR"
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o demo-logger.wasm .
echo "built $DIR/demo-logger.wasm"
