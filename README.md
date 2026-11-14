# id.aiii.codequality

A **coverage-aware file-review selector** using a Jev-wire judge: TypeSafe's hosted
Jev, or an operator-configured local endpoint. It supplies a reading list and its
evidence, not a correctness certificate, call graph, or automatic fix. Source text
is sent to the configured judge; `sent_to` names the destination.

This source is **0.2.2**, the version AII OS 0.2.2 carries. The release before it is 0.1.15:
it carries 0.1.15 and the cache, report and scan rebuilt since (see [CHANGELOG.md](CHANGELOG.md)),
and what it is known not to do is under "Known limits of 0.2.2" below. The live semantic pilot is **NOT RUN**; ordering usefulness, model accuracy and
review-time savings are not established. See [RELEASING.md](RELEASING.md) for separate
gates and the discrepancies below.

**Labels.** `0.2.0-r1` to `r4`, in this file, the changelog and the plugin's own messages, name
test builds made between 0.1.15 and this release; none was published. "The first 0.2.0 build" is
a package signed on 2026-11-12 and installed in one test slot only. 0.2.1 is a package signed
on 2026-11-14 for a first real scan. Neither was published. Each keeps its version, and this
release is 0.2.2: signed bytes are not replaced under a version
([RELEASING.md](RELEASING.md)). A message saying a scan was made "before 0.2.0-r2" therefore
means by 0.1.x, by the first 0.2.0 build or by r1.

## Operations and report views

| Operation | Effect | Purpose |
|---|---|---|
| `judge` | write.external | Assess supplied `items: [{ref, text, language?}]` within one invocation's call budget; no scan-owned restart state |
| `scan` | write.external | `start` a sandbox-folder scan, then `step` it; preserve per-battery progress |
| `advance` | write.external | Host-only scheduled step of the first unfinished scan when enabled; the host calls again at once while a step saves work, so its 600-second period is only the slowest pace |
| `models` | read.external | List offered model names; not inference or alias-resolution proof |
| `report` | read.internal | With no `scan_id`, list scans; otherwise select a report shape |

Example operation arguments (illustrative, not an executed scan):

```json
{"action":"start","path":"path/to/checkout","include_tests":true}
```

Use the returned ID for `scan {"action":"step","scan_id":"..."}`. Tests remain
excluded by default; include them explicitly for the pilot.

```json
{"scan_id":"...","shape":"triage"}
```

```json
{"scan_id":"...","shape":"review_queue","path":"internal/conversation/loop.go"}
```

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

- **`triage`**: one row per file, under 500 bytes for a path under 120: its tier, the
  strongest finding that puts it there (ID, fault, p, unit, lines), its correctness,
  maintainability and stated-intent counts, and its assessment. Correctness lane and
  pinned tier by default, 50 rows a page. Each row also carries the file's languages, its
  size and size finding, its slop count with its two most frequent kinds, and its intent
  counts. The header lists `tier_counts` for every tier, `out_of_scope` (each file not
  judged for its kind, once, with the reason), and `not_assessed_by_earlier_version` for
  an old scan's size gaps. `lane=size` lists the files with a size finding by line count;
  `lane=slop` lists the files with slop findings by count. A row's whole evidence is
  `review_queue` with `path` set to its ref.
- **`review_queue`**: one candidate per normalized path **and source digest**,
  combining allegations, ranges, provenance, assessment, selection reason and
  suggested next check. Different digests do not become one assessment. No inferred
  subsystem role is supplied.
- **`coverage`**: file gaps and subtree boundaries, including excluded scope.
  `children_not_enumerated` means no child count or inventory was established.
- **`summary`** (default): compatibility index/bands and lowest-index `worst`
  records, not distinct files (`worst_basis` says so: the legacy maintainability
  index), and `top_correctness`: up to `worst` files of the pinned tier, each with the
  finding that puts it there. It links to review and coverage views.
- **`findings`**: individual chunk/battery evidence, `faults` descriptions, `root`,
  integer `cursor`/`next_cursor`, and `total_records`. `language` and
  `min_severity` filters apply here alone; `path` (a file, folder or glob) applies here
  and to `review_queue`, `coverage` and `triage`.

For new shapes, copy `review.next_page_cursor` to `page_cursor` with the same scan,
shape, lane, tier and path; an empty next cursor ends that view. `limit` is 1–100
(default 20; triage 50).
`page_bytes` is 4,096–200,000 (default 131,072) for entries; metadata is additional.
The opaque cursor binds the scan's checkpoint, the shape, the lane and the last entry shown. On `STALE_CURSOR`,
restart at the first page: a running scan can change without adding a finding.
Do not reuse a findings integer cursor as a queue cursor. A page cursor also binds
`list_all` and the thresholds applied: a change of either needs a fresh first page. A file
whose review is larger than the page is listed alone on its page with as many of its findings,
then its slop findings, as fit, and `omitted` says how many it left out and the `findings`
report that reads them whole; `REVIEW_ENTRY_TOO_LARGE` is left for an entry that fits with
none (a page smaller than its own path). A review entry's `location_answer` is the choice and
its confidence; the distribution over every unit stays in the record, which `findings` shows.
A `findings` page holds at least one record and never cuts one: a record larger than
`page_bytes` is shown whole and `page_bytes_exceeded` says so (a record is at most 250 KiB).

**A report covers the whole scan.** Every shape reads the scan's whole journal one file at a
time: a scan appends one file's events together, so each file is one run in the journal, and a
report keeps only the entries its page needs. Its memory is one file's events and a page,
whatever the journal's size, and `review.report_complete` is true: every judged file is ranked
or counted. A journal in which a file's events are not one run is refused
(`SCAN_JOURNAL_INTERLEAVED`), never ranked twice. While a scan runs, coverage lists what it has
yet to reach, from its checkpoint. Legacy (pre-v2) result input is still bounded at 48 MiB.

**Memory.** Nothing the plugin reads stays in its memory past its use: the answer cache is read a
page at a time (256 files of a share each), a host reply is freed when its call returns, and no
report keeps more than a page. A full scan of aii-os, its tests included, runs on the host's worker
at a 32 MiB ceiling with a full cache (measured 2026-11-10 on the plugin's guest harness).

**A folder of any size.** The walk reads each folder whole, page by page (AII OS 0.2.2's `fs.list`
`after`; the plugin needs host 0.2.2). The scan's checkpoint holds the paths still to
visit, in 250 KiB: about 6,000 files of ordinary names in one folder (about 940 of 255-byte
names). Past it the scan stops with `storage_cap`, naming the folder; its subfolders scanned
apart, or some of its files excluded, fit.

## Candidate ordering, not measured superiority

Policy `evidence-tier-v1` has two independently queryable lanes:

- **Correctness/lifecycle**: EH and DF items, ST-03, and high-severity ID-* items.
  This is a review grouping, not a security assessment.
- **Maintainability/documentation**: remaining items, including GC-02
  (evaluator-directed text), which is not itself proof of a code defect.

**The correctness lane, and the plain queue, are tiered by evidence.** A file is **pinned** when one of its
correctness findings has p at least 0.80, was placed in a unit with at least 0.60
confidence, is not context-limited, and states no intent; **unpinned** when its
correctness findings are any weaker; **intent_stated** when every correctness finding
it has carries a comment saying the error is ignored on purpose. Within a tier files
sort by the strongest qualifying p, then the number of qualifying categories, then
path. `tier` selects one (pinned by default, or `all`); every page counts all three.
**Both bounds are first picks, not yet measured** against hand-read verdicts.

**Stated intent labels a finding and never removes one.** For each EH-01, EH-02 or
EH-03 flag, placed in a unit or not (an unplaced flag is asked about its window), the
scan asks one further question over the window's text with its comments: does a comment
there say the error is ignored on purpose? A yes sets `intent_stated` (with
`intent_stated_p`) only when the same window's documentation battery has answered no to
GC-02 (a comment written to sway the evaluator): `intent_guard` says clear, fired or
unknown, and anything but clear leaves the tier as it was, so such a comment cannot lower
a finding. A flag with no saved answer says the word none and why, in `intent_none`:
`none: not asked`, `none: failed` (asked, no usable answer) or `none: sidecar
unreadable`; every file, page and summary counts
`intent_asked`, `intent_saved` and `intent_failed`, and of the saved, those the judge
answered yes to by their window's guard: `intent_yes_guard_clear`, `intent_yes_guard_fired`
and `intent_yes_guard_unknown`, which sum to the yeses. Each such flag says whose claim it is
in `intent_claim` ("Judge says a comment describes a deliberate omission; ...") and, for a
fired or unknown guard, "priority unchanged". A saved outcome is progress: a resumed
step never asks it again. The finding is kept, reported and counted in the index
as before; only its file's tier changes. The question is outside the catalogue and its
digest, so earlier records and cached answers stay valid. Its answers are kept beside
the journal (`scans/<id>.intent.jsonl`), never in it, because the first 0.2.0 build reads a journal
strictly; a scan started before 0.2.0-r1 has no such file and asks no intent question.
A missing intent file reports every finding's intent as unknown; one that cannot be read
does the same and says so (`intent_note`).

The maintainability lane sorts by maximum relevant catalogue severity (high, medium,
low), then number of distinct positive category IDs descending, then normalized
path/digest. Without `lane`, correctness comes first;
a file in both lanes appears once before the limit. Replay IDs and repeated
categories do not inflate breadth; distinct locations remain evidence. Breadth
counts **categories, not defects**. Larger files still have more opportunities to
trigger categories.

For example, high-severity EH-04 + DF-03 + DF-04 outranks EH-01 alone. That is the
policy, not evidence of greater practical value. Historical category samples do
not calibrate this model/backend. Coverage gaps sort non-excluded unknown/partial
entries first, then non-excluded `not_assessed_by_earlier_version`/`budget_pending`
reasons, then excluded entries, with path/digest ties. Coverage order is not severity
order.

**Size and slop are maintainability findings, and never move a correctness tier.** Each
judged, non-test file's lines, and for Go its top-level funcs and types, are recorded with
no judge call: over `large_file_lines` it is `large_file` (moderate), over
`very_large_file_lines` `very_large_file` (high). The slop findings come three ways.
*Deterministic*, from the file's text with no call: SL-DUP (a block of `dup_lines`
normalised lines repeated in the file), SL-NEST (nesting deeper than `nesting`: an
else-if counts at its chain's level, a function literal counts as a level and the finding
says "through a function literal"), SL-PARAMS (more than `params` parameters), SL-REWRAP
(fmt.Errorf of a lone %w, %v or %s, or errors.New of x.Error(): it reads no types, so it
does not know the value is an error), SL-STUB (TODO or FIXME where it begins a comment,
not quoted inside one; "not implemented"), SL-UNREACH (a statement after return, panic
or a jump), SL-FORWARD (a same-name wrapper: a whole body passing its parameters to a
function of its own name in another case, Start to start or readFile to
atomicfile.ReadFile), SL-UNUSEDPARAM
(not in a file for one platform, by a GOOS or GOARCH suffix or a build constraint, whose
signatures are its twins') and SL-UNUSEDFUNC ("no caller in the package's non-test code";
"called only from tests" when tests were scanned and call it). Every other function
whose whole body passes its parameters to one call, an adapter passing to its own name
through a value or a package included, is counted, not listed (see below). The last two need the whole
package: when a non-test Go file of the directory was left out, they are not applied and
the page says why. Methods are left out of both (a method can satisfy an interface the
package never names); a name reached by `//go:linkname` counts as referred to; Go cannot
reach an unexported function by reflection. Go runs every rule; brace languages and
Python run SL-DUP, SL-NEST and SL-STUB; others SL-DUP and SL-STUB; the start reply lists
them per language. A Go file is read as written, its licence, build tag and package
comment included. A declaration larger than one segment (a function, or a type or value literal), or a file that does not parse, is
not read by the Go tree rules; the file's evidence says which in `not_checked`, it is not
counted as a finding, and while any file of a package has such a range the package's
no-caller rules are not applied and the page says why. So with a file of more than 96 KiB of
names (every unexported function, name referred to and unused parameter), whose shape would
not fit one record; and a file whose shape is still over 200 KiB without them is unchecked
whole. Neither happens in aii-os `internal` (its most is 20,444 bytes of names). *Judged*, one more call a window over its text with
comments (slop-v1: SL-NARRATE, SL-IMPOSSIBLE, SL-INDIRECT, SL-MIXED), used only where
GC-02 answered no, as intent, each finding naming its window's lines and no narrower line
(the judge does not place it); and one call a file with more than one top-level function,
over its outline of names, kinds, signatures and line counts (outline-v1: SL-FILEJOBS,
SL-FILEWAYS), "judged from the outline, not the bodies; the judge does not name groups".
*From the catalogue*: DC-04, ST-02, RD-04, ST-04 and RD-02, read and shown, never asked
again. Every threshold is in `finding_thresholds` and applied when a report is read, so a
change needs no rescan and no call; the summary and the size and slop lanes print the five
values applied. Every review shape and lane carries the package rules' findings, a file's
detail by `path` included, and the summary counts each file's size from its own record.
The summary's `slop.questions` and a file's `slop_questions` count the slop questions
asked, answered and failed. A scan made before 0.2.0-r2 has no size or slop data and says
so. The start reply gives the calls for each started chunk of a file, not one figure a
file: a file's cost grows with its size. Once a file is judged, a scan's view shows its
calls per judged file (with the intent and slop calls apart) beside what that rate gives
for the windows those files had. A call the plugin refused before sending it, its step's
calls spent or a retry not yet due, is counted as none. An argument a report's shape does not use is named in
its result's `ignored_arguments`. **None of these is measured yet:** the thresholds are
first picks, and the judged questions' precision is unknown until a hand-read sample.

**Counted, not listed.** Three kinds are counted in the default view and not listed: SL-FORWARD
for a pass-through function that is not a same-name wrapper (93 of 112 on aii-os `internal`),
and RD-04 and ST-04, the catalogue's two kinds least often worth a look (measured precision 4 of
26 and 1 of 25; 0 of 10 in a hand-read of a live scan). Every review page but coverage, and the
summary's `slop`, give `counted_not_listed` (each kind, 0 included) with
`counted_not_listed_words`, and each file its own. RD-04 and ST-04 do not rank a file, and a file
with no other finding in a lane is not in that lane's queue. `list_all` on a review_queue or
triage report lists every one. The scan's records keep them all, and the summary's `lead_counts`
count them.

## Coverage is not semantic confidence

A summary counts each judged window, and its lines, once. A window with no code whose
comments were judged is counted apart, as `no_code_windows` on its file and in the
summary, and is not a gap. A large file that changes between steps, in a part already
judged or past its planned end, is planned again as it now reads; what was judged before
is stale (`source_changed`).

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
review covered 122 Go files in four samples from three repositories, not current
model/backend accuracy. Historical index studies do not validate this queue.

`inventory_complete`, `assessment_complete` and `report_complete` distinguish
discovery, checklist coverage and report input. A complete report can describe an
incomplete assessment. Inventory completion requires a completed v2 walk, complete
report input and no unresolved subtree boundary; every report reads the whole journal, so
no part of a scan goes unread. `assessment_complete` additionally
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

**Scope is a file's kind, never a size below 1 MiB.** A file is judged when it is text
(no NUL byte, valid UTF-8), in a recognised language, written by a person (not generated:
a generated-code marker in its first 40 lines; not minified: a mean line length over 110
bytes, for JavaScript, TypeScript, CSS, SCSS, JSON and HTML; `include_generated` judges
both), and at most 1 MiB (1,048,576 bytes, a constant: "larger than 1 MiB: not treated
as a source file"). Anything else is out of scope, not an error: listed once with its
reason and costing no call. So is what Go's tools ignore (`ignored_dir`): a folder named
`testdata` or beginning with `.`, named once with nothing in it read, and a Go file in a folder
beginning with `_`, named itself; that folder's other files are judged, since `_internal` or
`__mocks__` is source in Python or JavaScript. A file or folder whose name holds a line feed, a
carriage return or a NUL is counted in the scan's exclusions as `name_not_recordable` and
never read: every record names its file by a single-line path. A file larger than one 192 KiB segment is read and judged a
segment at a time, cut only where the lexer is at top level (before a Go declaration, or
at a blank line elsewhere) and never inside a private-key block; its place, with a hash per
segment, is checkpointed between segments, so a step resumes it and a changed file is
planned again. A file's languages (HTML's script and style, Markdown's fenced code) ride
on its requests as a hint and label its records; a one-language file is asked exactly as
before, so its cached answers stay valid. Measured on the guest, one worker: a 180,444-byte
Go file needs a 24 MiB ceiling, a 1 MiB one 48 MiB (the host's default is 64 MiB): the
heap grows over the steps of a file in one worker, not with the segment alone. An earlier
version's size gap reads as `not_assessed_by_earlier_version`.
Saved experimental scans above the old 192 KiB cap are report-only: start a new scan;
the plugin never silently rewrites their cap.
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

`advance` also tells the host whether to call again before the next interval. `more_work` is true
while a scan a step can advance is unfinished. `progress` is a count that rises only when the step
saved work (the journal's saved length); a step that saved nothing
gives none. `retry_not_before` is the earliest time the judge may be asked again, in RFC 3339. A
host that knows these continues an unfinished scan a few seconds after each step; a host that does
not ignores them and calls at its interval. The step's own progress in words is `scan_progress`.

Go parsing supplies declaration boundaries where possible; other languages use a
labeled lexical fallback. Go parse failure is explicit. Units larger than a window are split;
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
Unsupported languages and unreadable files remain visible by reason. A directory
listing that fails or is cut short is a boundary, not an empty folder. Excluded
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

A scan's room is fixed at its start: the 64 MiB quota less every file kept, the cache's
room to grow and a reserve. Its journal holds at most seven eighths of it, and its side
file whatever the journal has not used; an event is at most 250 KiB and a checkpoint
250 KiB. A whole scan of aii-os `internal` (972 files judged) writes 18.4 MB in all when
every question is answered yes and 8.4 MB when every one is answered no, measured with a
stand-in judge; a fresh slot's room is about 46 MiB. Storage exhaustion stops with
`storage_cap`, preserving evidence. A scan an earlier version stopped there with its walk
in place, because its side file had filled its fixed eighth, is continued by the next
`scan action=step` asked for it; the schedule steps only unfinished scans. Invalid/truncated journal tails fail closed, not
silently repaired. Two slots allow loading the newest valid checkpoint and reporting
a damaged alternative. Writers must be serialized. **No transaction, fsync,
power-loss or concurrent-writer guarantee is claimed beyond the host Store
contract.**

**Scans persist and expire by age.** A scan is kept until `scan_retention_days` (30)
after its last write, read from its files' host times, so reading a report never extends
it; a copy or restore that changes those times can extend it, never shorten it. An expired
scan is removed at a start or a tick, oldest first, and named in that reply
(`expired_removed`: its `scan_id`, `finished_at` (its last write) and `expires_at`). No scan is removed by count or by name, and a start short of room
refuses, naming the scans held with their sizes and expiry. `scan action=remove` removes
the scans named in `scan_ids`. Every id is checked before the first removal: one that names
no scan, or one named twice, refuses the call with nothing removed. A storage error after
that can leave some of them removed, and the call answers with the error, not with what it
removed. `report` with no scan_id lists each
scan's `expires_at` and the retention; a `keep_scans` still set on the card is named and
ignored.

**What a scan view says.** `max_files` counts the files **judged**; a file skipped (a
test, an exclusion, a file out of scope) counts toward `max_files_examined`, twenty times
`max_files`, so a walk of skipped files still ends. The stops are `file_cap_reached`
and `examined_cap_reached`; a scan the first 0.2.0 build saved as `budget_exhausted` reads as
`file_cap_reached`. While a scan runs its view gives `files_pending_known` (files listed
and not yet examined) and `dirs_unlisted`, facts rather than an estimate of steps, and
once a file is judged `calls_per_judged_file` (calls alone, and with cache hits). A
start's reply says what a judged file costs before caching. A scan this version will not
step lists as `legacy_report_only` while its saved status is unfinished; nothing steps
it, the schedule included, so it spends nothing.

**Rolling back to the first 0.2.0 build or r1.** Both read every journal this version writes (held by a
test that compiles the first build's own decoder from its commit): intent, slop, outline and shape
data are all in the side file (`scans/<id>.intent.jsonl`), never the journal. An older
version ignores the side file, so its reports show none of them; it keeps a scan's state
without the fields it does not know (a large file's cursor among them: it then reads that
file whole, which its own size cap skips); and it reads this version's stop words as an
ended scan of an unknown stop. **An older version still prunes by `keep_scans` in the
order of the scans' names**: clear `keep_scans` on the card before a rollback. A scan an
older version prunes leaves its side file behind, counted in the room a later start
finds.

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

A folder the scan chose to leave out (`.gitignore`, the caller's exclude, a global or language
folder, a nested repository, a symlink) is an exclusion with its reason; one it could not list or
read stays unknown.
Unreadable files are unknown; files out of scope by their kind are exclusions with their
reason. Both record metadata and actual question instructions name the
original physical line range. HTTP 400 lacks a verified typed-code envelope and
is conservatively reported as request validation rather than diagnosed from prose.

See `validation/guest/measure_test.go` (tag `measure`) for the memory and call figures above.
`validation/guest/README.md` describes the harness. It was last revised in October 2026,
before 0.1.15 was published, so it says nothing of this release's runs. The published tree
carries both files: the README in the edition the release tools supply for it, the test with
its comments removed. That the test is there does not show that the figures reproduce from
the published tree: it was not run there.

The three control scripts in `validation/` (`negative_controls.py`,
`narrow_negative_controls.py`, `scope_negative_controls.py`) were also written in October 2026
and were not run for this release. Read against this source, not run:
`narrow_negative_controls.py` and `scope_negative_controls.py` each look first for a line
that is no longer there (in `config.go` and in `main.go`), and by their own check stop before
making their first change or running its test. `narrow_negative_controls.py` stops before it
writes anything. `scope_negative_controls.py` writes the `main.go` it read at its start back
as it exits, so an edit made to `main.go` while it ran would be overwritten.

## Known limits of 0.2.2

**After a worker ends in the middle of a scan, or a save to the host's storage fails,** the
plugin recovers imperfectly. None of the four below was produced by a run through the plugin's
entry point; each was found by reading the source.

- A scan can become unreportable. If the worker ends just after a part-judged large file is
  closed, and that file's folder is back in scope (or was cut short at its listing) before the
  retry, the scan resumes the file after its folder's row, and no report reads that journal.
  Start the scan again.
- A file can be retried more often than the limit says: a save that fails while the scan leaves
  a file loses the count of worker endings against it.
- Package rules (SL-UNUSEDFUNC and SL-UNUSEDPARAM) can be withheld from a package that was read
  whole, if the worker ended between the package's marks and its closing row. The effect is
  missing findings, not false ones. The report names the package with the rules withheld, as
  it does for any Go file left out; it cannot say that the mark was left by an interrupted
  write.
- A file given up after two ended workers gets a not-finished row with no digest. The report
  does not join it to its windows that were judged.

**A step has no check of elapsed time.** A step ends when it has used its judge calls or
examined 32 files, judged or left out, whichever comes first; neither is the step's time on
the clock. 0.2.1 let a step examine 400 files, and over a tree judged before, where the
answers come from the cache and cost no judge call, scheduled advances of a real scan ran past
the host's 90 s limit and had their worker ended. Still not counted: a folder's listing, a
slow save, and a file planned in more than one segment, whose segments answered from the
cache are all worked through before the count of files is looked at again. So a step can
still run past the limit and have its worker ended; the four limits above then apply. The
number 32 comes from that one scan's rate, about 0.8 s for a file examined; when this was
written no step under it had been timed.

**Many kept scans.** A start checks that the six files its scan can come to hold fit beside
the entries the scans folder holds now. It does not count the attempt record that an older,
unfinished scan can still leave. With the folder close to the 1,024 entries the plugin can
list, several scans can each be admitted and together pass it. From then on the folder's
listing comes back cut short, and a start, a removal, an enabled scheduled tick and a `report`
with no scan_id each answer `SCANS_UNLISTABLE`. A removal lists the folder before it deletes
anything, so neither a removal nor the expiry of old scans frees the room once that has happened.
`scan action=step` with a scan_id does not list the folder. When such a step finishes its
scan's current file or gives it up, it deletes that scan's attempt record if there is one.
That is one entry, so a folder one entry over can be listed again. A step can spend judge
calls. Remove scans that are no longer needed with `scan action=remove` before the folder
fills. All of this is traced in the source; no run produced it.

**What the coverage page leaves out.** While a folder that could not be listed waits to be tried
again, the report's coverage page names it among the gaps with the reason code
`unlistable_dir`; the host's own error is in the scan's status, as `last_error`, and not on
that page. One case differs: if a large file of that folder is part judged when the folder
cannot be read, no row is written for the folder until it can be read again, and only
`last_error` names it. A file's count of completed windows is a lower bound: it counts windows
judged whole, so a window that was interrupted is not counted. Both are readings of the source,
not runs.

**What no run shows.** The tests of recovery after an ended worker sit at the report's reader and
inside the package: no test ends a worker between two writes through the plugin's entry point.
The size of the state a step saves was measured with a made-up state, not with one a judge
produced. No test reads a report's second page after a closing row was written twice.

**A scan saved by an earlier build.** A scan saved with this build's record version (the last
test build, r4, wrote that version) is resumed, not started again, and it keeps the folder
listings the earlier build saved. Two repairs in this release act where a folder is listed: a Go
file that is a symlink is marked, so its package is not taken as read whole; and a folder left
out after its listing takes its queued subfolders with it, so it is counted once. The mark is
made only where this build lists a folder: a folder an earlier build listed keeps its files as
that build queued them, and a symlinked Go file among them gets no mark. The second repair
reaches further: when this build comes back to a folder an earlier build listed and finds it
left out, the subfolders still queued under it are dropped then. Counts already saved stay as
they were recorded. Start a new scan to have both repairs throughout. This is a reading of the
source; no resumed scan was run to show it. A cursor or folder list saved past this build's
room is not closed by
name at resume, and a journal where a folder's row sits between a file's windows stays
unreadable. A scan saved by test build r2 or r3 is to be started again, not resumed.

**A symlinked Go file whose name cannot be recorded.** The mark above names the file. A symlink
whose name holds a line feed, a carriage return or a NUL cannot be named in a record, so it gets
no mark, and the no-caller rules can still be applied to its package.

**The judge's value is not shown.** One scan of aii-os `internal` against the hosted judge (test
build r4: 1,222 files judged, none refused) was hand-read in three files. The eight allegations
there were read against the source by two readers separately, and a third ruled on the three
they had labelled differently: none of the eight is a defect. Eight allegations from three
files is not a rate, in either direction. Until a larger hand-read sample exists, treat every
allegation as a place to read, not as a finding.

## Configuration and consent

The declared host floor is AII OS 0.2.2; actual candidate compatibility is a gate,
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
| `keep_scans` | unset | Retired and ignored; named when set. Clear it: an older version still removes scans by it |
| `finding_thresholds` | `large_file_lines=1000 very_large_file_lines=2000 dup_lines=6 nesting=4 params=6` | key=value pairs applied when a report is read; a key left out takes its default; an unknown key is refused |
| `scan_retention_days` | 30 | 1–3650 days after a scan's last write |
| `cache_max_bytes` | 16777216 | 1–256 MiB over this version's 256 answer files. The files 0.1.15 kept its answers in are outside it |
| `retry_budget` | 8 | 0–32 retries of an unavailable judge, waits doubling to 300 s, before they start again; an outage alone does not end a scan, though a Retry-After over a day blocks it |
| `scheduled_steps` | true | Host advances every 600 s; manual steps still honor backoff |

Caches bind exact outgoing state/questions, battery, preprocessing, catalogue and
model/backend identity; changing adjacent code changes the documentation key.
Cache write failure does not erase a retained answer. The answers 0.1.15 kept stay in
the sixteen files it wrote (`cache/<one hex digit>.jsonl`): a lookup that finds nothing
in this version's files reads the one its key names, and nothing writes, moves or
removes them, so a rollback to 0.1.15 finds them as it left them. Answers asked since
are in this version's own files, which 0.1.15 does not read. The earlier files count
against the room a scan may use and are outside `cache_max_bytes`. What the cache keeps
it knows from one whole listing of its folder, and each file it must read it reads to
the end. A listing that fails or is cut short, or a file that cannot be read to its end,
is not taken for "nothing kept": the step stops as `CACHE_UNREADABLE`, the judge is not
asked for what that lookup covers, what was already recorded for the file stands, the file
stays pending, and the next step goes on from there. A shard is rewritten only from a tail
read to its end. Shards and whole-file
processing allocate memory: byte caps do not prove guest memory fit. Remaining-step
estimates are estimates, not inference-cost promises.

## Development and evaluation

Use the frozen-candidate native **Go 1.27.3**, separate TinyGo guest and isolated
no-live-provider host gates in [RELEASING.md](RELEASING.md). Native tests are not
guest proof; fake judges are not semantic validation. The live pilot is **NOT
RUN**. Its identical-record ranking baseline, unique-issue deduplication,
per-hour/per-file metrics and separate diagnostics are specified there.
`cq/annex_l.json` derives from third-party data; see NOTICE.
