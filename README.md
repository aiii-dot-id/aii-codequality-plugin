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

A statement's findings are reported as **findings** when a blind review confirmed at least 60% of
them, and as **leads** otherwise: a lead is a place to look, not a verdict. Measured on 122 Go
files from four repositories: EH-01 88%, RD-03 84%, ID-GO-01 76%, ST-01 75% and ST-02 73% are
findings; the other statements (EH-02 is next, at 56%), and those not yet measured (the comment
statements, other languages' own), are leads. Leads still count in the index, which was
validated with them.

Each code finding and lead names the **unit** that holds it — a function, type or declaration
block, by its lines — when Jev locates it with confidence 0.7 or more; on reviewed findings those
locations held the reviewer's line 49 times in 56, and 31 in 40 on two later codebases before
0.1.10 stopped a multi-line raw string from cutting a function into pieces. Below 0.7 it is left
unlocated rather than guessed. Locating costs one more call per chunk with findings, and is
cached like every answer.

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
- **Tick files** and press Save grants. `scan` then works in the identity's own sandbox, by the
  paths the identity's tools take: its home, and the folders added in Settings → Sandbox. The
  identity's data directory, key, store and config stay out of reach.

Attaching a stored key to a public host requires a review-proven package (T2 or T3).

## Build and test

```sh
./build.sh    # TinyGo 0.42 or newer; TINYGO names it when it is not on PATH
go test -race ./...
go tool aiisdk test -grant files=$PWD/testdata/tree -grant net.outbound:api.typesafe.ai:443
```

## Validation (catalogue general.v3)

**Expert ratings.** 167 Java classes rated for maintainability by 70 professional developers, a
published dataset on which no catalogue wording was chosen:
- Spearman −0.70 between the index and the experts' overall rating
- AUC 0.98 for maintainable versus not
- band weighted κ 0.63

**Injected faults.** 76 source files in eight languages, each also judged with 1, 2 and 4 faults
injected (an error ignored, a failure swallowed, a guard removed, a resource not released):
- the index falls from 0 to 4 faults on 53 of 76 files
- the injected items are named 76% of the time

**Real trees.** One repository each for Go, TypeScript, Python, C, C++, Java, Rust and
JavaScript, 300 files each:
- 1,173 files judged with 1,757 Jev calls and 4.6M input tokens
- re-scanning an unchanged tree made 0 calls
- 7 files were declined by Jev

The language table in `cq/annex_l.json` is derived from third-party data; see NOTICE.
