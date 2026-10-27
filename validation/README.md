# Validation

This directory contains reproducible tests and deliberate negative controls, not private development transcripts. See [guest instructions](guest/README.md) and [release procedure](../RELEASING.md).

## Prior candidate qualification (before official derivation)

- Native Go 1.27.3 uncached race tests and vet passed for the prior scrubbed scratch export, not the officially derived tree.
- TinyGo 0.42.0 rebuilt guest SHA-256 `355226bc321e1e92e8f69462e9bb1c77e6ed7d3503479835a00ca0b11c5970d3`, byte-identical to the previously qualified guest.
- SDK qualification passed 25 checks and 15 cases with only a fixture-filesystem grant, no network grant.
- The actual-worker release suite passed again in 79.227 seconds. It includes six sustained scans, three per reused 64 MiB worker at the 128 KiB default and 192 KiB maximum, with no restarts; compatibility and coverage assertions passed.
- These are local results, not installation receipts or semantic accuracy evidence. Synthetic judge transport tests routing and recovery, not model quality.
- Expanded 1 MiB support remains deferred. Its known-failing regressions remain behind the `experimental` build tag; they are not passing release tests. Bounded workload survival does not prove a memory plateau or safety for every future workload.
- Live-provider semantic evaluation has NOT RUN.

The prior scratch scrub changed documentation, CI permissions and harness setup, not runtime source, schemas, settings, or the guest. Official derivation additionally sanitizes source comments; the derived tree must independently pass the release gates. The following hashes describe the prior candidate and are comparison targets, not a claim that the derived build has already passed. All 20 package install files and both signing hashes remained unchanged. The signed archive SHA-256 is `aded4c4b05c069d14bedc7946048a2d22955c1bd9bea18bfab52fdfc0d8fdc56`.

## Negative controls

Run the `*_negative_controls.py` scripts only in disposable copies. They deliberately remove safeguards and require a matching test failure before restoring the file. Never publish or package a mutated copy. `freeze_candidate.py` records a source/artifact inventory locally; generated evidence belongs under ignored `dist/`, not in the source release.
