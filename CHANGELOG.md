# Changelog

## 0.1.15 — unreleased: 0.1.14's regressions corrected

The version is set for the AII OS 0.2.0 release train; the ladder signs and publishes it.
**Live semantic pilot: NOT RUN.**

- **Ordinary repositories scan again.** 0.1.14 refused every `.gitattributes` attribute but a
  positive linguist exclusion, and every `.gitignore` character class, so `scan start` failed on
  aii-os (`gradlew.bat -whitespace`) and the SDK (`*.go text eol=lf`). Attributes without
  exclusion meaning are ignored again, as before 0.1.14. `linguist-vendored`, `-generated` and
  `-documentation` resolve per attribute, last match winning, including `-x`, `!x` and `=false`;
  linguist forms the parser cannot interpret still fail closed. Ignore rules read classes and
  ranges (`*.py[cod]`, `[!a]`). Over 820 rule files the old grammar refused 215 lines; this one 6.
- **A refused text no longer ends a scan.** Every 403 was read as an authentication failure and
  any non-retryable failure was persisted as a block, so one file Jev's edge refused ended the
  scan for good. A 403 page and every other 4xx but authentication now record that file as
  `refused_by_judge` and the scan moves on, as before 0.1.14. A 401, or a 403 answered in JSON,
  stops the step as `JUDGE_AUTHENTICATION_REQUIRED` with the operator's route; the scan keeps its
  place and continues once the key works.
- **An outage no longer ends a scan.** Nine unavailable attempts (about 90 minutes at the
  schedule) blocked it permanently. Exhausting the retry budget now ends that round of backoff;
  the next step retries. A transient failure of optional localization is asked again on resume
  rather than recorded as `localization_failed` at once; after one unanswered retry round it is
  recorded so, and the scan moves on.
- **Scans 0.1.14 already blocked stay blocked.** A scan 0.1.14 persisted as `request_blocked`
  (for a refused text, an authentication failure or exhausted retries) is not reopened; its
  results remain reportable. Start a new scan of the same folder.
- **A revoked key appends nothing per step.** An authentication stop leaves the file pending
  without a failed-battery record or coverage event; the route is in `last_error`.
- **`retry_budget` is a setting** (8 by default, as before; 0–32). How much of a journal a report
  ranks stays a named constant, lowered from 2 MiB to 1 MiB: on the 64 MiB guest worker a report
  retaining 2 MiB of real scan records traps, so no larger value is safe to offer.
- **A step's cost is its own work.** Each step validated the whole journal, twice in some steps;
  on the 64 MiB guest worker that took 16.5 s at an 8 MiB journal and trapped out of memory at
  16 MiB, so a scan stopped advancing long before its 48 MiB cap. A step now validates the suffix
  after its checkpoint: about 0.02 s of validation at any size, 0.3 s for a whole step at 47 MiB.
- **A report answers on any scan the plugin can step.** Every report read and decoded the whole
  journal and trapped the guest once it passed 2 MiB of real scan records. A report now reads only the prefix it ranks
  and the uncommitted suffix past the checkpoint (validated; a torn tail fails closed); its
  cursors bind the ranked prefix and the journal's length. 2.3–4.1 s per report at 5, 16 and
  47 MiB journals. `report_complete=false` and the note still say when only a prefix is ranked.
- **Nothing silently vanishes.** A scan whose state or checkpoint cannot be loaded is listed as
  `unreadable` with its route, and scheduled advancement steps the next scan instead of failing.
  A crashed start's leftovers no longer answer `SCAN_EXISTS` forever, and retention deletes a
  scan's state last. A missing host clock is refused as `HOST_CLOCK_UNAVAILABLE`. A direct
  `judge` refusal states the largest text one invocation's calls allow.
- **Typed codes of a failed direct `judge`:** `JUDGE_UNAVAILABLE` (429, 5xx, no answer, exhausted
  retries — 0.1.14 named it `JUDGE_JUDGE_UNAVAILABLE`), `JUDGE_AUTHENTICATION_REQUIRED` (401, or
  a 403 in JSON; 0.1.13 said `JUDGE_KEY_REFUSED`), and for a refused text `JUDGE_REFUSED`,
  `JUDGE_REQUEST_VALIDATION`, `JUDGE_REQUEST_BODY_LIMIT` or `JUDGE_MODEL_CONTEXT_LIMIT` (0.1.13
  said `JUDGE_REFUSED_TEXT`). Neither AII OS nor the catalog reads these names.
- **Every report shape says when it ranks only a prefix.** `review_queue` and `coverage` now
  carry the same `note` and route as summary and findings.
- **Known limit, stated:** refusal versus authentication rests on the status and on whether a
  403 body is JSON, unverified against real Jev bodies. A JSON firewall 403 reads as an
  authentication stop; a revoked key answered with an HTML 403 marks every file refused and
  finishes `done`. A summary now states `refused_by_judge` and the files judged whenever any were
  refused.
- The unreachable alias memo (`cache/models.json`) and status mapping are removed; model-read and
  judge-sent sentences live in `words.go` and `cq/words.go`; the operation summaries say what the
  results now contain. The guest harness no longer defaults to an identity's workspace paths.

## 0.1.14 — bounded review queue, released 2026-10-27

These describe source-level changes, not evidence of semantic accuracy. **Live semantic pilot: NOT RUN.**

- Add distinct path/digest `review_queue` and separate `coverage` views; retain
  summary/findings compatibility. Versioned severity/category-breadth ordering is
  an unvalidated candidate, not a demonstrated improvement. Opaque generation-bound
  cursors reject stale evidence/query combinations.
- Separate assessment (`assessed`, `partial`, `unknown`, `excluded`), allegations
  and legacy score. Retain valid raw probabilities, applicability, policy/source/
  model provenance and context-limited questions. Abstention or missing context is
  not clean. Historical lead labels remain historical; see CONTEXT-POLICY.md.
- Replace whole-file comments-only requests with bounded contextual documentation
  windows. Code questions remain comment-blanked. Go declaration boundaries and
  lexical/parser/split limits are explicit; masking preserves offsets and requests
  are checked after encoding. Preserve the legacy 128 KiB raw default / 192 KiB
  maximum; oversized files remain explicit unassessed coverage gaps. Expanded
  1 MiB support is out of this release, with its failed memory regressions retained separately. Saved experimental
  scans above the release ceiling remain report-only, without silent cap changes.
  Optional localization can decline.
- Load nested rules before submission with a limited grammar and combined chain
  budget. Unsafe/unreadable/unsupported rules fail closed for the affected subtree;
  coverage retains exclusions and unenumerated boundaries.
- Add v2 per-battery/coverage journals and alternating checksummed bounded
  checkpoints. Replay stable identities independently of inference-cache retention;
  changed source is stale, not merged. No fsync, power-loss, transaction or
  concurrent-writer guarantee is claimed. Legacy scans remain reportable with
  unknown coverage but require a fresh scan for the new pipeline.
- Pin scan backend/full endpoint/model identity before dependent progress. Hosted
  aliases resolve from the first ordinary assessment reply, not an old memo.
  Local identity still relies on the operator/service contract. Persist bounded
  availability/rate-limit backoff with SDK Retry-After support; invalid unchanged
  requests block rather than retry indefinitely.
- Bound report input: v2 validates/digests the full journal but retains a 2 MiB
  event prefix and marks incompleteness. Pagination cannot retrieve its omitted
  suffix; use a narrower scan, not a prefix's top N as repository ranking.
- Direct judge establishes fresh invocation-level attribution and returns retained
  partial records on assessment failure; only scans persist progress/backoff across
  restart. This integration changed during documentation review and is not a runtime
  validation claim.
- Inventory completion cannot hide unknown subtree boundaries or an unread report
  suffix. Oversized/unreadable/unsupported source is unknown rather than an
  intentional exclusion. Question instructions include original physical ranges.
- HTTP 400 still has no verified typed-code envelope; preserve the conservative
  request-validation diagnosis rather than infer token overflow from error prose.
- Specify separate native Go 1.27.3, TinyGo, isolated no-live-provider guest/host,
  unsigned-candidate, semantic pilot and authorized signing gates. Pilot rankings
  use identical records and deduplicate actionable issues as well as files; no
  command outcome or readiness claim follows from this changelog.
- Retain hosted/local configuration, destination disclosure, bounded caches,
  configurable step/call budgets and optional host scheduling. Operator grants
  remain the consent boundary; implementation authorizes no live source egress.

## 0.1.13

- Keep a pending multi-chunk file when a retry uses a smaller call budget. Cached answers continue across bounded steps; the file-size limit is unchanged.


## 0.1.12

- Repackage with the corrected SDK so the declared dynamic model picker survives into the signed settings member.

## 0.1.11

- Model discovery refuses malformed entries, duplicate JSON keys, and more than 256 distinct models. It no longer silently skips bad entries or trims a list while reporting success. Duplicate valid model names still appear once.

## 0.1.10

- **A raw string no longer cuts a function into units.** A multi-line raw string whose lines
  start at column 0 (a command's usage text in backticks, a template literal) was split as if
  each line began a declaration, so a location named a fragment of the right function. The
  comment lexer, which already follows every language's strings, now reports the lines that
  begin inside one, and the splitter never starts a unit there.
- **A location is kept for the units it was asked about.** Its cache key covers the units
  offered, so a text split differently is asked again. Scans made earlier ask each location
  once more.
- **A findings page names the folder scanned** (`root`): its records' paths are relative to it.
- **Measured on a fourth codebase**: 122 Go files from four repositories. No statement changes
  side; findings run 73–88%, EH-02 is a lead at 56%.

## 0.1.9

- **`last_error` is the last step's.** A step that recovered from an unanswered call kept
  reporting the old error beside `status: running`, and a reader took the recovered file for
  failed again. Each step now clears it; it is set only when that step stopped short.

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
