# Actual-worker validation

The release lane validates the supported **128 KiB default / 192 KiB maximum** using one reused 64 MiB worker per cap. Tests use synthetic source and an in-process fake judge; no live provider or real credential is needed. The assertions cover multi-file scans, partial results, report coverage, byte limits, saved experimental scans, invalid settings, and sustained operation without scheduled worker restarts.

## Prerequisites and execution

Use Go 1.27.3 for the native harness and a compatible AII OS source checkout containing `cmd/aii-plugin-worker`. The harness depends on the public SDK v0.1.18. Supply the corresponding SDK checkout for revision reporting. Download dependencies explicitly before the network-disabled run:

```sh
# From this directory, using Go 1.27.3:
go mod download
# From the repository root, build the guest using the documented TinyGo toolchain:
./build.sh
# Set these to your own checkouts/compiler; no identity paths are assumed.
export AII_OS_SOURCE=/path/to/aii-os
export AII_SDK_SOURCE=/path/to/aii-plugin-sdk
export AII_GO=/path/to/go1.27.3/bin/go
bash validation/guest/run.sh release
```

Host build dependencies must also already be cached. The script uses `GOTOOLCHAIN=local`, `GOPROXY=off` and `GOSUMDB=off`; it does not install dependencies or mutate a host. It builds a disposable worker under ignored `bin/`, copies the chosen candidate WASM, prints tool/source/artifact identifiers, and runs the actual-worker tests. `CANDIDATE_WASM` can select another candidate explicitly.

## Known-failing experimental lane

```sh
bash validation/guest/run.sh experimental
```

The `experimental` build tag preserves the original 1 MiB and diagnostic tests. Historical attempts exhausted the 64 MiB worker; fragmentation, growth behavior and conservative retention were observed in diagnostics. No tested runtime workaround established uninterrupted full-workflow success. Those observations do not justify shipping a private runtime patch. The release retains the standard toolchain and excludes expanded large-file support.

An experimental failure is a failure, not an expected-pass release result. Do not weaken the oracle or increase the worker allowance to relabel it. Instrumented measurements are not production measurements, and passing the bounded release workload does not establish unlimited stability.

See [local qualification](../README.md) for the exported candidate's results and their limits.
