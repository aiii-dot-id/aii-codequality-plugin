# id.aiii.codequality

A **coverage-aware file-review selector** using a Jev-wire judge: TypeSafe's hosted
Jev, or an operator-configured local endpoint. It supplies a reading list and its
evidence, not a correctness certificate, call graph, or automatic fix. Source text
is sent to the configured judge; `sent_to` names the destination.

**0.1.14 is released** (T3-signed, 2026-10-27). This source is **0.1.15, unreleased**:
it corrects 0.1.14's regressions (see [CHANGELOG.md](CHANGELOG.md)) and awaits the release
ladder. The live semantic pilot is **NOT RUN**; ordering usefulness, model accuracy and
review-time savings are not established. See [RELEASING.md](RELEASING.md) for separate
gates and the discrepancies below.

## Operations and report views

| Operation | Effect | Purpose |
|---|---|---|
| `judge` | write.external | Assess supplied `items: [{ref, text, language?}]` within one invocation's call budget; no scan-owned restart state |
| `scan` | write.external | `start` a sandbox-folder scan, then `step` it; preserve per-battery progress |
| `advance` | write.external | Host-only scheduled step of the first unfinished scan, every 600 seconds when enabled |
| `models` | read.external | List offered model names; not inference or alias-resolution proof |
| `report` | read.internal | With no `scan_id`, list scans; otherwise select a report shape |

Example operation arguments (illustrative, not an executed scan):

```json
{"action":"start","path":"path/to/checkout","include_tests":true}
```

Use the returned ID for `scan {"action":"step","scan_id":"..."}`. Tests remain
excluded by default; include them explicitly for the pilot.

```json
{"scan_id":"...","shape":"review_queue","lane":"correctness","limit":20}
```

```json
{"scan_id":"...","shape":"coverage","limit":20}
```

New-shape responses contain `root`, `scan_id`, and a `review` object with `entries`,
`report_policy`, `generation`, `next_page_cursor`, `total_entries`,
`coverage_gap_count`, `coverage_shape`, completeness flags and a caveat. Paths are
relative to `root`. Keep both views: no allegation does not mean complete coverage.

- **`review_queue`**: one candidate per normalized path **and source digest**,
  combining allegations, ranges, provenance, assessment, selection reason and
  suggested next check. Different digests do not become one assessment. No inferred
  subsystem role is supplied.
- **`coverage`**: file gaps and subtree boundaries, including excluded scope.
  `children_not_enumerated` means no child count or inventory was established.
- **`summary`** (default): compatibility index/bands and lowest-index `worst`
  records, not distinct files. It links to review and coverage views.
- **`findings`**: individual chunk/battery evidence, `faults` descriptions, `root`,
  integer `cursor`/`next_cursor`, and `total_records`. `path`, `language` and
  `min_severity` filters apply here, not to the new queue shapes.

For new shapes, copy `review.next_page_cursor` to `page_cursor` with the same scan,
shape and lane; an empty next cursor ends that view. `limit` is 1–100 (default 20).
`page_bytes` is 4,096–200,000 (default 131,072) for entries; metadata is additional.
The opaque cursor binds checkpoint, the ranked prefix, the journal's length and query. On `STALE_CURSOR`,
restart at the first page: a running scan can change without adding a finding.
Do not reuse a findings integer cursor as a queue cursor. On
`REVIEW_ENTRY_TOO_LARGE`, use a larger allowed page or `findings` for individual
chunk evidence.

**Pagination cannot reach beyond the report-input cap.** A v2 report retains and
ranks a **1 MiB event prefix** of the journal and reads beyond it only the uncommitted
suffix after the checkpoint, which it validates (a torn tail fails closed); the part
between was validated when its checkpoint was committed and is not read. Report memory
and time are therefore bounded by the prefix, not the journal: on the 64 MiB guest
worker a report answers in 2.3–4.1 s on journals of 5, 16 and 47 MiB (0.1.14 read the
whole journal and trapped once it passed 2 MiB of real scan records). If the journal does not all fit, `review.report_complete=false`; totals and ranking cover that
prefix only, not the repository's top N. Summary/findings use `complete=false`
with an explanatory note. Legacy result input is bounded at 48 MiB. Raising
`page_bytes`, filtering or following every cursor does not recover omitted input.
The available operational route is a **new narrower scan**. Durable results remain
stored, but this API has no suffix-export cursor. Every shape, `review_queue`
and `coverage` included, carries the same `note` with that route whenever the report ranks only
a prefix.

## Candidate ordering, not measured superiority

Policy `severity-category-breadth-v1` has two independently queryable lanes:

- **Correctness/lifecycle**: EH and DF items, ST-03, and high-severity ID-* items.
  This is a review grouping, not a security assessment.
- **Maintainability/documentation**: remaining items, including GC-02
  (evaluator-directed text), which is not itself proof of a code defect.

Each lane sorts by maximum relevant catalogue severity (high, medium, low), then
number of distinct positive category IDs descending, then normalized path/digest.
No index or probability participates. Without `lane`, correctness comes first;
a file in both lanes appears once before the limit. Replay IDs and repeated
categories do not inflate breadth; distinct locations remain evidence. Breadth
counts **categories, not defects**. Larger files still have more opportunities to
trigger categories.

For example, high-severity EH-04 + DF-03 + DF-04 outranks EH-01 alone. That is the
policy, not evidence of greater practical value. Historical category samples do
not calibrate this model/backend. Coverage gaps sort non-excluded unknown/partial
entries first, then non-excluded `oversize`/`budget_pending` reasons, then excluded
entries, with path/digest ties. The walker currently marks oversize files excluded,
so they sort in the excluded group. Coverage order is not severity order.

## Coverage is not semantic confidence

Records use format **v2**, preprocessing `context-windows-v2`, catalogue
`general.v3` plus its digest, and threshold policy `noul-0.4-0.6-v1`. Valid raw
probabilities, expected/non-applicable IDs, source digest, window/battery identity,
ranges and model/backend provenance are retained.

| Assessment | Meaning |
|---|---|
| `assessed` | All applicable answers determinate under the input contract, with complete recorded file coverage |
| `partial` | Some determinate evidence, but questions, context, windows or completion evidence remain unresolved |
| `unknown` | No determinate coverage established, including legacy coverage never recorded |
| `excluded` | An encountered file was not assessed; its reason may describe intentional exclusion or a read/size limitation |

`has_findings` is separate: partial/unknown evidence may retain serious allegations.
With complete assessment and no positives, say **“no findings detected”**, not
“safe” or “clean.” The legacy `clean` band is compatibility arithmetic. An isolated
serious flag can coexist with a high index. File `legacy_index` is the minimum
retained record index; summary means remain line-weighted legacy record statistics.

The plugin interprets `p < .4` as negative, `.4 <= p < .6` as uncertain and
`p >= .6` as positive. Missing/invalid answers remain missing, not fabricated .5.
These are plugin thresholds, not model-emitted abstention labels. Probability is
neither severity nor calibrated bug likelihood. Localization confidence is neither.
Historical `lead` labels come from the fixed catalogue table and are applied at
report time; neither a lead nor a legacy “finding” is confirmed for this source.
For example, EH-01's historical sample is 36/41 and EH-04's 7/14; the historical
review covered 122 Go files across four repositories, not current model/backend
accuracy. Historical index studies do not validate this queue.

`inventory_complete`, `assessment_complete` and `report_complete` distinguish
discovery, checklist coverage and report input. A complete report can describe an
incomplete assessment. Inventory completion requires a completed v2 walk, complete
report input and no unresolved subtree boundary. A bounded prefix cannot establish
that an omitted suffix contains no gaps. `assessment_complete` additionally
requires complete checklist evidence under the declared context contract.

[CONTEXT-POLICY.md](CONTEXT-POLICY.md) explains local proof versus missing caller
and ownership evidence. ST-03, EH-04, DF-01, DF-03 and DF-04 are conservatively
context-limited on every applicable code window. An entirely answered file may
therefore remain partial. The model does not certify its own context completeness.

## Bounded inputs and source scope

Code questions receive redacted, comment-blanked `code`. Documentation questions
receive separate redacted `source` windows with comments **beside their code**,
not whole-file comments-only requests. Questions over identical state are batched;
code and contextual-doc states are not combined merely to save calls. Pattern
redaction does not guarantee that all sensitive text is found.

The raw-file default remains **128 KiB (131,072 bytes)**, configurable up to
**192 KiB (196,608 bytes)**; explicit lower settings remain respected. Larger files
are listed as unassessed coverage gaps and are not submitted. Expanded 1 MiB support
is outside this release. Its failing memory regressions remain in the experimental lane.
Saved experimental scans above this ceiling are report-only: start a new scan
within the supported bounds; the plugin never silently rewrites their cap.
Scheduled advancement skips report-only scans and returns their IDs and reasons,
so an old experimental scan cannot starve a newer supported scan. Direct `judge`
items also obey the configured raw byte cap, validated for the entire batch before
inference; the schema's character limit is a separate constraint. Read-only reports
remain available with invalid assessment settings, returning
`assessment_configuration_error` and omitting progress estimates until the settings
are corrected. Settings-read and evidence-read failures still fail, not look empty.
This is bounded whole-file preparation, not constant-memory
streaming or a measured guest peak-memory guarantee. Scans capture their raw cap
and `chunk_bytes`. Window bodies honor that byte bound; serialized checklist
requests (escaping, questions and overhead included) are checked against the
256 KiB host-request limit and windows reduced if necessary. Bytes do not prove
per-question or total model-token fit. Irreducible input errors are gaps, not
negative answers.

Go parsing supplies declaration boundaries where possible; other languages use a
labeled lexical fallback. Go parse failure is explicit. Oversized units split;
split/fallback uncertainty limits applicable questions. Masking preserves positions
and newlines; long-line fragments can share a line number. Optional localization
can choose `cannot_localize`, with at most 254 units plus that option. Invalid or
low-confidence choices, unsupported option counts and localization failure preserve
allegations without invented locations. Choice distributions are retained when
supplied; their absence is labeled. An unavailable judge (or a step's spent calls)
is not at first a location outcome: the file stays pending with its checklist evidence
and the next step asks for the location again. Once a whole retry round (`retry_budget`)
passes unanswered, the findings are recorded `localization_failed` and the scan moves on,
so optional evidence cannot hold the window's documentation battery and later files.

Scans apply language rules, nested `.gitignore`/`.gitattributes`, and caller
exclusions before submission. They exclude symlinks, nested repositories,
secret-named/generated/minified/binary files, detected NUL bytes, over-cap files and
(by default) tests. A file growing past the cap is not assessed as a prefix.
Unsupported languages and unreadable files remain visible by reason. Truncated
directory listings are boundaries: the FS interface cannot paginate them. Excluded
subtrees are not entered just to count children or find a negating child rule.

### Limited rule grammar — fail closed

Rules load from scan base through target directory, ancestor first, under a
**128 KiB combined raw-text budget** for both files across that chain. Complete
listing establishes absence. Unreadable, symlinked, malformed, unsupported,
over-budget or incompletely listed rules stop submission for the affected subtree.
No partially parsed rule set authorizes source egress.

`.gitignore` supports blank/comment lines, leading `!`, leading `/` anchoring,
trailing `/` directory patterns, printable ASCII literals and spaces, `*`, `?`,
character classes (`*.py[cod]`, `[a-z]`, negated `[!a]`/`[^a]`, never matching
`/`) and `**` as a whole path component. Bare names match below their scope;
slash patterns anchor there. Trailing whitespace is ignored. Escapes, quotes,
POSIX classes, non-ASCII patterns, dot path components and other `**` placements
are refused. Negation cannot resurrect a pruned ancestor. This is **not full Git
equivalence**.

`.gitattributes` is read for `linguist-vendored`, `linguist-generated` and
`linguist-documentation` only. Set or `=true` excludes; `-x`, `!x` or `=false`
clears that attribute alone, and each attribute takes its last matching line, as
in Git. Attributes with no exclusion meaning (`text`, `eol`, `-whitespace`, `diff`,
`merge`, `filter`, `binary`, `linguist-language`, `linguist-detectable`, macros
without linguist attributes) are ignored. Linguist forms the parser cannot
interpret are refused: other values, unknown `linguist-*` names, linguist
attributes inside a macro, and negated, directory-only or unsupported patterns on
a linguist line. Attributes match named paths, not implicit descendants. Correct unsupported rules
within approved scope or use another explicitly authorized scope; do not bypass
exclusions just to make a scan finish.

## Progress, model identity and retry limits

New scans use a private append-only coverage/per-battery journal, an initial marker
and two alternating checksummed checkpoint slots. Checklist evidence is written
before optional localization or later batteries. Replay deduplicates stable IDs;
durable progress is separate from the disposable inference cache. Resume re-reads
source and checks its digest: changed source becomes stale with
`fresh_assessment_required`, not blended old/new evidence. Start a new scan after
stabilizing source. A scan is not an atomic repository snapshot.

Journal bounds are 48 MiB total and 250 KiB per encoded event; checkpoints are
250 KiB each. Storage exhaustion stops with `storage_cap`, preserving evidence
and requiring a narrower scan. Invalid/truncated journal tails fail closed, not
silently repaired. Two slots allow loading the newest valid checkpoint and reporting
a damaged alternative. Writers must be serialized. **No transaction, fsync,
power-loss or concurrent-writer guarantee is claimed beyond the host Store
contract.** Retention removes journals/checkpoints with old scans (eight by default).

**Old scans are report-only.** Legacy findings remain readable with
`legacy_coverage_unknown`; a last visible chunk cannot establish completion.
Stepping a pre-v2 scan returns `SCAN_RESTART_REQUIRED`. Start a new scan for the
new pipeline; reporting does not rewrite old data.

For **scans**, identity binds backend, full endpoint (including local base path),
requested name and concrete response name before dependent progress:

- Hosted `jev-latest`/`jev-preview` bypass cache resolution for their
  first ordinary assessment reply, then pin its concrete `jev-MAJOR.MINOR.PATCH`
  identity for subsequent requests/cache. Explicit versioned IDs can use cache
  immediately. `/v1/models` lists names, not alias targets. This conservative
  grammar is not a claim to accept every future name.
- Local scans take the configured name as the operator's explicit identity
  assertion and require matching response attribution; known moving Jev aliases
  are refused. A matching string **does not prove fixed weights**. Establish the
  service's identity contract before relying on cache or pilot reproducibility.
- Configuration drift, missing identity or mismatch blocks new assessment, leaving
  earlier work reportable. Correct the cause and start a new scan.

Rate-limit/availability failures (429, 529 and other 5xx/transport failures) use
persisted bounded exponential backoff, at most `retry_budget` (default eight)
retries after the initial call, without sleeping in the guest. SDK `RetryAfterS` may extend the delay; waits
over one day block instead of retrying early. Before the deadline a step returns
`retry_not_before_ms` without inference. An outage outlasting the retry budget
ends that round of backoff, not the scan: it stays `judge_unavailable` and the
next step (scheduled or manual) retries. A judge refusing one text — a 403 page
from Jev's edge, or any other 4xx that is not an authentication failure
(400/404/413/415/422 included) — records that file as `refused_by_judge` and the
scan moves on, as before 0.1.14. Only an authentication failure (401, or a 403
answered in JSON) stops the step, as `JUDGE_AUTHENTICATION_REQUIRED` with the
operator's route in `last_error`; the file stays pending and the scan continues
once the key works. A refused optional localization request keeps its checklist
evidence, marked `localization_failed`.

**Known limit — refusal versus authentication.** The split rests on the status and on
whether a 403's body is JSON; it has not been checked against bodies a real Jev returns.
A firewall answering 403 in JSON would read as an authentication stop, and a revoked key
answered with an HTML 403 would mark every file `refused_by_judge` and finish `done`. A
summary therefore states `refused_by_judge` and how many files were judged whenever the
judge refused any; a scan that judged nothing is not complete coverage.
The helper recognizes typed `max_tokens_exceeded` as `model_context_limit`, but the
current scan adapter passes no typed service code: **HTTP 400 is
`request_validation`, not a proven token-limit diagnosis**. It does not search
arbitrary response prose to invent that code.

### Integration limits

Direct `judge` uses fresh invocation-level attribution and returns retained partial
records with `complete=false` and `assessment_error` when later assessment fails.
Only scans persist retry/pin state across invocations. A model pin is checkpointed
against the last committed cursor, never transient mid-file counters; a runtime
checkpoint failure stops inference without clearing live traversal state.

The report-prefix limit above remains: use narrower scans for omitted data.
Subtree boundaries conservatively remain unknown until explicitly resolved.
Oversized, unreadable and unsupported source files are unknown, not intentional
exclusions. Both record metadata and actual question instructions name the
original physical line range. HTTP 400 lacks a verified typed-code envelope and
is conservatively reported as request validation rather than diagnosed from prose.

Expanded 1 MiB support is deferred; it remains a known
failing experiment, not a passing release claim. See `validation/guest/README.md`
for candidate-specific runtime evidence. The narrowed release must pass repeated
multi-file scans and reports on one 64 MiB worker using the standard toolchain
at its declared 128 KiB default and 192 KiB maximum. A lower cap alone does not
establish readiness.

## Configuration and consent

The declared host floor is AII OS 0.1.10; actual candidate compatibility is a gate,
not established by that declaration. On the plugin card:

- `backend=jev`: paste a Jev key; the host grants it only to `api.typesafe.ai:443`.
  Attaching a stored key to a public host requires a review-proven package (T2/T3).
- `backend=local`: set `local_judge_url` to an http(s) base serving `/v1/systemone`
  and `/v1/models`, and grant the address under `net.local`. No Jev key is sent.
- Grant files for sandbox scans. Identity data, keys, store and configuration
  remain out of scope. Listing a model does not grant source egress.

`judge`, `scan` and `advance` are `unconfirmed_by_design`: destination settings and
grants supply consent, not a separate confirmation each call. Contextual windows
authorize neither new destinations nor excluded files. A hosted pilot needs
explicit source-egress and cost approval.

| Setting | Default | Range / meaning |
|---|---|---|
| `backend` | `jev` | `jev` or `local` |
| `api_key` | unset | Hosted credential handle managed by host |
| `local_judge_url` | empty | Local http(s) base; no credentials/query/fragment |
| `model` | `jev-latest` | Judge-listed name; scan pin restrictions above |
| `step_ms` | 24000 | 1000–85000 ms shared call-time budget |
| `max_call_ms` | 8000 | 500–85000 ms maximum per call |
| `max_calls_per_step` | 12 | 1–64, also bounds direct judge |
| `retry_calls` | 3 | 1–64 reduced retry-step call budget |
| `chunk_bytes` | 29000 | 2048–131072; captured by scan; not token fit |
| `max_file_bytes` | 131072 | 1024–196608 raw bytes; captured by scan |
| `keep_scans` | 8 | 1–256 |
| `cache_max_bytes` | 33554432 | 1–256 MiB total over sixteen shards |
| `retry_budget` | 8 | 0–32 retries of an unavailable judge, waits doubling to 300 s, before they start again; an outage alone does not end a scan, though a Retry-After over a day blocks it |
| `scheduled_steps` | true | Host advances every 600 s; manual steps still honor backoff |

Caches bind exact outgoing state/questions, battery, preprocessing, catalogue and
model/backend identity; changing adjacent code changes the documentation key.
Cache write failure does not erase a retained answer. Shards and whole-file
processing allocate memory: byte caps do not prove guest memory fit. Remaining-step
estimates are estimates, not inference-cost promises.

## Development and evaluation

Use the frozen-candidate native **Go 1.27.3**, separate TinyGo guest and isolated
no-live-provider host gates in [RELEASING.md](RELEASING.md). Native tests are not
guest proof; fake judges are not semantic validation. The live pilot is **NOT
RUN**. Its identical-record ranking baseline, unique-issue deduplication,
per-hour/per-file metrics and separate diagnostics are specified there.
`cq/annex_l.json` derives from third-party data; see NOTICE.
