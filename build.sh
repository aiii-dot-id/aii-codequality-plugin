#!/bin/sh
# Build dist/id.aiii.codequality.wasm — the guest build 'aiisdk build' runs, usable without it.
# The host's worker requires every flag: bare wasm with no WASI (-target=wasm-unknown), one
# host-driven thread (-scheduler=none), a non-moving collector (-gc=conservative), no DWARF.
set -eu
cd "$(dirname "$0")"
TINYGO="${TINYGO:-/opt/tinygo0.42.0/bin/tinygo}"
mkdir -p dist
GOFLAGS=-buildvcs=false "$TINYGO" build -o dist/id.aiii.codequality.wasm \
  -target=wasm-unknown -scheduler=none -gc=conservative -no-debug .
ls -l dist/id.aiii.codequality.wasm
