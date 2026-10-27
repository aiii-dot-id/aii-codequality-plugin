#!/bin/bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
HOST=${AII_OS_SOURCE:?Set AII_OS_SOURCE to a compatible host source checkout}
SDK=${AII_SDK_SOURCE:?Set AII_SDK_SOURCE to the matching SDK checkout}
GO=${AII_GO:-go}
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
[[ "$("$GO" version)" == 'go version go1.27.3 '* ]] || { echo 'Requires Go 1.27.3'; exit 1; }
LANE=${1:-release}
case "$LANE" in
 release) TEST_ARGS=(); echo 'NARROWED RELEASE: default 128KiB / maximum 192KiB; NOT 1MiB qualification. Experimental tests are retained, not passed.' ;;
 experimental) TEST_ARGS=(-tags experimental -run '^(TestGuestOneMiB|TestDiagnosticOneMiB)'); echo 'EXPERIMENTAL: retained 1MiB tests; failures are failures, not release qualification.' ;;
 *) echo 'Usage: bash run.sh [release|experimental]' >&2; exit 2 ;;
esac
mkdir -p "$HERE/bin"
"$GO" version
# Only the real host worker is built here, never production WASM.
(cd "$HOST" && "$GO" build -o "$HERE/bin/aii-plugin-worker" ./cmd/aii-plugin-worker)
cp "${CANDIDATE_WASM:-$HERE/../../dist/id.aiii.codequality.wasm}" "$HERE/bin/candidate.wasm"
shasum -a 256 "$HERE/bin/candidate.wasm" "$HERE/bin/aii-plugin-worker"
git -C "$HOST" rev-parse HEAD
git -C "$SDK" rev-parse HEAD
"$GO" version "$HERE/bin/aii-plugin-worker"
(cd "$HERE" && "$GO" test -race -count=1 -v -timeout=4m "${TEST_ARGS[@]}" ./...)
