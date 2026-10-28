package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const (
	filesPerStep = 400

	stateBytes = 8 << 20

	resultsBytes = 48 << 20

	reportInputBytes = 1 << 20

	defaultPageBytes   = 131072
	defaultReviewLimit = 20

	invokeSlackMS = 6000
)

var caps = []string{jevHost, netLocal, "fs.sandbox", "fs.private"}

func init() {
	p := sdk.New("id.aiii.codequality")

	p.Describe("judge", sdk.Descriptor{
		Summary:        judgeSummary,
		Input:          "schemas/judge_in.json",
		Output:         "schemas/judge_out.json",
		Effects:        sdk.EffectsWriteExternal,
		Capabilities:   caps,
		MaxResultBytes: 65536,
		Family:         "code quality",
		Keywords:       []string{"code quality", "review", "judge", "maintainability", "findings"},
		Examples:       []string{`{"items":[{"ref":"store.go","text":"package store\n..."}]}`},
	})
	p.Handle("judge", named(judge))

	p.Describe("scan", sdk.Descriptor{
		Summary:        scanSummary,
		Input:          "schemas/scan_in.json",
		Output:         "schemas/scan_out.json",
		Effects:        sdk.EffectsWriteExternal,
		Capabilities:   caps,
		MaxResultBytes: 65536,
		Family:         "code quality",
		Keywords:       []string{"code quality", "scan", "repository", "folder", "quality report"},
		Examples:       []string{`{"action":"start","path":"projects/aii-os/internal"}`, `{"action":"step","scan_id":"cq_1758500000000"}`},
	})
	p.Handle("scan", named(scan))

	p.Describe("models", sdk.Descriptor{
		Summary:        modelsSummary,
		Input:          "schemas/models_in.json",
		Output:         "schemas/models_out.json",
		Effects:        sdk.EffectsReadExternal,
		Capabilities:   []string{jevHost, netLocal},
		MaxResultBytes: 65536,
		Family:         "code quality",
		Keywords:       []string{"jev", "models"},
	})
	p.Handle("models", named(models))

	p.Describe("report", sdk.Descriptor{
		Summary:        reportSummary,
		Input:          "schemas/report_in.json",
		Output:         "schemas/report_out.json",
		Effects:        sdk.EffectsReadInternal,
		Capabilities:   []string{"fs.private"},
		MaxResultBytes: 262144,
		Family:         "code quality",
		Keywords:       []string{"code quality", "report", "findings", "summary"},
	})
	p.Handle("report", named(report))

	p.Describe("advance", sdk.Descriptor{
		Summary:        advanceSummary,
		Input:          "schemas/advance_in.json",
		Output:         "schemas/advance_out.json",
		Effects:        sdk.EffectsWriteExternal,
		Capabilities:   caps,
		MaxResultBytes: 65536,
		Family:         "code quality",
	})
	p.Handle("advance", named(advance))

	p.Run()
}

func main() { sdk.MainDescribe() }

var tbl *cq.Table

func table() (*cq.Table, error) {
	if tbl == nil {
		t, err := cq.LoadTable()
		if err != nil {
			return nil, err
		}
		tbl = t
	}
	return tbl, nil
}

type hostFS struct{ dir string }

func (f hostFS) at(rel string) string {
	if rel == "" || rel == "." {
		return f.dir
	}
	return path.Join(f.dir, rel)
}

func (f hostFS) List(dir string) ([]cq.Entry, bool, error) {
	es, truncated, err := sdk.Files.List(sdk.SandboxRoot, f.at(dir))
	if err != nil {
		return nil, false, err
	}
	out := make([]cq.Entry, 0, len(es))
	for _, e := range es {
		out = append(out, cq.Entry{Name: e.Name, Dir: e.Dir, Size: e.Size, Symlink: e.Symlink})
	}
	return out, truncated, nil
}

func (f hostFS) Read(file string, max int64) ([]byte, error) {
	return cq.ReadWhole(hostStore{sdk.SandboxRoot}, f.at(file), max)
}

type hostStore struct{ root string }

func (h hostStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	b, eof, _, err := sdk.Files.Read(h.root, p, off, n)
	return b, eof, err
}
func (h hostStore) Write(p string, b []byte) error  { return sdk.Files.Write(h.root, p, b) }
func (h hostStore) Append(p string, b []byte) error { return sdk.Files.Append(h.root, p, b) }

var private = hostStore{sdk.PrivateRoot}

type poster func(url string, opts *sdk.HTTPOptions) (sdk.HTTPResult, error)

type hostJudge struct {
	writeRuntimeCheckpoint func([]byte) error
	at                     judgeAt
	configured             string
	model                  string
	timeout, left          int
	send                   poster
	runtime                *scanRequestState
	persistRuntime         func() error
	nowMS, retryBaseMS     int64
	retryBudget            uint32
}

var errCallsSpent = fmt.Errorf("%w: the calls this step planned are spent; the next step continues from the answers kept", cq.ErrJudge)

var errUnbound = errors.New("judge called before its scan or invocation binding")

func newJudge(c config, calls int) (*hostJudge, error) {
	at, err := c.where()
	if err != nil {
		return nil, err
	}
	return &hostJudge{at: at, configured: c.model, model: c.model, timeout: c.callMS(calls), left: calls, send: sdk.HTTP.Post}, nil
}

func (j *hostJudge) Model() string { return j.model }
func (j *hostJudge) Backend() string {
	if j.runtime != nil {
		return j.runtime.Pin.Backend + ":" + j.runtime.Pin.Endpoint
	}
	return j.at.backend
}

func (j *hostJudge) Ask(state, questions map[string]any) (map[string]float64, error) {
	_, body, err := j.post(state, questions)
	if err != nil {
		return nil, err
	}
	return cq.ParseAnswers(body)
}

func (j *hostJudge) Choose(state, questions map[string]any) (map[string]cq.Choice, error) {
	_, body, err := j.post(state, questions)
	if err != nil {
		return nil, err
	}
	return cq.ParseChoices(body)
}

func (j *hostJudge) post(state, questions map[string]any) (int, []byte, error) {
	if j.left <= 0 {
		return 0, nil, errCallsSpent
	}
	if j.runtime == nil {
		return 0, nil, errUnbound
	}
	body, err := cq.RequestBody(j.configured, state, questions)
	if err != nil {
		return 0, nil, err
	}
	if err := j.beforeScanRequest(body); err != nil {
		return 0, nil, err
	}
	j.left--

	res, err := j.send(j.at.systemOne, &sdk.HTTPOptions{Body: string(body), ContentType: "application/json",
		AuthProfile: j.at.auth, TimeoutMS: j.timeout})
	if _, denied := sdk.AsDenied(err); denied {
		return 0, nil, err
	}
	raw := responseBody(res)
	if e := j.scanResponse(res.Status, raw, res.RetryAfterS); e != nil {
		return res.Status, raw, e
	}
	return res.Status, raw, nil
}

func responseBody(res sdk.HTTPResult) []byte {
	raw := []byte(res.Body)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s)
	}
	return raw
}

func newCache(c config) *cq.ShardCache { return &cq.ShardCache{S: private, MaxBytes: c.cacheBytes()} }

func stepCalls(c config, s *cq.Scan) int {
	if s.Status == "judge_unavailable" {
		return c.retryCalls()
	}
	return c.maxCalls()
}

func failure(err error) (any, error) {
	var op *sdk.OperationError
	var request *cq.RequestError
	var pin *ModelPinError
	switch d, denied := sdk.AsDenied(err); {
	case err == nil:
		return nil, nil
	case denied:
		return nil, sdk.Deny(d.ReasonCode, fmt.Sprintf(hostDenied, d.Message))
	case errors.As(err, &op):
		return nil, err
	case errors.As(err, &request) && request.Reason == cq.RequestAuthentication:
		return nil, sdk.Fail("JUDGE_AUTHENTICATION_REQUIRED", fmt.Sprintf(judgeKeyRefused, request.Error()))
	case errors.As(err, &request) && request.Retryable():
		return nil, sdk.Fail("JUDGE_UNAVAILABLE", err.Error()+" — try again later")
	case errors.As(err, &request):
		return nil, sdk.Fail("JUDGE_"+strings.ToUpper(string(request.Reason)), err.Error())
	case errors.As(err, &pin):
		return nil, sdk.Fail("SCAN_MODEL_UNRESOLVED", pin.Error())
	case errors.Is(err, cq.ErrJudge):
		return nil, sdk.Fail("JUDGE_UNAVAILABLE", err.Error()+" — try again later")
	}
	return nil, sdk.Fail("CODEQUALITY_FAILED", err.Error())
}

func named(fn func(sdk.Call) (any, error)) func(sdk.Call) (any, error) {
	return func(c sdk.Call) (any, error) {

		runtime.GC()
		out, err := fn(c)
		if err != nil {
			return failure(err)
		}
		return out, nil
	}
}

func models(c sdk.Call) (any, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	at, err := cfg.where()
	if err != nil {
		return nil, err
	}
	res, err := sdk.HTTP.Get(at.models, &sdk.HTTPOptions{AuthProfile: at.auth, TimeoutMS: cfg.callMS(1)})
	if err != nil && res.Status == 0 {
		return failure(err)
	}
	raw := responseBody(res)
	if res.Status != 200 {
		return nil, sdk.Fail("JUDGE_REFUSED", fmt.Sprintf("the judge answered %d to the model list: %.200s", res.Status, raw))
	}
	choices, err := cq.ModelChoices(raw)
	if err != nil {
		return nil, sdk.Fail("JUDGE_REFUSED", err.Error())
	}
	return map[string]any{"choices": choices}, nil
}

func judgeBudget(cfg config, items []sdk.Object) (int, error) {
	budget := 0
	for _, it := range items {
		text, _ := it.String("text")
		if int64(len(text)) > cfg.maxFileBytes() {
			ref, _ := it.String("ref")
			return 0, sdk.Fail("OPERATION_ARGUMENT_INVALID", fmt.Sprintf(judgeItemTooLarge, ref, len(text), cfg.maxFileBytes()))
		}
		budget += cq.EstimatedCallsAt(int64(len(text)), cfg.chunkBytes())
	}
	if budget > cfg.maxCalls() {

		largest := max(cfg.maxCalls()/3*cfg.chunkBytes()-1, 0)
		return 0, sdk.Fail("OPERATION_ARGUMENT_INVALID", fmt.Sprintf(judgeTooManyCalls, budget, cfg.maxCalls(), cfg.chunkBytes(), largest))
	}
	return budget, nil
}

func judge(c sdk.Call) (any, error) {
	t, err := table()
	if err != nil {
		return nil, err
	}
	items, ok := sdk.ObjectArray(c.Args().Raw("items"))
	if !ok || len(items) == 0 {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "judge requires arguments.items: [{ref, text, language?}]")
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	budget, err := judgeBudget(cfg, items)
	if err != nil {
		return nil, err
	}
	j, err := newJudge(cfg, budget)
	if err != nil {
		return nil, err
	}
	now, err := hostNow(c)
	if err != nil {
		return nil, err
	}
	if err := j.bindInvocation(cfg, now); err != nil {
		return nil, err
	}
	cache := newCache(cfg)
	var recs []cq.Record
	var assessmentErr error
	for _, it := range items {
		ref, _ := it.String("ref")
		text, _ := it.String("text")
		lang, _ := it.String("language")
		lang = strings.ToLower(lang)
		if lang == "" {
			lang = t.Detect(ref, []byte(text))
		}
		if lang == "" || t.Langs[lang] == nil {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", fmt.Sprintf("item %q: name its language (the ref's extension did not say)", ref))
		}
		rs, err := cq.JudgeTextChunked(t, j, cache, ref, lang, text, cfg.chunkBytes())
		recs = append(recs, rs...)
		if err != nil {
			assessmentErr = err
			break
		}
	}
	cq.MarkLeads(recs)
	raw, err := recordsJSON(recs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"records": raw, "faults": cq.FaultsOf(recs), "sent_to": j.at.sentTo(), "complete": assessmentErr == nil}
	if assessmentErr != nil {
		if len(recs) == 0 {
			return failure(assessmentErr)
		}
		out["assessment_error"] = assessmentErr.Error()
	}
	return out, nil
}

func statePath(id string) string   { return "scans/" + id + ".state.json" }
func resultsPath(id string) string { return "scans/" + id + ".jsonl" }
func journalPath(id string) string { return "scans/" + id + ".v2.jsonl" }
func checkpoints(id string) cq.CheckpointPair {
	return cq.CheckpointPair{Store: private, Paths: [2]string{"scans/" + id + ".checkpoint.0", "scans/" + id + ".checkpoint.1"}}
}

func validID(id string) bool {
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return id != "" && len(id) <= 64
}

func loadScan(id string) (*cq.Scan, error) {
	if !validID(id) {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan_id is required (scan start returns it)")
	}
	b, err := cq.ReadWhole(private, statePath(id), stateBytes)
	if errors.Is(err, cq.ErrTooLarge) {
		return nil, sdk.Fail("SCAN_TOO_WIDE", "scan "+id+"'s saved state is larger than it is read to; start a narrower scan")
	}
	if err != nil {
		return nil, sdk.Fail("SCAN_UNKNOWN", "no scan "+id+" — report with no scan_id lists the scans")
	}
	var s cq.Scan
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Version == cq.RecordVersion {
		state, _, recovered, err := checkpoints(id).Load()
		if err != nil {
			return nil, fmt.Errorf("load scan checkpoint: %w", err)
		}
		if len(state) > 0 {
			if err := json.Unmarshal(state, &s); err != nil {
				return nil, err
			}
		}
		if recovered {
			s.LastError = checkpointRecovered + s.LastError
		}
	}
	return &s, nil
}

func saveScan(s *cq.Scan) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if s.Version != cq.RecordVersion {
		return sdk.Fail("SCAN_RESTART_REQUIRED", legacyReportOnly)
	}
	err = checkpoints(s.ID).Save(b)
	if !errors.Is(err, cq.ErrCheckpointFull) {
		return err
	}

	s.Status = "storage_cap"
	s.LastError = checkpointFull
	s.Stack, s.Pending, s.Refused = nil, nil, nil
	s.Dir = ""
	b, err = json.Marshal(s)
	if err != nil {
		return err
	}
	return checkpoints(s.ID).Save(b)
}

func progress(cfg config, s *cq.Scan) string {
	n := s.StepsLeft(cfg.maxCalls(), filesPerStep)
	switch {
	case n == 0:
		return ""
	case cfg.scheduled:
		return fmt.Sprintf(progressScheduled, n, scheduleSecs)
	}
	return fmt.Sprintf(progressManual, n)
}

func scanView(cfg config, s *cq.Scan) map[string]any {
	out := map[string]any{"scan_id": s.ID, "status": s.Status, "files_seen": s.FilesSeen, "files_judged": s.FilesJudged,
		"chunks": s.Chunks, "calls": s.Calls, "cache_hits": s.CacheHits, "excluded": s.Excluded,
		"languages": s.LangCounts, "last_error": s.LastError, "refused": s.Refused}
	if why := scanStepRefusal(s); why != "" {
		out["report_only_reason"] = why
		return out
	}

	if cfg.maxCalls() == 0 {
		return out
	}
	if p := progress(cfg, s); p != "" {
		out["steps_left"] = s.StepsLeft(cfg.maxCalls(), filesPerStep)
		out["progress"] = p
	}
	return out
}

func scan(c sdk.Call) (any, error) {
	args := c.Args()
	action, _ := args.String("action")
	switch action {
	case "start":
		return scanStart(c)
	case "step":
		id, _ := args.String("scan_id")
		s, err := loadScan(id)
		if err != nil {
			return nil, err
		}
		cfg, err := loadConfig()
		if err != nil {
			return nil, err
		}
		now, err := hostNow(c)
		if err != nil {
			return nil, err
		}
		return stepScan(cfg, s, now)
	}
	return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan requires action: start | step")
}

func scanStepRefusal(s *cq.Scan) string {
	if s.Version != cq.RecordVersion {
		return legacyScanRefusal
	}
	if s.MaxFileBytes <= 0 || s.MaxFileBytes > maximumFileBytes {
		return savedCapUnsupported
	}
	return ""
}

func stepScan(cfg config, s *cq.Scan, nowMS int64) (any, error) {
	if why := scanStepRefusal(s); why != "" {
		return nil, sdk.Fail("SCAN_RESTART_REQUIRED", why)
	}

	progress := &cq.JournalProgress{Journal: &cq.Journal{Store: private, Path: journalPath(s.ID)}, Offset: s.ProgressOffset}
	if err := progress.Verify(); err != nil {
		return nil, fmt.Errorf("scan journal unavailable: %w", err)
	}
	s.Progress = progress
	t, err := table()
	if err != nil {
		return nil, err
	}
	calls := stepCalls(cfg, s)
	j, err := newJudge(cfg, calls)
	if err != nil {
		return nil, err
	}
	if err := j.bindScan(cfg, s, nowMS); err != nil {
		return nil, err
	}
	if j.runtime.Blocked != "" {
		return nil, sdk.Fail("SCAN_REQUEST_BLOCKED", fmt.Sprintf(scanBlocked, j.runtime.Blocked))
	}
	if !j.runtime.Retry.Ready(nowMS) {
		out := scanView(cfg, s)
		out["retry_not_before_ms"] = j.runtime.Retry.NotBeforeMS
		return out, nil
	}
	recs, err := s.Step(t, hostFS{s.Root}, j, newCache(cfg), calls, filesPerStep)

	if j.runtime.Blocked != "" {
		s.Status, s.LastError = "request_blocked", j.runtime.Blocked
	}
	if errors.Is(err, cq.ErrJournalFull) {
		s.Status, s.LastError = "storage_cap", journalFull
	}
	var request *cq.RequestError
	if errors.As(err, &request) && request.Reason == cq.RequestAuthentication {
		s.LastError = fmt.Sprintf(judgeKeyRefused, request.Error())
	}
	if serr := saveScan(s); serr != nil {
		return nil, serr
	}
	if err != nil {
		return failure(err)
	}
	out := scanView(cfg, s)
	judged := make([]any, 0, len(recs))
	for _, r := range recs {
		a := cq.AssessmentOf(r)
		judged = append(judged, map[string]any{"ref": r.Ref, "chunk": r.Chunk, "legacy_index": r.Index, "legacy_band": r.Band, "index": r.Index, "band": r.Band, "assessment": a.Status, "reason_codes": a.Reasons, "findings": len(r.Findings)})
	}
	out["judged"] = judged
	out["sent_to"] = j.at.sentTo()
	return out, nil
}

func scanStart(c sdk.Call) (any, error) {
	args := c.Args()
	folder, _ := args.String("path")
	if folder == "" {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan start requires path: the folder to scan, as the identity's own tools name it")
	}
	exclude, _ := args.StringArray("exclude")
	t, err := table()
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	asked, _ := args.StringArray("languages")
	languages, err := t.Languages(asked)
	if err != nil {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", err.Error())
	}
	tests, _ := args.Bool("include_tests")
	maxFiles := 5000
	if n, ok := args.Int("max_files"); ok && n > 0 && n < 100000 {
		maxFiles = int(n)
	}
	id, named := args.String("scan_id")
	if named && id != "" {
		if !validID(id) {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan_id is letters, digits, '_' and '-' only")
		}
	} else {
		now, err := hostNow(c)
		if err != nil {
			return nil, err
		}
		id = "cq_" + strconv.FormatInt(now, 10)
		if exists, err := scanExists(id); err != nil {
			return nil, err
		} else if exists {
			id += "_" + strconv.FormatInt(now%997, 10)
		}
	}
	if exists, err := scanExists(id); err != nil {
		return nil, err
	} else if exists {
		return nil, sdk.Fail("SCAN_EXISTS", "a scan named "+id+" exists — step it, or choose another name")
	}
	s, err := cq.NewScan(hostFS{folder}, id, folder, "", exclude, languages, tests, maxFiles, cfg.maxFileBytes())
	if err != nil {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", err.Error())
	}
	binding, err := scanBinding(cfg)
	if err != nil {
		return nil, err
	}
	s.Runtime, err = json.Marshal(binding)
	if err != nil {
		return nil, err
	}
	s.ChunkBytes = cfg.chunkBytes()
	if _, _, err := sdk.Files.List(sdk.SandboxRoot, folder); err != nil {
		return failure(err)
	}
	if err := pruneScans(cfg.keepScans()); err != nil {
		return nil, err
	}

	if err := removeScanFiles(id, false); err != nil {
		return nil, err
	}
	if err := private.Write(journalPath(id), nil); err != nil {
		return nil, err
	}

	initial, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if len(initial) > 240<<10 {
		return nil, sdk.Fail("SCAN_TOO_WIDE", scanTooWide)
	}
	for _, name := range checkpoints(id).Paths {
		if err := private.Write(name, nil); err != nil {
			return nil, err
		}
	}
	if err := private.Write(statePath(id), initial); err != nil {
		return nil, err
	}
	if err := saveScan(s); err != nil {
		return nil, err
	}
	return scanView(cfg, s), nil
}

func pruneScans(keep int) error {
	ids, err := scanIDs()
	if err != nil {
		return err
	}
	for len(ids) >= keep && len(ids) > 0 {
		if err := removeScanFiles(ids[0], true); err != nil {
			return err
		}
		ids = ids[1:]
	}
	return nil
}

func removeScanFiles(id string, withState bool) error {
	files := []string{resultsPath(id), journalPath(id), checkpoints(id).Paths[0], checkpoints(id).Paths[1]}
	if withState {
		files = append(files, statePath(id))
	}
	for _, p := range files {
		if _, err := sdk.Files.Delete(sdk.PrivateRoot, p); err != nil {
			return err
		}
	}
	return nil
}

func scanExists(id string) (bool, error) {
	_, _, exists, err := sdk.Files.Digest(sdk.PrivateRoot, statePath(id))
	return exists, err
}

func hostNow(c sdk.Call) (int64, error) {
	now, ok := c.HostNowMillis()
	if !ok || now <= 0 {
		return 0, sdk.Fail("HOST_CLOCK_UNAVAILABLE", hostClockMissing)
	}
	return now, nil
}

func scanIDs() ([]string, error) {
	es, _, err := sdk.Files.List(sdk.PrivateRoot, "scans")
	if err != nil {
		return nil, nil
	}
	var ids []string
	for _, e := range es {
		if strings.HasSuffix(e.Name, ".state.json") {
			ids = append(ids, strings.TrimSuffix(e.Name, ".state.json"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func advance(c sdk.Call) (any, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.scheduled {
		return map[string]any{"advanced": "", "reason": "scheduled_steps is off"}, nil
	}
	ids, err := scanIDs()
	if err != nil {
		return nil, err
	}
	return advanceScans(ids, loadScan, func(s *cq.Scan) (any, error) {
		now, err := hostNow(c)
		if err != nil {
			return nil, err
		}
		return stepScan(cfg, s, now)
	})
}

func advanceScans(ids []string, load func(string) (*cq.Scan, error), step func(*cq.Scan) (any, error)) (any, error) {
	skipped, unreadable := []any{}, []any{}
	for _, id := range ids {
		s, err := load(id)
		if err != nil {

			unreadable = append(unreadable, unreadableScan(id, err))
			continue
		}
		if s.Status != "running" && s.Status != "judge_unavailable" {
			continue
		}
		if why := scanStepRefusal(s); why != "" {
			skipped = append(skipped, map[string]any{"scan_id": id, "reason": why})
			continue
		}
		out, err := step(s)
		if err != nil {
			return nil, err
		}
		view := out.(map[string]any)
		view["advanced"] = id
		view["report_only"] = skipped
		view["unreadable"] = unreadable
		return view, nil
	}
	return map[string]any{"advanced": "", "reason": advanceNone, "report_only": skipped, "unreadable": unreadable}, nil
}

func listScans(cfg config, ids []string, load func(string) (*cq.Scan, error)) []any {
	scans := make([]any, 0, len(ids))
	for _, id := range ids {
		s, err := load(id)
		if err != nil {
			scans = append(scans, unreadableScan(id, err))
			continue
		}
		scans = append(scans, scanView(cfg, s))
	}
	return scans
}

func unreadableScan(id string, err error) map[string]any {
	return map[string]any{"scan_id": id, "status": "unreadable", "last_error": err.Error(), "route": scanUnreadable}
}

func report(c sdk.Call) (any, error) {
	vals, err := sdk.Settings.Load()
	if err != nil {
		return nil, err
	}
	return reportWithSettings(vals, func(cfg config) (any, error) {
		return reportData(c, cfg)
	})
}

func reportWithSettings(vals settingsReader, read func(config) (any, error)) (any, error) {
	cfg, configErr := configFrom(vals)
	if configErr != nil {
		cfg = config{}
	}
	out, err := read(cfg)
	if err == nil && configErr != nil {
		out.(map[string]any)["assessment_configuration_error"] = configErr.Error()
	}
	return out, err
}

func reportData(c sdk.Call, cfg config) (any, error) {
	args := c.Args()
	id, _ := args.String("scan_id")
	if id == "" {
		ids, err := scanIDs()
		if err != nil {
			return nil, err
		}
		return map[string]any{"scans": listScans(cfg, ids, loadScan)}, nil
	}
	s, err := loadScan(id)
	if err != nil {
		return nil, err
	}
	var b []byte
	var recs []cq.Record
	var coverage []cq.Coverage
	complete := true
	if s.Version == cq.RecordVersion {
		recs, coverage, b, complete, err = cq.JournalReport(&cq.Journal{Store: private, Path: journalPath(id)}, reportInputBytes, s.ProgressOffset)
		if err != nil {
			return nil, err
		}
	} else {
		b, complete, err = cq.ReadPrefix(private, resultsPath(id), resultsBytes)
		if err != nil {

			_, _, exists, derr := sdk.Files.Digest(sdk.PrivateRoot, resultsPath(id))
			if derr != nil {
				return nil, derr
			}
			if exists || s.Chunks > 0 {
				return nil, fmt.Errorf("read committed results for %s: %w", id, err)
			}
			b, complete = nil, true
		}
		if !complete {
			b = cq.WholeLines(b)
		}
		recs, err = cq.ParseRecords(b)
		if err != nil {
			return nil, err
		}
	}
	incomplete := func(out map[string]any) map[string]any {
		if !complete {
			out["complete"] = false
			out["note"] = fmt.Sprintf(reportIncomplete, reportInputBytes>>20)
		}
		return out
	}
	shape, _ := args.String("shape")
	if shape == "review_queue" || shape == "coverage" {
		lane, _ := args.String("lane")
		if lane != "" && lane != "correctness" && lane != "maintainability" {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", laneInvalid)
		}
		view := cq.BuildReview(recs, coverage, s.Version == cq.RecordVersion && s.Status == "done", complete, lane)
		checkpoint, e := json.Marshal(s)
		if e != nil {
			return nil, e
		}
		generation := cq.ReviewGeneration(checkpoint, b)
		cursor, _ := args.String("page_cursor")
		pageBytes, limit := defaultPageBytes, defaultReviewLimit
		if n, ok := args.Int("page_bytes"); ok {
			pageBytes = int(n)
		}
		if n, ok := args.Int("limit"); ok {
			limit = int(n)
		}
		page, e := cq.PageReview(view, generation, shape, lane, cursor, pageBytes, limit)
		if errors.Is(e, cq.ErrStaleCursor) {
			return nil, sdk.Fail("STALE_CURSOR", e.Error())
		}
		if errors.Is(e, cq.ErrReviewEntryTooLarge) {
			return nil, sdk.Fail("REVIEW_ENTRY_TOO_LARGE", e.Error())
		}
		if e != nil {
			return nil, e
		}
		raw, e := json.Marshal(page)
		if e != nil {
			return nil, e
		}
		return incomplete(map[string]any{"root": s.Root, "scan_id": s.ID, "review": json.RawMessage(raw)}), nil
	}
	if shape == "" || shape == "summary" {
		worst := 10
		if n, ok := args.Int("worst"); ok && n > 0 && n <= 100 {
			worst = int(n)
		}
		out := cq.Summary(recs, worst)
		out["scan"] = scanView(cfg, s)
		out["catalogue"] = cq.CatalogueDigest()

		if n := s.Excluded["refused_by_judge"]; n > 0 {
			out["refused_by_judge"] = n
			out["refused_note"] = fmt.Sprintf(refusedNote, n, s.FilesJudged)
		}
		return incomplete(out), nil
	}
	cursor, _ := args.Int("cursor")
	pageBytes := defaultPageBytes
	if n, ok := args.Int("page_bytes"); ok && n >= 4096 && n <= 200000 {
		pageBytes = int(n)
	}
	minSev, _ := args.String("min_severity")
	if minSev == "" {
		minSev = "low"
	}
	lang, _ := args.String("language")
	lang = strings.ToLower(lang)
	glob, _ := args.String("path")
	selected := cq.Select(recs, glob, minSev, lang)
	page, next := cq.Page(selected, int(cursor), pageBytes)
	raw, err := recordsJSON(page)
	if err != nil {
		return nil, err
	}

	return incomplete(map[string]any{"root": s.Root, "records": raw, "faults": cq.FaultsOf(page), "next_cursor": next,
		"total_records": len(selected)}), nil
}

func recordsJSON(recs []cq.Record) (json.RawMessage, error) {
	if len(recs) == 0 {
		return json.RawMessage("[]"), nil
	}
	type visibleRecord struct {
		cq.Record
		Assessment cq.Assessment `json:"assessment"`
	}
	visible := make([]visibleRecord, 0, len(recs))
	for _, r := range recs {
		visible = append(visible, visibleRecord{r, cq.AssessmentOf(r)})
	}
	b, err := json.Marshal(visible)
	if err != nil {
		return nil, sdk.Fail("RESULT_ENCODE_FAILED", "the records could not be encoded: "+err.Error())
	}
	return b, nil
}
