# id.aiii.codequality

Measures source code quality with TypeSafe's **Jev** as the judge.

Each file's comments are blanked (line numbers kept) and secrets redacted. Then Jev is asked a
fixed catalogue of fault statements (`general.v3`, 16 general items plus 4 per-language items
for Go, Python, JavaScript/TypeScript and Java) about the code, and 5 more about the comments,
in two calls. Answers at p ≥ 0.6 are findings; each finding deducts 5 / 12 / 25 points (low /
medium / high) from its dimension. The index is the mean of the dimensions present, and the
band is clean ≥ 90, minor ≥ 75, moderate ≥ 60, severe below. Answers are cached by content, so
an unchanged file is never asked twice.

## Operations

| Operation | Effect | Does |
|---|---|---|
| `judge` | write.external | judges up to six Jev calls' worth of texts passed in (`items: [{ref, text, language?}]`) |
| `scan` | write.external | `start` a walk of a granted folder, then `step` it until `done`: each step judges files until six Jev calls are used |
| `report` | read.internal | a scan's summary (index, bands, per-language means, finding counts, worst files) or its findings page by page; no `scan_id` lists the scans |

A scan skips what the language table (Annex L), the folder's `.gitignore` and `.gitattributes`
(vendored / generated / documentation), and the caller's `exclude` globs leave out. It also
skips:
- nested repositories and symlinks
- secret-named files, files with NUL bytes and files over 128 KiB
- generated and minified files
- test files, unless `include_tests` is set

Every skip is counted by reason. A text Jev's hosted edge refuses (its firewall blocks some
legitimate source) is recorded under `refused` and the scan moves on. The eight most recent
scans are kept in the private directory.

## Install

Grants on the host (`plugins.grants.id.aiii.codequality`):
- `roots`: the folders to scan
- `hosts`: `["api.typesafe.ai:443"]`
- `credential_handles`: the auth profile holding Jev's key (Bearer, `api.typesafe.ai:443`)

The operator names that profile in the `api_key` setting. Attaching a stored key to a public host
requires a review-proven package (T2 or T3).

## Build and test

```sh
TINYGO=/opt/tinygo0.42.0/bin/tinygo ./build.sh
go test -race ./...
go tool aiisdk test -grant root:src=$PWD/testdata/tree -grant net.outbound:api.typesafe.ai:443
```

`cmd/cqvalidate` runs the same core (package `cq`) against Jev over local corpora.

## Validation (Jev 1.13.0, catalogue general.v3)

**Humans.** 167 Java classes rated for maintainability by 70 professionals (Schnappinger et al.,
ICSME 2020, [figshare 12801215](https://doi.org/10.6084/m9.figshare.12801215), CC BY 4.0).
No catalogue wording was chosen on these classes. Results:
- Spearman −0.70 between index and the experts' Overall rating
- AUC 0.98 for maintainable versus not
- band weighted κ 0.63

On the same files, CodeScene Code Health scores −0.75 and the Maintainability Index −0.60 (the
tool scores published with the ICSME 2024 replication, Zenodo 12548630).

**Injected faults.** 76 source files in eight languages from our own repositories, each also
judged with 1, 2 and 4 faults injected (an error ignored, a failure swallowed, a guard removed, a
resource not released):
- the index falls from 0 to 4 faults on 53 of 76 files
- the injected items are named 76% of the time

**Real trees.** One repository each for Go, TypeScript, Python, C, C++, Java, Rust and
JavaScript, 300 files each:
- 1,173 files judged in 336 steps with 1,757 Jev calls and 4.6M input tokens, about a minute per
  300 files
- re-scanning an unchanged tree made 0 calls
- 7 files were refused by Jev's edge

`cmd/cqvalidate` reproduces these runs given the corpora and a Jev key.

## Third-party data

`cq/annex_l.json`, the language table, is derived from:
- GitHub Linguist's `languages.yml` (MIT), for extensions, file names and interpreters
- `github/gitignore` templates (CC0-1.0), for the per-language exclusions

See NOTICE.
