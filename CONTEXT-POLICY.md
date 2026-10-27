# Context policy: evidence is not a whole-program verdict

This describes `context-windows-v2` in `cq/pipeline.go`, `cq/windows.go` and the
`general.v3` catalogue. It is source-level policy, not a semantic test result.
Live context diagnostics and the ranking pilot are **NOT RUN**. The plugin
retrieves no callers, ownership graph, requirements or tests.

## What the judge receives

| Battery | Input | Applicability and limits |
|---|---|---|
| Code | `language` and redacted `code`, comments blanked in place | Catalogue/language code questions; no code battery for standalone Markdown |
| Documentation | `language` and redacted `source`, adjacent code/comments in one bounded body | DC-01/DC-02 remain applicable to comment-free code; DC-03/DC-04 require a comment in the window; GC-02 applies to submitted text |
| Standalone Markdown | Bounded redacted `source` | DC-01 and GC-02 only; DC-02/DC-03/DC-04 explicitly non-applicable, not false |
| Optional localization | Named lexical units of the code chunk | At most 254 units plus `cannot_localize`; location evidence, not verification of the allegation |

Instructions name the state field and original inclusive physical line range. Ranges are also recorded
with answers, but the request does not explicitly supply original offsets in
question text. Parsed Go declarations can keep local relationships together;
non-Go lexical boundaries and Go parser fallback are not semantic understanding.
Oversized declarations split. Split windows and parser/lexical uncertainty limit
applicable questions. Masking keeps positions/newlines; long-line fragments can
share a line range. Complete declarations do not establish callers or ownership.

The combined documentation body honors `chunk_bytes`; serialized requests
independently honor 256 KiB. Preparation uses at most 1 MiB raw source. There is
no constant-memory or model-token-fit promise. Redaction uses patterns, not a
complete secret detector. Source and comments are **data**, never instructions to
the evaluator. GC-02 is a review signal, not a prompt-injection security boundary
or proof of adversarial robustness.

## Mechanical coverage rule versus contextual review

The implementation **always** marks these applicable code IDs external-context-
limited: **ST-03, EH-04, DF-01, DF-03, DF-04** (`needsExternalContext`). Raw
probabilities and positive allegations remain, but their determinate answers do
not count as complete coverage. It does not inspect a contract, ask the model to
certify context sufficiency, or waive the flag when a local case looks easy.
Split units additionally limit every applicable ID. Ordinary source can remain
partial even with every question answered. `assessed` concerns checklist/input
completeness, not confidence in correct behavior; `answered` includes uncertainty.

These are **illustrative review obligations**, not claims that the classifier
proves or rejects the examples. “Locally ruled out” concerns this allegation in
the shown scope, never the program. A reviewer must establish control flow and
language/API semantics before treating an example as proof. The conservative
flags above still apply in production.

| Question | Locally provable example (under established semantics) | Locally ruled-out example | Requires external context |
|---|---|---|---|
| ST-03, unexpected boundary | A visible caller relies on its slice remaining unchanged; the shown callee overwrites it against that visible contract | A shown scalar computation has no mutation or external effect relevant to the allegation | Is an isolated helper's mutation expected? Need callers and API contract; absence of them proves neither answer |
| ST-04, blurred responsibilities | A visible parser also updates unrelated global account state, exposing both responsibilities locally | A narrow shown conversion has one coherent purpose and no basis for this allegation | Component ownership and orchestration may require architectural context; ST-04 is **not** automatically external-context-limited by the current ID rule |
| EH-04, resource lifetime | Successful acquisition of a known closable resource followed by a reachable return with no release or transfer | A local resource is released on every path after successful acquisition under the API's lifetime rules | Returned/stored/passed resources may be transferred intentionally; inspect ownership contract, receiver and failure paths |
| DF-01, unchecked argument | A visible caller supplies zero to the unchecked divisor or an empty slice to an unconditional index | The local function rejects zero/empty before every relevant use | An isolated unchecked helper may rely on caller validation; missing callers do not establish missing guarantees |
| DF-03, reachable nil | Explicit local nil assignment flows into unconditional dereference | A dominating nil guard prevents that use on all relevant paths | Can a constructor/callback/API return nil? Inspect producers and failure contracts |
| DF-04, unvalidated external data | Visible ingestion feeds a demonstrated invalid value to an operation without validation on that path | Semantically sufficient validation dominates every relevant use | Validation/sanitization may occur upstream; inspect provenance and validators |
| DC-03, comment contradiction | Adjacent comment says milliseconds while visible arithmetic/API clearly uses seconds | Adjacent comment accurately describes the branch under established semantics | Comment refers to callers/system policy not in the window, or its implementation was split away |

### Comments are evidence of a claimed contract, not proof of compliance

`// caller guarantees non-empty` or `// caller owns Close` may describe intended
preconditions/transfers. It does not prove that all callers obey them. Removing
the comment does not prove misuse either. Code questions cannot see it at all:
comments are blanked there. Documentation questions see adjacent code/comments,
but cannot establish cross-file behavior. Check callers, receiving ownership,
failure paths and tests.

A helper returning `xs[0]` with a non-empty precondition comment requires caller
review; a visible caller passing `nil` can settle that example. Returning an open
file with a transfer comment requires receiver lifetime evidence; a local early
return before transferring or closing can expose a distinct defect. These are
different evidence states, not different probability thresholds.

The proposed code-plus-relevant-comment variant is a **separately budgeted
experiment**, not the production code battery. Evaluate present/removed/correct/
contradictory contracts and necessary/irrelevant context pairs; do not silently
change catalogue wording or cache compatibility to obtain a desired outcome.

## Retaining uncertainty

- Keep positive flags despite partial documentation or localization failure.
  Escape selection, low confidence, unknown key or too many units must not invent
  a location. A concern may be distributed across units.
- Thresholds remain `p < .4` negative, `.4 <= p < .6` uncertain, `p >= .6` positive;
  missing/invalid stays missing. A negative with missing context is not a guarantee.
  Severity is catalogue metadata, not probability.
- Non-applicable is a stored input decision, not an omitted answer retrospectively
  called false. Historical lead labels/samples do not calibrate the current model.
- A declaration is not a call graph; imports are not callers. A source digest
  describes captured bytes, not an atomic repository snapshot.

A useful handoff contains path/digest, category/allegation, actual ranges or unknown
location, model/backend/preprocessing provenance, limitations and the first
check that can distinguish the alternatives. Confirm with surrounding contracts
and preferably a reproducer/test; reject with contrary evidence; otherwise keep
it unresolved. See [README.md](README.md) for report/coverage boundaries and
[RELEASING.md](RELEASING.md) for the unrun pilot and diagnostics protocol.
