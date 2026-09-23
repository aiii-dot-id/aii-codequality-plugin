# Changelog

## 0.1.8

- **A slow call no longer stalls a scan.** Each Jev call had 2 s, and a file whose call went
  unanswered was retried with the same 2 s. While Jev was slow for minutes, the location calls
  of a repository's largest files went unanswered every step, and the scan could not move. A
  step after an unanswered call now retries the pending file alone with three calls of 8 s,
  going on from the answers already kept. A judge makes only the calls its step planned, and
  each call's timeout is the step's 24 s shared among them, so `judge` on one text allows 8 s.
- `last_error` says how long the unanswered call was allowed.

## 0.1.7

- **Measured on a third codebase.** A third blind review, of 20 files from a further repository,
  joins the first two: 96 Go files in all. EH-02 (a failure swallowed) falls to 59% and is now a
  lead; ST-01 (a unit long enough to split) rises to 64% and is now a finding. The catalogue
  holds each statement's counts, confirmed and reviewed, rather than a rounded share.

## 0.1.6

- **`report`'s `path` reaches every folder.** The filter matched each glob against whole paths
  with a single-segment `*`, so `**` returned the top level only and `internal/**` nothing. A
  glob now matches within each segment, `**` spans any depth, and a glob naming a folder holds
  every file below it.
- **`total_records` counts what the pages return**: the files under the path, in the language,
  with a finding at the severity asked for. It used to count every file under the path.

## 0.1.5

- **Findings and leads.** A blind review of 76 Go files measured how often each statement's
  findings are real. Statements confirmed at least 60% of the time (EH-01, ID-GO-01, RD-03,
  ST-02, EH-02) report findings; the rest, and unmeasured statements, report leads, marked
  `lead`. The index is unchanged. `report`'s summary counts findings and leads apart.
- **Each finding says where.** One call per chunk with findings asks Jev which unit — a function,
  type or declaration block, by its lines — holds each code finding. The unit is reported when
  Jev's confidence is 0.7 or more (`where`, `where_confidence`); on reviewed findings such
  locations were right 49 times in 56. Below that, nothing is guessed.
- **The cache holds any answer** as JSON; caches written by earlier releases read unchanged.

## 0.1.4

- **`report`'s findings and `judge`'s results reach the caller.** Both returned records as Go
  structs, which the plugin kit's result encoder does not take, so each call failed as a bare `PLUGIN_HANDLER_FAILED`. Records are now handed
  over already encoded.
- **Every error leaves named.** The kit keeps the words of an operation error and drops the
  text of any other, so a Jev outage, a refused key or a failed read also reached the caller as
  `PLUGIN_HANDLER_FAILED`. Each operation now has one exit that names its error:
  `JUDGE_UNAVAILABLE`, `JUDGE_KEY_REFUSED`, `JUDGE_REFUSED_TEXT`, or `CODEQUALITY_FAILED` with
  the reason.
- **A report says what each finding means.** `report` and `judge` carry `faults`: every finding
  id they return, with the fault statement it was asked as. Until now a finding arrived as a
  bare code such as `EH-01`.
- **A step does twice the work.** Jev answers in well under a second (848 calls: median 0.23 s,
  max 0.50 s), and a step was held to 6 calls by a 4 s per-call timeout. The timeout is now 2 s
  and a step makes up to 12 calls: at every call's timeout it still ends inside the host's 30 s
  invoke wall. `max_calls` is gone from `scan`; a step sizes itself.
- **`include_tests` says what it costs.** Tests are often most of a large tree, and each file is
  about two Jev calls.

## 0.1.3

- **Jev's status decides what a failed call means.** The host hands a 4xx or 5xx back as the
  response and an error; 0.1.2 read only the error, so every failure was taken as "Jev is
  unavailable" and a text Jev declines was retried at every step, holding the scan on that file.
  Now 429 and 5xx are retried at the next step; a 403 page from Jev, or any other 4xx,
  records that file as refused and the scan moves on; a 401, or a 403 in JSON, says the key was
  refused. The status is kept in `last_error`, which also says the next step retries the file.
- **Languages are named in any case.** `["Go"]` selected nothing in 0.1.2: every Go file was
  excluded as `language_not_selected` and the scan reported `done`. A name the table does not
  know is now refused with the names it does.
- **`report`'s `path: "."` means every file**, as it does for `scan`.

## 0.1.2

- **`scan` works in the identity's sandbox.** The plugin no longer has folders of its own. With
  files granted on its card, `scan` takes one `path`, named as the identity's own tools name
  it: relative to its home, or absolute inside a folder added in Settings → Sandbox. The
  `root` argument is gone.
- **Capability `fs.sandbox`** replaces `fs.roots`. **Needs AII OS 0.1.10 or newer**
  (`aiios_min_version`): an older host knows no sandbox for plugins, so it is not offered this
  release.

## 0.1.1

- **The key is pasted on the plugin's card.** The `api_key` setting is still a credential handle
  the plugin never sees. On AII OS 0.1.9 the card takes the key itself, keeps it privately,
  and grants it to this plugin for `api.typesafe.ai` only.
- **The model is chosen from Jev's own list.** The `model` setting names a new read operation,
  `models`, which asks Jev (`GET /v1/models`) with the pasted key. The card offers the answer
  as a drop-down; if the lookup fails, it keeps a text field and says why.
- **The default model is `jev-latest`.** That is the name Jev lists; `jev-1.13.0` was not in
  the list.
- **Needs AII OS 0.1.9 or newer** (`aiios_min_version`). An older host reads the settings
  strictly and would refuse this release, so older hosts are not offered it.

## 0.1.0

First release.

- `judge`, `scan` and `report` over TypeSafe's Jev (`/v1/systemone`).
- Before any text leaves the machine, comments are blanked (line numbers kept) and secrets
  redacted. Code and comments are asked in separate calls.
- Catalogue `general.v3`:
  - 16 general fault statements
  - 4 per language for Go, Python, JavaScript/TypeScript and Java
  - 5 about comments
- Scoring: findings at p ≥ 0.6; low / medium / high deduct 5 / 12 / 25 from their dimension; the
  index is the mean of the dimensions present; bands are clean, minor, moderate and severe.
- Answers are cached by content, catalogue and model, so an unchanged tree re-scans without a
  call.
- The scan is resumable, six Jev calls per step. It follows the language table, `.gitignore`,
  `.gitattributes` and the caller's excludes. It skips nested repositories, symlinks,
  secret-named, binary, generated, minified, oversize and test files, and counts every skip by
  reason.
- A text Jev declines is recorded under `refused`, and the scan moves on.
