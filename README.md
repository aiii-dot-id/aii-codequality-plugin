# id.aiii.codequality

Measures source code quality with TypeSafe's **Jev** as the judge.

Each file's comments are blanked (line numbers kept) and secrets redacted. Then Jev is asked a
fixed catalogue of fault statements (`general.v3`, 16 general items plus 4 per-language items
for Go, Python, JavaScript/TypeScript and Java) about the code, and 5 more about the comments,
in two calls. Answers at p ≥ 0.6 are findings; each finding deducts 5 / 12 / 25 points (low /
medium / high) from its dimension. The index is the mean of the dimensions present, and the
band is clean ≥ 90, minor ≥ 75, moderate ≥ 60, severe below. Answers are cached by content, so
an unchanged file is never asked twice.

## Findings, leads and locations

Results use the catalogue's fixed calibration table to distinguish **findings**
from **leads**. A lead is a place to investigate, not a verdict; both contribute
to the reported index. A code finding or lead names its containing unit when
Jev locates it with confidence of at least 0.7. Lower-confidence locations are
left unspecified. Locating costs one additional call per chunk with findings,
and the answer is cached.

## Operations

| Operation | Effect | Does |
|---|---|---|
| `judge` | write.external | judges up to twelve Jev calls' worth of texts passed in (`items: [{ref, text, language?}]`) |
| `scan` | write.external | `start` a walk of a folder in the identity's sandbox, then `step` it until `done`: each step judges files until twelve Jev calls are used |
| `models` | read.external | Jev's model list, the choices the card offers for the `model` setting |
| `report` | read.internal | a scan's summary (index, bands, per-language means, finding and lead counts, worst files) or its findings and leads page by page, with the fault statements they name and the folder the paths are relative to (`root`); no `scan_id` lists the scans |

A scan skips what the language table, the folder's `.gitignore` and `.gitattributes`
(vendored / generated / documentation), and the caller's `exclude` globs leave out. It also
skips:
- nested repositories and symlinks
- secret-named files, files with NUL bytes and files over 128 KiB
- generated and minified files
- test files, unless `include_tests` is set

Every skip is counted by reason. A text Jev declines is recorded under `refused` and the scan
moves on. The eight most recent scans are kept in the private directory.

A step makes up to twelve Jev calls of 2 s each. When a call goes unanswered, the scan stops as
`judge_unavailable` with the file still pending, and the next step retries that file alone with
three calls of 8 s each, going on from the answers already kept.

## Install

Needs AII OS 0.1.10 or newer. On the plugin's card in the dashboard:
- **Paste Jev's key** and press the card's Save. AII OS keeps it and grants it to this plugin
  for `api.typesafe.ai:443` only.
- **Choose the model** from the drop-down, filled from Jev's own list by the `models` operation.
  Malformed or oversized lists are refused whole; no incomplete list is presented as successful.
- **Tick files** and press Save grants. `scan` then works in the identity's own sandbox, by the
  paths the identity's tools take: its home, and the folders added in Settings → Sandbox. The
  identity's data directory, key, store and config stay out of reach.

Attaching a stored key to a public host requires a review-proven package (T2 or T3).

## Build and test

```sh
./build.sh    # TinyGo 0.42 or newer; TINYGO names it when it is not on PATH
go test -race ./...
aiisdk test -grant files=$PWD/testdata/tree -grant net.outbound:api.typesafe.ai:443
```

## Qualification scope

Local native and packaged-WASM checks cover validation, bounded retries, cache
reuse, model discovery and host-broker behavior. They use synthetic transport
responses; they do not establish live TypeSafe accuracy or availability.

The language table in `cq/annex_l.json` is derived from third-party data; see NOTICE.

Package with AII Plugin SDK 0.1.18 or newer. Verify that staged settings preserve the complete author declaration, including `choices_from`, before signing.
