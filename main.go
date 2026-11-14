package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const (
	filesPerStep = cq.StepFiles

	stateBytes = 8 << 20

	resultsBytes = 48 << 20

	defaultPageBytes   = 131072
	defaultReviewLimit = 20
	defaultTriageLimit = 50

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

const listPages = 16

func (f hostFS) List(dir string) ([]cq.Entry, bool, error) {
	var out []cq.Entry
	after := ""
	for page := 0; ; page++ {
		es, truncated, err := sdk.Files.ListAfter(sdk.SandboxRoot, f.at(dir), after)
		if err != nil {
			return nil, false, err
		}
		if after != "" && len(es) > 0 && es[0].Name <= after {
			return out, true, nil
		}
		for _, e := range es {
			out = append(out, cq.Entry{Name: e.Name, Dir: e.Dir, Size: e.Size, Symlink: e.Symlink})
		}
		if !truncated || len(es) == 0 {
			return out, false, nil
		}
		if page+1 >= listPages {
			return out, true, nil
		}
		after = es[len(es)-1].Name
	}
}

func (f hostFS) Read(file string, max int64) ([]byte, error) {
	return cq.ReadWhole(hostStore{sdk.SandboxRoot}, f.at(file), max)
}

func (f hostFS) Digest(file string) (string, int64, error) {
	sum, size, exists, err := sdk.Files.Digest(sdk.SandboxRoot, f.at(file))
	if err == nil && !exists {
		return "", 0, cq.ErrFileChanged
	}
	return sum, size, err
}

func (f hostFS) ReadAt(file string, off int64, n int) ([]byte, bool, error) {
	out := make([]byte, 0, n)
	for len(out) < n {
		b, eof, _, err := sdk.Files.Read(sdk.SandboxRoot, f.at(file), off+int64(len(out)), min(n-len(out), cq.IOPage))
		if err != nil {
			return nil, false, err
		}
		out = append(out, b...)
		if eof {
			return out, true, nil
		}
		if len(b) == 0 {
			return nil, false, fmt.Errorf("read %s at %d: no progress", file, off+int64(len(out)))
		}
	}
	return out, false, nil
}

type hostStore struct{ root string }

func (h hostStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	b, eof, _, err := sdk.Files.Read(h.root, p, off, n)
	return b, eof, err
}
func (h hostStore) Write(p string, b []byte) error  { return sdk.Files.Write(h.root, p, b) }
func (h hostStore) Append(p string, b []byte) error { return sdk.Files.Append(h.root, p, b) }

func (h hostStore) List(dir string) ([]cq.Entry, bool, error) {
	es, truncated, err := sdk.Files.List(h.root, dir)
	if notFound(err) {
		return nil, false, fmt.Errorf("%w: %w", cq.ErrNotFound, err)
	}
	if err != nil {
		return nil, false, err
	}
	out := make([]cq.Entry, 0, len(es))
	for _, e := range es {
		out = append(out, cq.Entry{Name: e.Name, Dir: e.Dir, Size: e.Size, Symlink: e.Symlink})
	}
	return out, truncated, nil
}
func (h hostStore) PublishStaged(p, staged, sum string) error {
	_, err := sdk.Files.PublishStaged(h.root, p, staged, sum, "")
	return err
}
func (h hostStore) Delete(p string) error { _, err := sdk.Files.Delete(h.root, p); return err }

var private = hostStore{sdk.PrivateRoot}

func notFound(err error) bool {
	var oe *sdk.OperationError
	return errors.As(err, &oe) && oe.ReasonCode == "FS_NOT_FOUND"
}

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

var errCallsSpent = fmt.Errorf("%w (%w, %w): the next step continues from the answers kept", cq.ErrStepCalls, cq.ErrJudge, cq.ErrNotSent)

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
		return 0, nil, fmt.Errorf("%w (%w)", errUnbound, cq.ErrNotSent)
	}

	body, err := cq.RequestBody(j.configured, state, questions)
	if err != nil {
		return 0, nil, fmt.Errorf("%w (%w)", err, cq.ErrNotSent)
	}
	if err := j.beforeScanRequest(body); err != nil {
		return 0, nil, fmt.Errorf("%w (%w)", err, cq.ErrNotSent)
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
	case errors.Is(err, cq.ErrCache):

		return nil, sdk.Fail("CACHE_UNREADABLE", fmt.Sprintf(cacheUnreadable, err.Error()))
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

		cq.FillLowHeap()
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
		if len(text) > cq.SegmentBytes {
			ref, _ := it.String("ref")
			return 0, sdk.Fail("OPERATION_ARGUMENT_INVALID", fmt.Sprintf(judgeItemTooLarge, ref, len(text), cq.SegmentBytes))
		}
		budget += cq.EstimatedCallsAt(int64(len(text)), cfg.chunkBytes())
	}
	if budget > cfg.maxCalls() {

		largest := max(cfg.maxCalls()/cq.CallsPerChunk*cfg.chunkBytes()-1, 0)
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
func intentPath(id string) string  { return "scans/" + id + ".intent.jsonl" }

func attemptPath(id string) string { return "scans/" + id + ".attempt.json" }

func journal(s *cq.Scan) *cq.Journal {
	return &cq.Journal{Store: private, Path: journalPath(s.ID), MaxBytes: s.JournalCap}
}

const intentShare = 8

func journalCapOf(room int64) int64 { return room - room/intentShare }

func intentJournal(s *cq.Scan) *cq.Journal {
	return &cq.Journal{Store: private, Path: intentPath(s.ID), MaxBytes: scanRoom(s)}
}

func scanRoom(s *cq.Scan) int64 { return s.JournalCap + s.JournalCap/(intentShare-1) }

func asksIntent(s *cq.Scan) (bool, error) {
	if s.JournalCap <= 0 {
		return false, nil
	}
	_, _, exists, err := sdk.Files.Digest(sdk.PrivateRoot, intentPath(s.ID))
	return exists, err
}

func earlierVersion(intentFile bool) string {
	if intentFile {
		return "0.2.0-r1"
	}
	return "0.2.0 or earlier"
}

func intentFileOf(s *cq.Scan) (*cq.Journal, string) {
	asks, err := asksIntent(s)
	if err != nil {
		return nil, err.Error()
	}
	if !asks {
		return nil, ""
	}
	return intentJournal(s), ""
}

const (
	privateQuota   = 64 << 20
	journalReserve = 2*250<<10 + 1<<20
	minJournalRoom = 2 << 20
	listingMost    = 1024
	newScanFiles   = 6
)

func cacheGrowth(cacheBound, newer int64) int64 { return max(cacheBound-newer, 0) }

func journalRoom(held, newer, cacheBound, shareBytes int64) int64 {
	return privateQuota - held - cacheGrowth(cacheBound, newer) - journalReserve - shareBytes
}
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
	s.Status = cq.CurrentStatus(s.Status)
	return &s, nil
}

const lastErrorMost = 1 << 10

func saveScan(s *cq.Scan) error {
	s.LastError = cq.Clip(s.LastError, lastErrorMost)
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

	where := path.Join(s.Root, s.Dir)
	if s.Dir == "" && s.File != nil {
		where = path.Join(s.Root, path.Dir(s.File.Ref))
	}
	s.Status = "storage_cap"
	s.LastError = fmt.Sprintf(checkpointFull, cq.Clip(where, 512))
	s.Stack, s.Pending, s.Refused, s.NotFinished, s.File = nil, nil, nil, nil, nil
	s.TooWide = nil
	s.Truncated = nil
	s.PlanTooLarge = nil
	s.Dir = ""
	b, err = json.Marshal(s)
	if err != nil {
		return err
	}
	return checkpoints(s.ID).Save(b)
}

func progress(cfg config, s *cq.Scan) string {
	switch {
	case !unfinished(s):
		return ""
	case cfg.scheduled:
		return progressScheduled
	}
	return progressManual
}

const statusReportOnly = "legacy_report_only"

func scanView(cfg config, s *cq.Scan) map[string]any {
	out := map[string]any{"scan_id": s.ID, "status": s.Status, "files_seen": s.FilesSeen, "files_judged": s.FilesJudged,
		"max_files": s.MaxFiles, "max_files_examined": s.MaxExamined(),
		"chunks": s.Chunks, "calls": s.Calls, "cache_hits": s.CacheHits, "excluded": s.Excluded,
		"languages": s.LangCounts, "last_error": s.LastError, "refused": s.Refused}
	if s.FilesJudged > 0 {
		out["calls_per_judged_file"] = map[string]any{
			"calls":           round2(float64(s.Calls) / float64(s.FilesJudged)),
			"with_cache_hits": round2(float64(s.Calls+s.CacheHits) / float64(s.FilesJudged)),
			"intent":          round2(float64(s.IntentCalls) / float64(s.FilesJudged)),
			"slop":            round2(float64(s.SlopCalls) / float64(s.FilesJudged)),

			"estimated_before_caching": round2(float64(cq.ScanCallsPerChunk*s.Chunks+s.FilesJudged) / float64(s.FilesJudged)),
		}
		out["intent_calls"] = s.IntentCalls
	}

	if n := s.Excluded[cq.NotFinished]; n > 0 {
		view := map[string]any{"count": n, "files": s.NotFinished, "why": fmt.Sprintf(notFinishedWords, cq.MaxAttempts)}
		if more := n - len(s.NotFinished); more > 0 {
			view["files_not_listed_here"] = more
		}
		out["not_finished"] = view
	}

	if n := s.Excluded[cq.FolderTooWide]; n > 0 {
		view := map[string]any{"count": n, "folders": s.TooWide, "why": fmt.Sprintf(tooWideWords, s.FrontierRoom()>>10)}
		if more := n - len(s.TooWide); more > 0 {
			view["folders_not_listed_here"] = more
		}
		out["too_wide"] = view
	}

	if n := s.Excluded[cq.ListingTruncated]; n > 0 {
		view := map[string]any{"count": n, "folders": s.Truncated, "why": fmt.Sprintf(listingTruncatedWords, listPages)}
		if more := n - len(s.Truncated); more > 0 {
			view["folders_not_listed_here"] = more
		}
		out["listing_truncated"] = view
	}

	if n := s.Excluded[cq.PlanTooLarge]; n > 0 {
		view := map[string]any{"count": n, "files": s.PlanTooLarge, "why": fmt.Sprintf(planTooLargeWords, s.CursorRoom()>>10)}
		if more := n - len(s.PlanTooLarge); more > 0 {
			view["files_not_listed_here"] = more
		}
		out["plan_too_large"] = view
	}
	if why := scanStepRefusal(s); why != "" {
		out["report_only_reason"] = why
		if s.Status == "running" || s.Status == "judge_unavailable" {
			out["status"] = statusReportOnly
		}
		return out
	}
	if s.Status == "running" || s.Status == "judge_unavailable" {
		out["files_pending_known"] = len(s.Pending)
		out["dirs_unlisted"] = len(s.Stack)
	}

	if cfg.maxCalls() == 0 {
		return out
	}
	if p := progress(cfg, s); p != "" {
		out["progress"] = p
	}
	return out
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

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
	case "remove":
		ids, _ := args.StringArray("scan_ids")
		return scanRemove(hostScanFiles{}, ids)
	}
	return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan requires action: start | step | remove")
}

func scanRemove(f scanFiles, ids []string) (any, error) {
	removed, err := removeScans(f, ids)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "removed", "removed": removed}, nil
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

	progress := &cq.JournalProgress{Journal: journal(s), Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
	if err := progress.Verify(); err != nil {
		return nil, fmt.Errorf("scan journal unavailable: %w", err)
	}
	s.Progress = progress
	s.Attempts = hostAttempts{attemptPath(s.ID)}

	s.Checkpoint = func() error { return saveScan(s) }
	asks, err := asksIntent(s)
	if err != nil {
		return nil, fmt.Errorf("scan intent answers unavailable: %w", err)
	}
	s.AskIntent = asks
	if s.AskIntent {
		progress.Intent, progress.Room = intentJournal(s), scanRoom(s)
	}

	if s.Status == "storage_cap" && (len(s.Stack) > 0 || len(s.Pending) > 0) {
		room, err := progress.HasRoom()
		if err != nil {
			return nil, fmt.Errorf("scan journal unavailable: %w", err)
		}
		if room {
			s.Status, s.LastError = "running", ""
		}
	}
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
	if errors.Is(err, cq.ErrIntentFull) {
		s.LastError = intentFull
	}
	var request *cq.RequestError
	if errors.As(err, &request) && request.Reason == cq.RequestAuthentication {
		s.LastError = fmt.Sprintf(judgeKeyRefused, request.Error())
	}
	if errors.Is(err, cq.ErrCache) {
		s.LastError = fmt.Sprintf(cacheUnreadable, err.Error())
	}
	serr := saveScan(s)

	if cerr := s.Settle(); cerr != nil && serr == nil {
		serr = cerr
	}
	if serr != nil {
		return nil, serr
	}
	if err != nil {
		return failure(err)
	}
	out := scanView(cfg, s)
	judged := make([]any, 0, len(recs))
	for _, r := range recs {
		if r.Battery == cq.IntentBattery {
			continue
		}
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

	maxFiles := 30000
	if n, ok := args.Int("max_files"); ok && n > 0 && n < 100000 {
		maxFiles = int(n)
	}
	now, err := hostNow(c)
	if err != nil {
		return nil, err
	}
	id, named := args.String("scan_id")
	if named && id != "" {
		if !validID(id) {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan_id is letters, digits, '_' and '-' only")
		}
	} else {
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
	s, err := cq.NewScan(hostFS{folder}, id, folder, "", exclude, languages, tests, maxFiles, cq.SegmentBytes)
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
	s.IncludeGenerated, _ = args.Bool("include_generated")
	if _, _, err := sdk.Files.List(sdk.SandboxRoot, folder); err != nil {
		return failure(err)
	}
	removed, err := beginScan(hostScanFiles{}, s, now/1000, cfg.retentionDays(), cfg.cacheBytes(), newCache(cfg).ShardBytes())
	if err != nil {
		return nil, err
	}
	if err := saveScan(s); err != nil {
		return nil, err
	}
	view := scanView(cfg, s)
	view["expired_removed"] = removed

	recognised := languages
	if len(recognised) == 0 {
		recognised = t.Known()
	}
	view["recognised_languages"] = recognised
	view["scope"] = scopeLine
	view["judge_calls"] = fmt.Sprintf(callsBeforeCaching, cq.ScanCallsPerChunk, s.ChunkBytes>>10)
	rules := map[string]any{}
	for _, l := range recognised {
		rules[l] = cq.RulesFor(l)
	}
	view["rules_without_calls"] = rules
	return view, nil
}

type scanFiles interface {
	List(dir string) ([]sdk.FileEntry, bool, error)
	Write(path string, b []byte) error
	Delete(path string) error
}

type hostScanFiles struct{}

func (hostScanFiles) List(dir string) ([]sdk.FileEntry, bool, error) {
	return sdk.Files.List(sdk.PrivateRoot, dir)
}
func (hostScanFiles) Write(p string, b []byte) error { return private.Write(p, b) }
func (hostScanFiles) Delete(p string) error          { return private.Delete(p) }

type hostAttempts struct{ path string }

const attemptBytes = 16 << 10

func (h hostAttempts) Load() (cq.Attempt, error) {
	b, err := cq.ReadWhole(private, h.path, attemptBytes)
	if notFound(err) || errors.Is(err, cq.ErrTooLarge) {
		return cq.Attempt{}, nil
	}
	if err != nil {
		return cq.Attempt{}, err
	}
	var a cq.Attempt
	if json.Unmarshal(b, &a) != nil {
		return cq.Attempt{}, nil
	}
	return a, nil
}

func (h hostAttempts) Save(a cq.Attempt) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return private.Write(h.path, b)
}

func (h hostAttempts) Clear() error { return private.Delete(h.path) }

func beginScan(f scanFiles, s *cq.Scan, now int64, retentionDays int, cacheBound, shareBytes int64) ([]any, error) {
	scans, err := listing(f.List("scans"))
	if err != nil {
		return nil, err
	}
	cached, err := listing(f.List("cache"))
	if err != nil {
		return nil, err
	}
	held := heldScans(scans, retentionDays)
	expired := expiredOf(held, now, s.ID)
	gone := map[string]bool{}
	for _, h := range expired {
		for _, p := range scanFilePaths(h.ID) {
			gone[strings.TrimPrefix(p, "scans/")] = true
		}
	}
	var kept int64
	files := 0
	for _, e := range scans {
		if !gone[e.Name] {
			kept += e.Size
			files++
		}
	}
	earlier, newer := cacheHeld(cached)
	keeping := func() string {
		return fmt.Sprintf(scanKept, len(held)-len(expired), kept>>10, earlier>>10, newer>>10, privateQuota>>20) + heldWords(held)
	}

	if files+newScanFiles > listingMost {
		return nil, sdk.Fail("SCAN_STORAGE_FULL", fmt.Sprintf(scanFilesFull, listingMost)+keeping())
	}
	room := journalRoom(kept+bytesOf(cached), newer, cacheBound, shareBytes)
	if room < minJournalRoom {
		words := fmt.Sprintf(scanRoomFull, (minJournalRoom-room+1023)>>10) + keeping()
		if held := cacheGrowth(cacheBound, newer); held > 0 {
			words += fmt.Sprintf(cacheRoomHeld, held>>10)
		}
		if earlier > 0 {
			words += earlierAnswersStay
		}
		return nil, sdk.Fail("SCAN_STORAGE_FULL", words)
	}
	s.JournalCap = journalCapOf(room)

	initial, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if len(initial) > 240<<10 {
		return nil, sdk.Fail("SCAN_TOO_WIDE", scanTooWide)
	}
	removed, err := removeExpired(f, expired)
	if err != nil {
		return removed, err
	}

	if err := removeScanFiles(f, s.ID, false); err != nil {
		return removed, err
	}
	if err := f.Write(journalPath(s.ID), nil); err != nil {
		return removed, err
	}
	if err := f.Write(intentPath(s.ID), nil); err != nil {
		return removed, err
	}
	for _, name := range checkpoints(s.ID).Paths {
		if err := f.Write(name, nil); err != nil {
			return removed, err
		}
	}
	return removed, f.Write(statePath(s.ID), initial)
}

func cacheHeld(cached []sdk.FileEntry) (earlier, newer int64) {
	for _, e := range cached {
		switch {
		case cq.EarlierShard(e.Name):
			earlier += e.Size
		case cq.Shard(e.Name):
			newer += e.Size
		}
	}
	return earlier, newer
}

func bytesOf(es []sdk.FileEntry) (n int64) {
	for _, e := range es {
		n += e.Size
	}
	return n
}

func scanFilePaths(id string) []string {
	return []string{resultsPath(id), journalPath(id), intentPath(id), checkpoints(id).Paths[0], checkpoints(id).Paths[1], attemptPath(id), statePath(id)}
}

func removeScanFiles(f scanFiles, id string, withState bool) error {
	paths := scanFilePaths(id)
	if !withState {
		paths = paths[:len(paths)-1]
	}
	for _, p := range paths {
		if err := f.Delete(p); err != nil {
			return err
		}
	}
	return nil
}

func listed(dir string) ([]sdk.FileEntry, error) {
	return listing(hostScanFiles{}.List(dir))
}

func listing(es []sdk.FileEntry, cut bool, err error) ([]sdk.FileEntry, error) {
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if cut {
		return nil, sdk.Fail("SCANS_UNLISTABLE", scansUnlistable)
	}
	return es, nil
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
	es, err := listed("scans")
	if err != nil {
		return nil, err
	}
	return scanIDsOf(es), nil
}

func scanIDsOf(es []sdk.FileEntry) []string {
	var ids []string
	for _, e := range es {
		if strings.HasSuffix(e.Name, ".state.json") {
			ids = append(ids, strings.TrimSuffix(e.Name, ".state.json"))
		}
	}
	sort.Strings(ids)
	return ids
}

func advance(c sdk.Call) (any, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.scheduled {
		return map[string]any{"advanced": "", "reason": "scheduled_steps is off"}, nil
	}
	now, err := hostNow(c)
	if err != nil {
		return nil, err
	}

	scans, err := listed("scans")
	if err != nil {
		return nil, err
	}
	removed, err := removeExpired(hostScanFiles{}, expiredOf(heldScans(scans, cfg.retentionDays()), now/1000, ""))
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs()
	if err != nil {
		return nil, err
	}
	out, err := advanceScans(ids, loadScan, func(s *cq.Scan) (any, error) {
		now, err := hostNow(c)
		if err != nil {
			return nil, err
		}
		before := s.ProgressOffset
		out, err := stepScan(cfg, s, now)
		if err != nil {
			return nil, err
		}
		return continuing(out.(map[string]any), s.ProgressOffset, before), nil
	})
	if view, ok := out.(map[string]any); ok && len(removed) > 0 {
		view["expired_removed"] = removed
	}
	return out, err
}

func continuing(view map[string]any, saved, before int64) map[string]any {
	if words, ok := view["progress"]; ok {
		view["scan_progress"] = words
		delete(view, "progress")
	}
	if saved > before {
		view["progress"] = saved
	}
	if ms, ok := view["retry_not_before_ms"].(int64); ok && ms > 0 {
		view["retry_not_before"] = rfc3339((ms + 999) / 1000)
	}
	return view
}

func unfinished(s *cq.Scan) bool {
	return (s.Status == "running" || s.Status == "judge_unavailable") && scanStepRefusal(s) == ""
}

func rfc3339(sec int64) string {
	days, rem := sec/86400, sec%86400
	if rem < 0 {
		rem += 86400
		days--
	}
	z := days + 719468
	era := z / 146097
	if z < 0 {
		era = (z - 146096) / 146097
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if m > 12 {
		m -= 12
		y++
	}
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02dZ", y, m, d, rem/3600, rem%3600/60, rem%60)
}

func advanceScans(ids []string, load func(string) (*cq.Scan, error), step func(*cq.Scan) (any, error)) (any, error) {
	skipped, unreadable := []any{}, []any{}
	for i, id := range ids {
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

		more := unfinished(s)
		for _, next := range ids[i+1:] {
			if more {
				break
			}
			if n, err := load(next); err == nil {
				more = unfinished(n)
			}
		}
		view["more_work"] = more
		return view, nil
	}
	return map[string]any{"advanced": "", "reason": advanceNone, "report_only": skipped, "unreadable": unreadable}, nil
}

func scanList(cfg config, scans []sdk.FileEntry, load func(string) (*cq.Scan, error)) map[string]any {
	list := listScans(cfg, scanIDsOf(scans), load)
	if cfg.retentionDays() < 1 {

		for _, v := range list {
			if view, ok := v.(map[string]any); ok {
				view["expires_at"] = expiryNoSettings
			}
		}
		return map[string]any{"scans": list}
	}
	expiry := expiryOf(heldScans(scans, cfg.retentionDays()))
	for _, v := range list {
		if view, ok := v.(map[string]any); ok {
			if at, ok := expiry[view["scan_id"].(string)]; ok {
				view["expires_at"] = at
			} else {
				view["expires_at"] = expiryUnknown
			}
		}
	}
	out := map[string]any{"scans": list, "scan_retention_days": cfg.retentionDays()}
	if cfg.keepScans() != keepEveryScan {
		out["keep_scans_notice"] = fmt.Sprintf(keepScansRetired, cfg.keepScans())
	}
	return out
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
		out, err := reportData(c, cfg)
		if m, ok := out.(map[string]any); ok && err == nil {
			if ignored := ignoredArguments(c.Args()); len(ignored) > 0 {
				m["ignored_arguments"] = ignored
			}
		}
		return out, err
	})
}

var shapeArguments = map[string][]string{
	"summary":      {"worst"},
	"findings":     {"cursor", "page_bytes", "min_severity", "language", "path"},
	"review_queue": {"lane", "tier", "path", "page_cursor", "page_bytes", "limit", "list_all"},
	"triage":       {"lane", "tier", "path", "page_cursor", "page_bytes", "limit", "list_all"},
	"coverage":     {"path", "page_cursor", "page_bytes", "limit"},
}

func ignoredArguments(args sdk.Object) []any {
	if !args.Has("scan_id") {
		return nil
	}
	shape, _ := args.String("shape")
	if shape == "" {
		shape = "summary"
	}
	used, ok := shapeArguments[shape]
	if !ok {
		return nil
	}
	ignored := []any{}
	for _, k := range []string{"worst", "cursor", "min_severity", "language", "lane", "tier", "path", "page_cursor", "page_bytes", "limit", "list_all"} {
		if args.Has(k) && !slices.Contains(used, k) {
			ignored = append(ignored, k)
		}
	}
	return ignored
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
		scans, err := listed("scans")
		if err != nil {
			return nil, err
		}
		return scanList(cfg, scans, loadScan), nil
	}
	s, err := loadScan(id)
	if err != nil {
		return nil, err
	}
	if s.Version == cq.RecordVersion {
		return journalReport(args, cfg, s)
	}
	var b []byte
	var recs []cq.Record
	var coverage []cq.Coverage
	complete := true
	{
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
			out["note"] = fmt.Sprintf(reportIncomplete, resultsBytes>>20)
		}
		return out
	}
	shape, _ := args.String("shape")
	if reviewShape(shape) {
		lane, _ := args.String("lane")
		if !reviewLane(lane) {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", laneInvalid)
		}
		tier, _ := args.String("tier")
		glob, _ := args.String("path")
		viewLane := lane
		if shape == "triage" && viewLane == "" {
			viewLane = "correctness"
		}
		view := cq.BuildReview(recs, coverage, s.Version == cq.RecordVersion && s.Status == "done", complete, viewLane)
		checkpoint, e := json.Marshal(s)
		if e != nil {
			return nil, e
		}
		generation := cq.ReviewGeneration(checkpoint, b)
		cursor, _ := args.String("page_cursor")
		pageBytes, limit := defaultPageBytes, reviewLimit(shape)
		if n, ok := args.Int("page_bytes"); ok {
			pageBytes = int(n)
		}
		if n, ok := args.Int("limit"); ok {
			limit = int(n)
		}
		q := cq.ReviewQuery{Shape: shape, Lane: lane, Tier: tier, Path: glob, Cursor: cursor,
			Generation: generation, PageBytes: pageBytes, Limit: limit}
		q.Size.ListAll, _ = args.Bool("list_all")
		page, e := cq.PageReview(view, q)
		if errors.Is(e, cq.ErrTierInvalid) {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", e.Error())
		}
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
	raw, err := pageRecordsJSON(page)
	if err != nil {
		return nil, err
	}

	return incomplete(map[string]any{"root": s.Root, "records": raw, "faults": cq.FaultsOf(page), "next_cursor": next,
		"total_records": len(selected)}), nil
}

func journalReport(args sdk.Object, cfg config, s *cq.Scan) (any, error) {
	j := journal(s)
	shape, _ := args.String("shape")
	if reviewShape(shape) {
		lane, _ := args.String("lane")
		if !reviewLane(lane) {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", laneInvalid)
		}
		checkpoint, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		q := cq.ReviewQuery{Shape: shape, Lane: lane, Generation: cq.ReviewGeneration(checkpoint), PageBytes: defaultPageBytes,
			Limit: reviewLimit(shape), ScanDone: s.Status == "done"}
		q.Cursor, _ = args.String("page_cursor")
		q.Tier, _ = args.String("tier")
		q.Path, _ = args.String("path")
		q.Size = cfg.sizeThresholds()
		q.Size.ListAll, _ = args.Bool("list_all")
		if n, ok := args.Int("page_bytes"); ok {
			q.PageBytes = int(n)
		}
		if n, ok := args.Int("limit"); ok {
			q.Limit = int(n)
		}
		if s.Status == "running" || s.Status == "judge_unavailable" {
			q.Frontier = s.Frontier()
		}
		q.IntentFile, q.IntentUnread = intentFileOf(s)
		q.EarlierVersion = earlierVersion(q.IntentFile != nil)

		shapes := 0
		if q.IntentFile != nil {
			pkg, notes, n, err := cq.PackageFindings(q.IntentFile, s.Status == "done")
			if err != nil {
				notes = append(notes, cq.PackageNote{Dir: ".", Note: "package rules not applied: the side file could not be read (" + err.Error() + ")"})
			}
			q.Package, q.PackageNotes, shapes = pkg, notes, n
		}
		if shapes == 0 && s.FilesJudged > 0 {
			q.SlopNote = slopBeforeR2
		}
		page, err := cq.ReviewFold(j, q)
		runtime.GC()
		switch {
		case errors.Is(err, cq.ErrTierInvalid):
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", err.Error())
		case errors.Is(err, cq.ErrStaleCursor):
			return nil, sdk.Fail("STALE_CURSOR", err.Error())
		case errors.Is(err, cq.ErrReviewEntryTooLarge):
			return nil, sdk.Fail("REVIEW_ENTRY_TOO_LARGE", err.Error())
		case errors.Is(err, cq.ErrJournalInterleaved):
			return nil, sdk.Fail("SCAN_JOURNAL_INTERLEAVED", err.Error())
		case err != nil:
			return nil, err
		}
		raw, err := page.JSON()
		if err != nil {
			return nil, err
		}
		return map[string]any{"root": s.Root, "scan_id": s.ID, "review": json.RawMessage(raw)}, nil
	}
	fold := func(add func(cq.Record)) error {
		_, err := cq.FoldJournal(j, func(run *cq.FileRun) error {
			for _, r := range cq.DeduplicateRecords(run.Records) {
				add(r)
			}
			return nil
		})
		if errors.Is(err, cq.ErrJournalInterleaved) {
			return sdk.Fail("SCAN_JOURNAL_INTERLEAVED", err.Error())
		}
		return err
	}
	if shape == "" || shape == "summary" {
		worst := 10
		if n, ok := args.Int("worst"); ok && n > 0 && n <= 100 {
			worst = int(n)
		}
		sum := cq.NewSummaryFold(worst)
		sum.Size = cfg.sizeThresholds()
		file, unread := intentFileOf(s)
		var packageNotes []cq.PackageNote
		shapes := 0
		if file != nil {
			pkg, notes, n, err := cq.PackageFindings(file, s.Status == "done")
			if err != nil {
				notes = append(notes, cq.PackageNote{Dir: ".", Note: "package rules not applied: the side file could not be read (" + err.Error() + ")"})
			}
			sum.Package, packageNotes, shapes = pkg, notes, n
		}
		cursor := cq.NewIntentCursor(file)
		_, err := cq.FoldJournal(j, func(run *cq.FileRun) error {
			records := cq.DeduplicateRecords(run.Records)
			for _, r := range records {
				sum.Add(r)
			}
			extra, _, _ := cursor.Take(run.Ref)
			if unread != "" {
				extra = []cq.Record{cq.IntentUnreadable(run.Ref)}
			}
			sum.AddRun(append(records, extra...), run.Coverage...)
			return nil
		})
		if unread == "" {
			if e := cursor.Finish(); e != nil {
				unread = e.Error()
			}
		}
		if errors.Is(err, cq.ErrJournalInterleaved) {
			return nil, sdk.Fail("SCAN_JOURNAL_INTERLEAVED", err.Error())
		}
		if err != nil {
			return nil, err
		}
		runtime.GC()
		out := sum.Result()
		if unread != "" {
			out["intent_note"] = fmt.Sprintf(intentUnreadSummary, unread)
		}
		if len(packageNotes) > 0 {
			notes := make([]any, 0, len(packageNotes))
			for _, n := range packageNotes {
				notes = append(notes, map[string]any{"dir": n.Dir, "note": n.Note})
			}
			out["package_notes"] = notes
		}
		if shapes == 0 && s.FilesJudged > 0 {
			out["slop_note"] = slopBeforeR2
		}
		if n, _ := out[cq.NotAssessedEarlier].(int); n > 0 {
			out["earlier_version"] = earlierVersion(file != nil)
		}
		out["scan"] = scanView(cfg, s)
		out["catalogue"] = cq.CatalogueDigest()
		if n := s.Excluded["refused_by_judge"]; n > 0 {
			out["refused_by_judge"] = n
			out["refused_note"] = fmt.Sprintf(refusedNote, n, s.FilesJudged)
		}
		return out, nil
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
	glob, _ := args.String("path")
	page := cq.NewFindingsFold(glob, minSev, strings.ToLower(lang), int(cursor), pageBytes)
	if err := fold(page.Add); err != nil {
		return nil, err
	}
	runtime.GC()
	raw, err := pageRecordsJSON(page.Page)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"root": s.Root, "records": raw, "faults": cq.FaultsOf(page.Page), "next_cursor": page.Next,
		"total_records": page.Total}
	if page.Over > 0 {
		out["page_bytes_exceeded"] = fmt.Sprintf(pageBytesExceeded, page.Over, pageBytes)
	}
	return out, nil
}

func reviewLane(lane string) bool { return lane == "" || slices.Contains(cq.ReviewLanes, lane) }

func pageRecordsJSON(recs []cq.Record) (json.RawMessage, error) {
	type visibleRecord struct {
		cq.Record
		Assessment cq.Assessment `json:"assessment"`
	}
	visible := make([]visibleRecord, 0, len(recs))
	for _, r := range recs {
		a := cq.AssessmentOf(r)
		r.Answers = nil
		visible = append(visible, visibleRecord{r, a})
	}
	if len(visible) == 0 {
		return json.RawMessage("[]"), nil
	}
	b, err := json.Marshal(visible)
	if err != nil {
		return nil, sdk.Fail("RESULT_ENCODE_FAILED", "the records could not be encoded: "+err.Error())
	}
	return b, nil
}

func reviewShape(shape string) bool {
	return shape == "review_queue" || shape == "coverage" || shape == "triage"
}

func reviewLimit(shape string) int {
	if shape == "triage" {
		return defaultTriageLimit
	}
	return defaultReviewLimit
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
