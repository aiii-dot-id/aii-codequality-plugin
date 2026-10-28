package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func scanJudgeT(t *testing.T, cfg config, saved []byte, now int64) (*hostJudge, *cq.Scan, *[]byte) {
	t.Helper()
	if len(saved) == 0 {
		binding, e := scanBinding(cfg)
		if e != nil {
			t.Fatal(e)
		}
		saved, _ = json.Marshal(binding)
	}
	s := &cq.Scan{Runtime: saved}
	j, e := newJudge(cfg, 12)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.bindScan(cfg, s, now); e != nil {
		t.Fatal(e)
	}
	durable := append([]byte(nil), saved...)
	j.persistRuntime = func() error {
		b, e := json.Marshal(j.runtime)
		if e == nil {
			durable = b
			s.Runtime = b
		}
		return e
	}
	return j, s, &durable
}

func TestScanPinPersistsBeforeAnswerAndRejectsDrift(t *testing.T) {
	cfg := config{backend: backendJev, model: "jev-latest", handle: "test-handle", ints: defaultInts()}
	j, s, durable := scanJudgeT(t, cfg, nil, 1000)
	if j.CacheEligible() || j.Model() != "jev-latest" {
		t.Fatal("unresolved alias admitted to the cache")
	}
	cap := &capture{model: "jev-1.14.0"}
	j.send = cap.send
	answers, e := j.Ask(map[string]any{"code": "synthetic"}, map[string]any{"q": map[string]any{"type": "noul"}})
	if e != nil || len(answers) != 1 {
		t.Fatalf("%v %v", answers, e)
	}
	var state scanRequestState
	if e = json.Unmarshal(*durable, &state); e != nil || state.Pin.Model != "jev-1.14.0" {
		t.Fatal("answer admitted before durable pin", state, e)
	}
	resumed, _, _ := scanJudgeT(t, cfg, *durable, 2000)
	if !resumed.CacheEligible() || resumed.configured != "jev-1.14.0" {
		t.Fatal("resume not pinned")
	}
	resumed.send = (&capture{model: "jev-1.15.0"}).send
	if _, e = resumed.Ask(nil, map[string]any{"q": map[string]any{"type": "noul"}}); e == nil {
		t.Fatal("model mixing admitted")
	} else {
		var pin *ModelPinError
		if !errors.As(e, &pin) || pin.Reason != "model_mismatch" {
			t.Fatalf("wrong refusal: %v", e)
		}
	}
	changed := cfg
	changed.model = "jev-preview"
	other, e := newJudge(changed, 2)
	if e != nil {
		t.Fatal(e)
	}
	if e = other.bindScan(changed, s, 2000); e == nil {
		t.Fatal("changed requested model admitted")
	}

	j, _, _ = scanJudgeT(t, cfg, nil, 1000)
	j.send = cap.send
	j.persistRuntime = func() error { return errors.New("storage unavailable") }
	if a, e := j.Ask(nil, nil); !errors.Is(e, cq.ErrProgress) || a != nil {
		t.Fatalf("write failure accepted: %v %v", a, e)
	}
}

type cacheProbe struct {
	cq.MemCache
	reads int
}

func (c *cacheProbe) Get(key string) (json.RawMessage, bool) { c.reads++; return c.MemCache.Get(key) }
func TestScanFreshAliasBypassesCacheThenUsesConcreteModel(t *testing.T) {
	cfg := config{backend: backendJev, model: "jev-latest", handle: "test-handle", ints: defaultInts()}
	j, _, durable := scanJudgeT(t, cfg, nil, 1000)
	c := &cacheProbe{MemCache: cq.MemCache{}}
	cap := &capture{model: "jev-1.14.0"}
	j.send = func(url string, o *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		if len(cap.urls) == 0 && c.reads != 0 {
			t.Fatal("unresolved alias read cache")
		}
		return cap.send(url, o)
	}
	records, e := cq.JudgeText(tableT(t), j, c, "x.go", "go", goSource)
	if e != nil || len(records) != 2 || len(cap.urls) != 2 {
		t.Fatalf("%v records=%d calls=%d", e, len(records), len(cap.urls))
	}
	if !strings.Contains(cap.opts[0].Body, `"model":"jev-latest"`) || !strings.Contains(cap.opts[1].Body, `"model":"jev-1.14.0"`) {
		t.Fatal("model not pinned between batteries")
	}
	again, _, _ := scanJudgeT(t, cfg, *durable, 2000)
	again.send = func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		t.Fatal("pinned cache miss")
		return sdk.HTTPResult{}, nil
	}
	if _, e = cq.JudgeText(tableT(t), again, c, "x.go", "go", goSource); e != nil {
		t.Fatal(e)
	}

	local := config{backend: backendLocal, localBase: "https://fixture.invalid/a", model: "fixed", ints: defaultInts()}
	a, _, _ := scanJudgeT(t, local, nil, 1000)
	local.localBase = "https://fixture.invalid/b"
	b, _, _ := scanJudgeT(t, local, nil, 1000)
	if a.Backend() == b.Backend() {
		t.Fatal("local endpoint cache collision")
	}
}

func TestScanBackoffSurvivesRestartAndStopsBeforeSend(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	j, _, durable := scanJudgeT(t, cfg, nil, 1000)
	hint := int64(60)
	calls := 0
	j.send = func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		calls++
		return sdk.HTTPResult{Status: 429, RetryAfterS: &hint}, errors.New("HTTP failure")
	}
	if _, e := j.Ask(nil, nil); !errors.Is(e, cq.ErrJudge) {
		t.Fatal(e)
	}
	var state scanRequestState
	_ = json.Unmarshal(*durable, &state)
	if state.Retry.Attempts != 1 || state.Retry.NotBeforeMS < 61000 {
		t.Fatal(state)
	}
	r, _, saved := scanJudgeT(t, cfg, *durable, state.Retry.NotBeforeMS-1)
	r.send = func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		calls++
		t.Fatal("early retry")
		return sdk.HTTPResult{}, nil
	}
	if _, e := r.Ask(map[string]any{"different": "battery"}, nil); !errors.Is(e, cq.ErrJudge) {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	due, _, _ := scanJudgeT(t, cfg, *saved, state.Retry.NotBeforeMS)
	due.send = (&capture{model: "fixed"}).send
	if _, e := due.Ask(nil, map[string]any{"q": map[string]any{"type": "noul"}}); e != nil {
		t.Fatal(e)
	}
	if due.runtime.Retry.Attempts != 0 {
		t.Fatal("successful retry did not reset")
	}

	for _, status := range []int{400, 413, 401} {
		j, _, saved := scanJudgeT(t, cfg, nil, 1000)
		j.send = func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) { return sdk.HTTPResult{Status: status}, nil }
		if _, e := j.Ask(nil, nil); e == nil || errors.Is(e, cq.ErrJudge) {
			t.Fatalf("%d: refusal accepted or retried: %v", status, e)
		}
		r, _, _ := scanJudgeT(t, cfg, *saved, 1000)
		r.send = (&capture{model: "fixed"}).send
		if _, e := r.Ask(map[string]any{"other": "text"}, map[string]any{"q": map[string]any{"type": "noul"}}); e != nil {
			t.Fatalf("%d: refusal blocked the next request: %v", status, e)
		}
	}
}

func refusingScan(t *testing.T, refusal sdk.HTTPResult) (*cq.Scan, *hostJudge, *cq.OSFS) {
	t.Helper()
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	root := t.TempDir()
	for name, body := range map[string]string{"a.go": goSource, "b.go": strings.Replace(goSource, "some text", "python -c text", 1), "c.go": goSource + "\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs := &cq.OSFS{Root: root}
	s, err := cq.NewScan(fs, "cq_refuse", "src", "", nil, nil, false, 100, 131072)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := scanBinding(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Runtime, _ = json.Marshal(binding)
	j := scanStepJudge(t, cfg, s, 1000)
	ok := &capture{model: "fixed"}
	j.send = func(url string, o *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		if strings.Contains(o.Body, "python -c") {
			return refusal, errors.New("HTTP failure")
		}
		return ok.send(url, o)
	}
	return s, j, fs
}

func scanStepJudge(t *testing.T, cfg config, s *cq.Scan, now int64) *hostJudge {
	t.Helper()
	j, err := newJudge(cfg, 64)
	if err != nil {
		t.Fatal(err)
	}
	j.writeRuntimeCheckpoint = func([]byte) error { return nil }
	if err = j.bindScan(cfg, s, now); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestScanRecordsATextTheJudgeRefusesAndMovesOn(t *testing.T) {
	for _, refusal := range []sdk.HTTPResult{
		{Status: 403, Body: json.RawMessage(`"<html>Attention Required</html>"`)},
		{Status: 400, Body: json.RawMessage(`{"error":"state too large"}`)},
		{Status: 413}, {Status: 422, Body: json.RawMessage(`{}`)}, {Status: 404},
	} {
		s, j, fs := refusingScan(t, refusal)
		recs, err := s.Step(tableT(t), fs, j, cq.MemCache{}, 64, 50)
		if err != nil || s.Status != "done" || j.runtime.Blocked != "" || s.FilesJudged != 2 ||
			s.Excluded["refused_by_judge"] != 1 || len(s.Refused) != 1 || s.Refused[0] != "b.go" {
			t.Fatalf("%d: err %v status %s blocked %q judged %d refused %v", refusal.Status, err, s.Status, j.runtime.Blocked, s.FilesJudged, s.Refused)
		}
		refused := 0
		for _, r := range recs {
			if r.Ref == "b.go" {
				refused++
				if a := cq.AssessmentOf(r); a.Status != "unknown" || a.Missing == 0 {
					t.Fatalf("%d: refusal misrepresented: %+v", refusal.Status, a)
				}
			}
		}
		if refused != 1 {
			t.Fatalf("%d: refused records = %d", refusal.Status, refused)
		}
	}
}

func TestScanAuthenticationFailureStopsTheStepNotTheScan(t *testing.T) {
	for _, refusal := range []sdk.HTTPResult{
		{Status: 401, Body: json.RawMessage(`{"error":"bad key"}`)},
		{Status: 403, Body: json.RawMessage(`{"error":"forbidden"}`)},
	} {
		s, j, fs := refusingScan(t, refusal)
		_, err := s.Step(tableT(t), fs, j, cq.MemCache{}, 64, 50)
		var request *cq.RequestError
		if !errors.As(err, &request) || request.Reason != cq.RequestAuthentication || !errors.Is(err, cq.ErrKey) ||
			j.runtime.Blocked != "" || len(s.Pending) == 0 || s.Pending[0].Name != "b.go" {
			t.Fatalf("%d: err %v blocked %q pending %v", refusal.Status, err, j.runtime.Blocked, s.Pending)
		}
		if _, code := failure(err); reasonOf(code) != "JUDGE_AUTHENTICATION_REQUIRED" || !strings.Contains(code.Error(), "plugin's card") {
			t.Fatalf("%d: operator route missing: %v", refusal.Status, code)
		}
		cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
		fixed := scanStepJudge(t, cfg, s, 2000)
		fixed.send = (&capture{model: "fixed"}).send
		if _, err := s.Step(tableT(t), fs, fixed, cq.MemCache{}, 64, 50); err != nil || s.Status != "done" || s.FilesJudged != 3 {
			t.Fatalf("%d: no resume once the key works: %v %s judged %d", refusal.Status, err, s.Status, s.FilesJudged)
		}
	}
}

func TestRetryExhaustionKeepsTheScanResumable(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	_, _, durable := scanJudgeT(t, cfg, nil, 1000)
	now := int64(1000)
	unavailable := func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		return sdk.HTTPResult{Status: 503}, errors.New("HTTP failure")
	}
	for attempt := 0; attempt <= int(defaultRetryBudget); attempt++ {
		j, _, saved := scanJudgeT(t, cfg, *durable, now)
		j.send = unavailable
		if _, e := j.Ask(nil, nil); !errors.Is(e, cq.ErrJudge) {
			t.Fatalf("attempt %d: %v", attempt, e)
		}
		durable = saved
		var state scanRequestState
		if e := json.Unmarshal(*durable, &state); e != nil || state.Blocked != "" {
			t.Fatalf("attempt %d: outage blocked the scan: %+v %v", attempt, state, e)
		}
		now = max(now, state.Retry.NotBeforeMS)
	}
	var state scanRequestState
	_ = json.Unmarshal(*durable, &state)
	if state.Retry.Attempts != 0 {
		t.Fatalf("exhausted budget did not start a fresh round: %+v", state.Retry)
	}
	back, _, _ := scanJudgeT(t, cfg, *durable, now)
	back.send = (&capture{model: "fixed"}).send
	if _, e := back.Ask(nil, map[string]any{"q": map[string]any{"type": "noul"}}); e != nil {
		t.Fatalf("judge back, scan not resumed: %v", e)
	}
}

const twoUnitSource = "package x\n\nfunc Good(a, b int) int {\n\treturn a + b\n}\n\nfunc Bad(path string) string {\n\tb, _ := os.ReadFile(path)\n\treturn string(b) + \"padding padding padding\"\n}\n"

type memJournalStore map[string][]byte

func (m memJournalStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	b, ok := m[p]
	if !ok {
		return nil, false, errors.New("FS_NOT_FOUND")
	}
	if off >= int64(len(b)) {
		return nil, true, nil
	}
	end := min(off+int64(n), int64(len(b)))
	return append([]byte(nil), b[off:end]...), end == int64(len(b)), nil
}
func (m memJournalStore) Write(p string, b []byte) error {
	m[p] = append([]byte(nil), b...)
	return nil
}
func (m memJournalStore) Append(p string, b []byte) error { m[p] = append(m[p], b...); return nil }

func oneFileScan(t *testing.T, cfg config, source string) (*cq.Scan, *cq.OSFS, memJournalStore) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &cq.OSFS{Root: root}
	s, err := cq.NewScan(fs, "cq_one", "src", "", nil, nil, false, 100, 131072)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := scanBinding(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Runtime, _ = json.Marshal(binding)
	store := memJournalStore{"journal": nil}
	return s, fs, store
}

func stepOnce(t *testing.T, cfg config, s *cq.Scan, fs *cq.OSFS, store memJournalStore, now int64, send poster) error {
	t.Helper()
	p := &cq.JournalProgress{Journal: &cq.Journal{Store: store, Path: "journal"}, Offset: s.ProgressOffset}
	if err := p.Verify(); err != nil {
		t.Fatal(err)
	}
	s.Progress = p
	j := scanStepJudge(t, cfg, s, now)
	j.send = send
	_, err := s.Step(tableT(t), fs, j, cq.MemCache{}, 12, 50)
	return err
}

func TestAFailingLocalizationEndsAfterOneRetryRound(t *testing.T) {
	ints := defaultInts()
	ints["retry_budget"] = 2
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: ints}
	s, fs, store := oneFileScan(t, cfg, twoUnitSource)
	located := 0
	send := func(url string, o *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		var req struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		_ = json.Unmarshal([]byte(o.Body), &req)
		answers := map[string]any{}
		for id, q := range req.Questions {
			if strings.Contains(string(q), `"choice"`) {
				located++
				return sdk.HTTPResult{Status: 503}, errors.New("HTTP failure")
			}
			answers[id] = map[string]any{"type": "noul", "noul": 0.9}
		}
		body, _ := json.Marshal(map[string]any{"model": "fixed", "answers": answers})
		return sdk.HTTPResult{Status: 200, Body: body}, nil
	}
	now := int64(1000)
	for step := 0; step < 8 && s.Status != "done"; step++ {
		if err := stepOnce(t, cfg, s, fs, store, now, send); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		var state scanRequestState
		_ = json.Unmarshal(s.Runtime, &state)
		now = max(now+1, state.Retry.NotBeforeMS)
	}
	if s.Status != "done" || s.FilesJudged != 1 || located != 3 {
		t.Fatalf("status %s judged %d location attempts %d (want the initial call and two retries)", s.Status, s.FilesJudged, located)
	}
	records, _, _, _, err := cq.JournalReport(&cq.Journal{Store: store, Path: "journal"}, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	docs := false
	for _, r := range records {
		if r.Battery == "docs" {
			docs = true
		}
		for _, f := range r.Findings {
			if r.Battery == "code" && f.LocationReason != "localization_failed" {
				t.Fatalf("finding not given up: %+v", f)
			}
		}
	}
	if !docs {
		t.Fatal("the documentation battery never ran")
	}
}

func TestARepeatedAuthStopAppendsNothing(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	s, fs, store := oneFileScan(t, cfg, goSource)
	refused := func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		return sdk.HTTPResult{Status: 401, Body: json.RawMessage(`{"error":"bad key"}`)}, errors.New("HTTP failure")
	}
	var sizes []int
	for step := 0; step < 3; step++ {
		if err := stepOnce(t, cfg, s, fs, store, int64(1000+step), refused); !errors.Is(err, cq.ErrKey) {
			t.Fatalf("step %d: %v", step, err)
		}
		sizes = append(sizes, len(store["journal"]))
	}
	if sizes[1] != sizes[0] || sizes[2] != sizes[0] {
		t.Fatalf("each auth stop appended to the journal: sizes %v", sizes)
	}
}
