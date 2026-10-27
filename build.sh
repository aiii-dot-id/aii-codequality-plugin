#!/bin/sh

set -eu
cd "$(dirname "$0")"
TINYGO="${TINYGO:-tinygo}"
mkdir -p dist
GOFLAGS=-buildvcs=false "$TINYGO" build -o dist/id.aiii.codequality.wasm \
  -target=wasm-unknown -scheduler=none -gc=conservative -no-debug .
ls -l dist/id.aiii.codequality.wasm
