# Releasing

This is a gate procedure, **not a record of gates run**. No result or release
readiness is asserted here. The review-queue live semantic pilot is **NOT RUN**.
Implementation, unsigned candidate validation, semantic evaluation, signing,
installation and publication are separate decisions. Do not install/publish or
edit an installed plugin as part of implementation.

## 1. Freeze one candidate before collecting evidence

Record commit, full working-tree state/hash, plugin/interface version, schemas and
catalogue/preprocessing/threshold/report-policy versions. Include untracked source
in the freeze; a commit name alone does not identify this integration worktree.
No parallel source edits during a gate. Bind logs and artifact digests to that same
candidate; a changed candidate needs fresh relevant evidence.

Reconcile README discrepancies against current source, including inventory/subtree
truth, report truncation routes, typed model-limit errors and request context
metadata. Verify the newly wired direct-judge attribution and partial-return path. Reconcile `plugin.json`
descriptions, interface compatibility and `public/` mirrors before release.
Do not inherit readiness from helper tests or this documentation.

Native verification uses **Go 1.27.3** explicitly. Set `GO` to the absolute path of
that installed compiler; record its resolved path/version and use
`GOTOOLCHAIN=local` (no implicit download). Commands to run, not claimed results:

```sh
export GOTOOLCHAIN=local
# Set GO=/absolute/path/to/the/installed/go1.27.3/bin/go first.
"$GO" version
"$GO" env GOROOT
"$GO" test -count=1 ./...
"$GO" test -race -count=1 ./...
"$GO" vet ./...
git diff --check
```

Keep exit statuses, commands, durations and PASS/FAIL/SKIP/NOT RUN with reasons.
Do not hide suite status behind a pipeline. The module's language floor is not
the compiler used for the gate. Native tests do not establish guest memory fit,
host contracts or semantic accuracy.

## 2. Mechanical negative controls and restart evidence

Map the release's declared behaviors to actual symbols and artifacts in
the frozen candidate. Require evidence for:

- All-abstention/missing answers remain unknown/partial; exact .4/.6 boundaries;
  isolated serious positives remain prominent; legacy coverage is unknown.
- Distinct-file aggregation, replay/category deduplication, lanes, coverage, stable
  ties, the EH-04/DF-03/DF-04 versus EH-01 counterexample, stale cursors, report read
  failures and prefix incompleteness.
- Code evidence survives later docs/refusal/localization failures; per-battery
  progress survives restart with empty/disabled cache and reduced call budgets.
- Failure before/after journal append and checkpoint writes; old-cursor replay;
  damaged alternate checkpoint; torn journal-tail refusal; storage-cap termination
  and retention cleanup. A mock Store does not prove fsync or power-loss behavior.
  Document actual host guarantees, not stronger imagined ones.
- Nested rules/negation within the supported grammar; unreadable/unsupported rules
  and truncated lists prevent forbidden submission. Put a sentinel in excluded
  source and inspect the deterministic judge's captured input.
- Code/comment context, comment-free DC-01/DC-02, Markdown applicability,
  adjacent-code cache misses, split/parser fallback, giant comments/lines, UTF-8,
  escaping and encoded request bounds. Distinguish at-cap and cap+1 files.
- Changed-source resume does not merge digests; concrete pins survive restart;
  alias moves, missing/mismatched identity, backend/base-path drift, terminal
  requests and persistent Retry-After/backoff prevent premature inference.
- Localization escape/unknown key/low confidence and 254-versus-255 units retain
  allegations without invented locations.

In a disposable candidate copy, deliberately break relevant behaviors
(all-abstention-as-clean, chunk-only ranking, comments-only docs input, duplicate
replay counting) and require corresponding tests to reject them. Record each
mutation/failure, restore correct code and rerun. Never ship mutation code.
Naming a failure mode is not evidence that a test detects it.

## 3. TinyGo build and isolated no-live-provider host gate

Build the actual guest separately with `build.sh` (used by `aiisdk build`). Record
TinyGo version, its compatible underlying Go environment, SDK CLI/dependency and
host binary/version/digest. Do **not** assume native Go 1.27.3 is interchangeable
with TinyGo's supported compiler. Do not change the native gate's compiler
requirement just to make TinyGo work.

```sh
# Set TINYGO=/absolute/path/to/the/selected/tinygo first.
"$TINYGO" version
TINYGO="$TINYGO" ./build.sh
# With a compatible SDK CLI and isolated output/state:
aiisdk package
```

`build.sh` uses `wasm-unknown`, scheduler `none`, conservative GC and no debug
information. Build success is not an executed guest. Packaging creates an
**unsigned candidate**, not a release eligible for installation or key attachment.
Record guest/package/manifest hashes and inspect declared metadata. The current
module names SDK v0.1.18; the historical f86854c settings-metadata fix is not a
substitute for checking actual packaged `choices_from`, grants, schemas and
versions. The old “runtime dependency v0.1.10” note no longer describes this module;
the declared host floor requires separate compatibility evidence.

Use isolated host/guest fixtures with synthetic source, temporary private state
and deterministic in-process/fake judge transport. **No live provider, real
credential, production identity or outbound hosted grant belongs in this gate.**
Inspect `aiisdk test` fixtures before invoking them: a real-provider fixture is not
part of this no-live-provider gate. If a local fixture endpoint is necessary, name
its grant/scope separately and establish that it is authorized and deterministic,
not an operator's live model service.

Exercise public scan/start/step/report through the actual guest/host boundary, not
only native helpers. Restart with journal/checkpoints retained and cache empty.
Require observable incomplete/recovery outcomes, schema acceptance, stale cursors
and capability denial. Measure actual guest peak memory with worst-case allowed
source and populated cache, largest encoded request, step duration/calls and
persisted progress. Cover 128 KiB default and 192 KiB maximum, cap+1, and
164,364, 155,361, 229,298-byte fixtures: assess only within the captured cap and
require visible unassessed coverage otherwise. Repeat scans/reports on one worker
without scheduled restarts. The 1 MiB expansion is out of this release; retain its failing experimental regressions,
not as passing tests of this narrowed candidate. Name the instrument and its limits. Native memory measurements and build
success cannot substitute for guest measurements. Mark any unexercised boundary
NOT RUN with its risk rather than upgrading helper tests into host integration.

## 4. Semantic evaluation — NOT RUN; separate authorization required

Before live inference, agree source-egress destination, file scope, model and total
inference/review budget. Freeze candidate, concrete model identity, repository
revision, settings, eligible inventory, exclusions and selection policies before
inspecting scores. Synthetic judges establish mechanics, not accuracy.

Compare the review queue against predeclared random, size and legacy baselines
on identical assessment records. Blind reviewers to selection method and score;
include low/unflagged files and coverage gaps without bypassing restrictions.
Record distinct confirmed issues, overlap, missed issues, review time, inference
cost, model identity and uncertainty. Separate selection accuracy from allegation
verification. Do not treat historical precision as a validated current-model
ranking weight, or a small pilot as whole-repository recall evidence.

The live semantic evaluation remains NOT RUN. Its scope and budget are separate
from local release qualification and require explicit authorization.

## 5. Authorized signing and publication

Only after evidence/limitations review and release authorization:

1. Set release version/interface decision in `plugin.json`, update changelog/public
   mirrors, and freeze/revalidate the final candidate. Record hashes from that exact
   packaged tree.
2. Stage for the platform T3 signing ceremony; keys never belong in this repository.
   Existing tooling uses:

   ```sh
   go run ./cmd/stage-t3
   aii-devsign -staging dist/t3 -payload-out dist/t3/ceremony-payload.json
   ```

   Select authorized compiler/tool binaries explicitly. Compare payload package
   hash with candidate. Explain manifest differences (historical tooling omitted
   SDK `default_variant`); inspect current manifests rather than assuming this.
3. The ceremony signs `plugin.platform_release`. Assemble and verify with authorized
   trust material:

   ```sh
   aii-devsign -staging dist/t3 -attach-sig platform.sig.json -root <platform-root> -status <snapshot> -o <pkg>
   aii plugin verify -platform-key <root> -trust-dir <trust> <pkg>
   ```

   Confirm current CLI syntax before use. Offline verification is required before
   publication; a signature is provenance, not semantic pilot evidence.
4. With publication authorization, attach the signed `.aiiospkg` to GitHub tag
   `v<version>`. `aiisdk publish` emits URL/hash/size for `aiii-dot-id/plugin-catalog`.
   Installation remains a separately authorized action.

Releases are immutable. Corrections get a new version: never replace signed bytes
under an old identity or borrow a previous candidate's evidence.
