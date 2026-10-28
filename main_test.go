// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
	"github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiospkg"
)

// values is a settings reader over a map, as the host hands values over.
type values map[string]any

func (v values) String(k string) (string, bool) { s, ok := v[k].(string); return s, ok }
func (v values) Handle(k string) (string, bool) { return v.String(k) }
func (v values) Int(k string) (int64, bool)     { n, ok := v[k].(int); return int64(n), ok }
func (v values) Bool(k string) (bool, bool)     { b, ok := v[k].(bool); return b, ok }

func mustConfig(t *testing.T, v values) config {
	t.Helper()
	c, err := configFrom(v)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func reasonOf(err error) string {
	var op *sdk.OperationError
	if errors.As(err, &op) {
		return op.ReasonCode
	}
	return ""
}

// A step ends inside the host's invoke wall whatever the judge does, because a guest has no
// clock to watch: every call may take its whole timeout, and the calls a step makes, each at its
// timeout, never exceed the step's time — at the defaults and at any step time and call count
// the operator sets.
func TestAStepFitsTheInvokeWall(t *testing.T) {
	for _, v := range []values{{}, {"step_ms": 60000, "max_calls_per_step": 7, "max_call_ms": 20000}, {"step_ms": 1000, "max_calls_per_step": 64}} {
		c := mustConfig(t, v)
		for calls := 1; calls <= c.maxCalls(); calls++ {
			if worst := calls * c.callMS(calls); worst > c.stepMS() {
				t.Fatalf("%v: %d calls at %d ms is %d ms, past the %d ms a step may spend on the judge", v, calls, c.callMS(calls), worst, c.stepMS())
			}
		}
	}
	c := mustConfig(t, values{})
	if c.callMS(c.maxCalls()) != 2000 || c.callMS(c.retryCalls()) != defaultMaxCallMS || c.callMS(1) != defaultMaxCallMS {
		t.Fatalf("by default a step's calls get 2 s, a retry's and a lone text's 8 s: %d %d %d", c.callMS(c.maxCalls()), c.callMS(c.retryCalls()), c.callMS(1))
	}
}

// A judge makes only the calls it was planned for; the next is refused as the judge's failure,
// before anything is sent, so the step ends in time and the next one resumes from the cache.
func TestAJudgeStopsAtTheCallsPlanned(t *testing.T) {
	sent := 0
	j := &hostJudge{at: judgeAt{systemOne: "x"}, configured: "m", model: "m", left: 0,
		send: func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) { sent++; return sdk.HTTPResult{}, nil }}
	if _, err := j.Ask(map[string]any{"code": "x"}, map[string]any{}); !errors.Is(err, errCallsSpent) || !errors.Is(err, cq.ErrJudge) || sent != 0 {
		t.Fatalf("a call past the planned ones is refused as the judge's failure, unsent: %v (sent %d)", err, sent)
	}
	c := mustConfig(t, values{})
	if stepCalls(c, &cq.Scan{Status: "running"}) != 12 || stepCalls(c, &cq.Scan{Status: "judge_unavailable"}) != 3 {
		t.Fatal("by default a step plans twelve calls, and three after one that found the judge unavailable")
	}
	c = mustConfig(t, values{"max_calls_per_step": 20, "retry_calls": 5})
	if stepCalls(c, &cq.Scan{Status: "running"}) != 20 || stepCalls(c, &cq.Scan{Status: "judge_unavailable"}) != 5 {
		t.Fatal("the operator's calls per step and retry calls are the ones a step plans")
	}
}

// Every error an operation returns leaves named: the kit carries an operation error's words
// and drops the text of any other, so a judge outage, a refused key, a refused text and an
// unexpected failure each reach the caller with a code and a reason, and an operation error
// already named passes as it is.
func TestEveryErrorLeavesNamed(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("%w: the judge answered 503", cq.ErrJudge), "JUDGE_UNAVAILABLE"},
		{cq.ClassifyHTTPFailure(401, "", nil, nil), "JUDGE_AUTHENTICATION_REQUIRED"},
		{cq.ClassifyHTTPFailure(403, "", []byte(`<html>`), nil), "JUDGE_REFUSED"},
		{cq.ClassifyHTTPFailure(400, "", nil, nil), "JUDGE_REQUEST_VALIDATION"},
		{fmt.Errorf("%w; %w", cq.ClassifyHTTPFailure(503, "", nil, nil), cq.ErrRetryExhausted), "JUDGE_UNAVAILABLE"},
		{cq.ClassifyHTTPFailure(429, "", nil, nil), "JUDGE_UNAVAILABLE"},
		{errors.New("state file unreadable"), "CODEQUALITY_FAILED"},
		{sdk.Fail("SCAN_EXISTS", "a scan named x exists"), "SCAN_EXISTS"},
	} {
		_, err := named(func(sdk.Call) (any, error) { return nil, c.err })(sdk.Call{})
		var op *sdk.OperationError
		if !errors.As(err, &op) || op.ReasonCode != c.code || op.Reason == "" {
			t.Fatalf("%v left as %#v, want %s with its reason", c.err, err, c.code)
		}
	}
	if out, err := named(func(sdk.Call) (any, error) { return "ok", nil })(sdk.Call{}); err != nil || out != "ok" {
		t.Fatal("a result passes untouched")
	}
}

type manifest struct {
	Envelope []string `json:"capability_envelope"`
	Variants []struct {
		Caps []string `json:"variant_capabilities"`
	} `json:"variants"`
	Settings []aiiospkg.SettingDecl  `json:"settings"`
	Schedule []aiiospkg.ScheduleDecl `json:"schedule"`
}

func readManifest(t *testing.T) manifest {
	t.Helper()
	b, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Every value that depends on the judge or the machine is an operator's setting with a default
// (OP-CQ-2): the package declares each with the default and range this code reads, the kit's
// own validator accepts the declaration, and the key is not required, since a local judge needs
// none.
func TestEveryBackendDependentValueIsASetting(t *testing.T) {
	m := readManifest(t)
	if err := aiiospkg.ValidateSettings(m.Settings); err != nil {
		t.Fatalf("the kit refuses the settings: %v", err)
	}
	decl := map[string]aiiospkg.SettingDecl{}
	for _, d := range m.Settings {
		decl[d.Key] = d
	}
	for _, b := range bounds {
		d, ok := decl[b.key]
		if !ok || d.Type != "integer" || d.Minimum == nil || d.Maximum == nil {
			t.Fatalf("%s is not declared as an integer setting with a range: %+v", b.key, d)
		}
		if def, _ := d.Default.(float64); int64(def) != b.def || int64(*d.Minimum) != b.min || int64(*d.Maximum) != b.max {
			t.Fatalf("%s: declared default %v range %v..%v, read as %d in %d..%d", b.key, d.Default, *d.Minimum, *d.Maximum, b.def, b.min, b.max)
		}
	}
	if d := decl["backend"]; d.Type != "enum" || strings.Join(d.Values, ",") != "jev,local" || d.Default != "jev" {
		t.Fatalf("backend: %+v", d)
	}
	if d := decl["local_judge_url"]; d.Type != "string" {
		t.Fatalf("local_judge_url: %+v", d)
	}
	if d := decl["scheduled_steps"]; d.Type != "boolean" || d.Default != true {
		t.Fatalf("scheduled_steps: %+v", d)
	}
	if decl["api_key"].Required {
		t.Fatal("a local judge needs no key, so the key cannot be required")
	}
	if decl["model"].ChoicesFrom != "models" {
		t.Fatal("the model picker lost its operation")
	}
}

// The settings are read with their defaults under the operator's values, and a value outside
// the declaration is refused by its key rather than used.
func TestSettingsAreReadAndHeldToTheirRanges(t *testing.T) {
	c := mustConfig(t, values{})
	if c.backend != backendJev || c.model != jevDefault || c.chunkBytes() != cq.ChunkBytes || c.maxFileBytes() != 128<<10 ||
		c.keepScans() != 8 || c.cacheBytes() != 32<<20 || !c.scheduled {
		t.Fatalf("defaults: %+v", c)
	}
	c = mustConfig(t, values{"backend": "local", "local_judge_url": "http://judge.lan:8080/", "chunk_bytes": 8000,
		"max_file_bytes": 65536, "keep_scans": 3, "cache_max_bytes": 4 << 20, "scheduled_steps": false, "model": "kev-9b"})
	if c.backend != backendLocal || c.localBase != "http://judge.lan:8080" || c.chunkBytes() != 8000 || c.maxFileBytes() != 65536 ||
		c.keepScans() != 3 || c.cacheBytes() != 4<<20 || c.scheduled || c.model != "kev-9b" {
		t.Fatalf("operator's values: %+v", c)
	}
	for _, v := range []values{{"chunk_bytes": 1}, {"max_calls_per_step": 0}, {"step_ms": 999999}, {"backend": "both"}, {"local_judge_url": "ftp://x"}} {
		if _, err := configFrom(v); reasonOf(err) != "JUDGE_CONFIGURATION_INVALID" {
			t.Fatalf("%v accepted: %v", v, err)
		}
	}
}

func TestALocalJudgeAddressIsHeldToItsShape(t *testing.T) {
	for raw, want := range map[string]string{
		"http://judge.lan:8080":              "http://judge.lan:8080",
		"https://judge.lan/":                 "https://judge.lan",
		"http://judge.lan:8080/v1/systemone": "http://judge.lan:8080",
		"http://judge.lan:8080/jev/":         "http://judge.lan:8080/jev",
		" http://judge.lan:9000 ":            "http://judge.lan:9000",
		"http://user:pw@example.invalid":     "",
		"http://judge.lan/?q=1":              "",
		"judge.lan:8080":                     "",
		"http://":                            "",
		"file:///etc/passwd":                 "",
		"http://judge.lan/#frag":             "",
	} {
		got, why := localBase(raw)
		if got != want || (want == "") != (why != "") {
			t.Errorf("%q: got %q (%s), want %q", raw, got, why, want)
		}
	}
}

// capture is a judge transport that records what was sent and answers as Jev's wire does.
type capture struct {
	urls  []string
	opts  []*sdk.HTTPOptions
	model string // the model the replies name
}

func (c *capture) send(url string, o *sdk.HTTPOptions) (sdk.HTTPResult, error) {
	c.urls = append(c.urls, url)
	c.opts = append(c.opts, o)
	var req struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	_ = json.Unmarshal([]byte(o.Body), &req)
	answers := map[string]any{}
	for id, q := range req.Questions {
		if strings.Contains(string(q), `"choice"`) {
			answers[id] = map[string]any{"type": "choice", "choice": "none", "confidence": 0.1}
		} else {
			answers[id] = map[string]any{"type": "noul", "noul": 0.1}
		}
	}
	body, _ := json.Marshal(map[string]any{"model": c.model, "answers": answers})
	return sdk.HTTPResult{Status: 200, Body: body}, nil
}

const goSource = "package x\n\n// Adds.\nfunc Add(a, b int) int {\n\treturn a + b + 1000000 + len(\"some text that makes it long enough\")\n}\n"

func tableT(t *testing.T) *cq.Table {
	t.Helper()
	tb, err := cq.LoadTable()
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

// One wire, two addresses (F-CQ-004): the same request goes to TypeSafe's hosted service with the
// operator's key, or to the operator's local endpoint with no key at all; the result says which.
func TestOneWireTwoAddresses(t *testing.T) {
	tb := tableT(t)
	for _, tc := range []struct {
		v              values
		url, auth, who string
		sent           []string // the model each request names: an alias until its first reply pins it
	}{
		{values{"api_key": "jev"}, "https://api.typesafe.ai/v1/systemone", "jev", "api.typesafe.ai", []string{"jev-latest", "jev-1.13.0"}},
		{values{"backend": "local", "local_judge_url": "http://judge.lan:8080", "api_key": "jev", "model": "jev-1.13.0"}, "http://judge.lan:8080/v1/systemone", "", "judge.lan:8080", []string{"jev-1.13.0", "jev-1.13.0"}},
	} {
		c := mustConfig(t, tc.v)
		j, err := newJudge(c, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err = j.bindInvocation(c, 1000); err != nil {
			t.Fatal(err)
		}
		tr := &capture{model: "jev-1.13.0"}
		j.send = tr.send
		recs, err := cq.JudgeText(tb, j, cq.MemCache{}, "x.go", "go", goSource)
		if err != nil || len(recs) != 2 || recs[0].Battery != "code" || recs[1].Battery != "docs" || len(tr.urls) != 2 {
			t.Fatalf("%v: %v, records %+v, calls %d", tc.v, err, recs, len(tr.urls))
		}
		for _, r := range recs {
			if r.Model != "jev-1.13.0" || r.Backend != j.Backend() {
				t.Fatalf("battery attribution: %+v", r)
			}
		}
		for i, u := range tr.urls {
			var sent map[string]json.RawMessage
			if u != tc.url || tr.opts[i].AuthProfile != tc.auth || json.Unmarshal([]byte(tr.opts[i].Body), &sent) != nil ||
				len(sent) != 3 || string(sent["model"]) != `"`+tc.sent[i]+`"` {
				t.Fatalf("%v: sent %s with key %q: %s", tc.v, u, tr.opts[i].AuthProfile, tr.opts[i].Body)
			}
		}
		if got := j.at.sentTo()["address"]; got != tc.who {
			t.Fatalf("%v: the result names %v as where the text went", tc.v, got)
		}
	}
	if _, err := newJudge(mustConfig(t, values{}), 1); reasonOf(err) != "JUDGE_UNCONFIGURED" {
		t.Fatalf("hosted judge without a key: %v", err)
	}
	if _, err := newJudge(mustConfig(t, values{"backend": "local", "api_key": "jev"}), 1); reasonOf(err) != "JUDGE_UNCONFIGURED" {
		t.Fatalf("local judge without an address: %v", err)
	}
}

// The model a reply names is the model recorded and the model its answers are kept under
// (OP-CQ-5), through the production binding each invocation makes: an alias is asked fresh
// until its first reply pins the concrete model, answers of a model the alias has moved off are
// asked again, not reused, and two judges that use one model name never share answers.
func TestTheRepliedModelIsRecordedAndKeysTheCache(t *testing.T) {
	tb := tableT(t)
	cache := cq.MemCache{}
	hosted := mustConfig(t, values{"api_key": "jev"})
	ask := func(c config, model, text string, wantCalls, wantCached int) {
		t.Helper()
		j, err := newJudge(c, 3)
		if err != nil {
			t.Fatal(err)
		}
		if err = j.bindInvocation(c, 1000); err != nil {
			t.Fatal(err)
		}
		tr := &capture{model: model}
		j.send = tr.send
		recs, err := cq.JudgeText(tb, j, cache, "x.go", "go", text)
		if err != nil || len(recs) != 2 || recs[0].Battery != "code" || recs[1].Battery != "docs" {
			t.Fatalf("%v: %+v", err, recs)
		}
		calls, cached := 0, 0
		for _, r := range recs {
			if r.Model != model || r.Backend != j.Backend() {
				t.Fatalf("battery %s recorded model %q backend %q, want %q %q", r.Battery, r.Model, r.Backend, model, j.Backend())
			}
			calls += r.Calls
			cached += r.Cached
		}
		if calls != wantCalls || len(tr.urls) != wantCalls || cached != wantCached {
			t.Fatalf("%s %s: calls %d (sent %d), cached %d; want %d, %d", j.Backend(), model, calls, len(tr.urls), cached, wantCalls, wantCached)
		}
	}
	ask(hosted, "jev-1.13.0", goSource, 2, 0)
	ask(hosted, "jev-1.13.0", goSource, 1, 1) // the alias is asked fresh; the pinned model's docs answer is kept
	other := strings.Replace(goSource, "Add", "Sum", -1)
	ask(hosted, "jev-1.14.0", other, 2, 0)
	ask(hosted, "jev-1.14.0", goSource, 2, 0) // the alias moved: 1.13.0's answers are not reused
	local := mustConfig(t, values{"backend": "local", "local_judge_url": "http://judge.lan:8080", "model": "jev-1.14.0"})
	ask(local, "jev-1.14.0", goSource, 2, 0)
}

// Each write.external summary names where the text goes (OP-CQ-3), and fits the host's 256
// bytes for an author's line, so the model reads it whole.
func TestSummariesNameWhereTheTextGoes(t *testing.T) {
	for _, s := range []string{judgeSummary, scanSummary} {
		if !strings.Contains(s, "api.typesafe.ai") || !strings.Contains(s, "operator's network") || !strings.Contains(s, "sent to") {
			t.Errorf("does not say where the text goes: %q", s)
		}
	}
	for _, s := range []string{judgeSummary, scanSummary, modelsSummary, advanceSummary} {
		if len(s) > 256 {
			t.Errorf("%d bytes, past the host's 256: %q", len(s), s)
		}
	}
}

// The confirmation stance is declared for every external write: each is marked unconfirmed by
// design, with a reason that says where the text goes, as the kit's qualifier requires.
func TestEveryExternalWriteDeclaresItsStance(t *testing.T) {
	marked := map[string]string{}
	files, _ := filepath.Glob("tests/*.json")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Operation string `json:"operation"`
			Why       string `json:"unconfirmed_by_design"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if c.Why != "" {
			marked[c.Operation] = c.Why
		}
	}
	for _, op := range []string{"judge", "scan", "advance"} {
		if why := marked[op]; !strings.Contains(why, "api.typesafe.ai") || !strings.Contains(why, "local") {
			t.Errorf("%s: stance %q does not name where the text goes", op, why)
		}
	}
}

// A long scan is stepped on the host's schedule (row 42): the package schedules advance at the
// interval the progress line names, within the kit's bounds.
func TestTheScheduleIsDeclaredAsTheProgressLineSaysIt(t *testing.T) {
	m := readManifest(t)
	if err := aiiospkg.ValidateSchedule(m.Schedule); err != nil {
		t.Fatal(err)
	}
	if len(m.Schedule) != 1 || m.Schedule[0].Operation != "advance" || m.Schedule[0].EverySeconds != scheduleSecs {
		t.Fatalf("schedule: %+v", m.Schedule)
	}
}

// A scan says what it still costs: about how many steps, and either that it advances by itself
// every interval or that each step is a call of the caller's; nothing once it has ended.
func TestAScanSaysWhatItStillCosts(t *testing.T) {
	s := &cq.Scan{Status: "running", Stack: []string{"a", "b"}, Pending: make([]cq.Entry, 30), DirsListed: 2, FilesListed: 40,
		FilesSeen: 10, FilesJudged: 5, Calls: 10, MaxFiles: 5000}
	n := s.StepsLeft(12, filesPerStep)
	if n < 1 {
		t.Fatalf("steps left %d", n)
	}
	if got := progress(mustConfig(t, values{}), s); got != fmt.Sprintf("This scan needs about %d more steps. It advances by itself every 600 seconds; `report` shows how far it has come.", n) {
		t.Fatalf("scheduled: %q", got)
	}
	if got := progress(mustConfig(t, values{"scheduled_steps": false}), s); got != fmt.Sprintf("This scan needs about %d more steps, and each step is one call of yours.", n) {
		t.Fatalf("manual: %q", got)
	}
	s.Status = "done"
	if got := progress(mustConfig(t, values{}), s); got != "" {
		t.Fatalf("an ended scan: %q", got)
	}
}

// The local judge is reached under net.local, declared in the envelope, on every variant and on
// each operation that calls the judge.
func TestTheLocalJudgeIsReachedUnderNetLocal(t *testing.T) {
	m := readManifest(t)
	has := func(xs []string, x string) bool {
		for _, v := range xs {
			if v == x {
				return true
			}
		}
		return false
	}
	if !has(m.Envelope, netLocal) || !has(m.Envelope, jevHost) {
		t.Fatalf("envelope: %v", m.Envelope)
	}
	for _, v := range m.Variants {
		if !has(v.Caps, netLocal) {
			t.Fatalf("variant without net.local: %v", v.Caps)
		}
	}
	if !has(caps, netLocal) {
		t.Fatal("judge, scan and advance do not declare net.local")
	}
}

// The retry budget is the operator's, defaulting to 0.1.14's eight, held to its range.
func TestRetryBudgetIsASetting(t *testing.T) {
	if c := mustConfig(t, values{}); c.retryBudget() != 8 {
		t.Fatalf("default: %d", c.retryBudget())
	}
	if c := mustConfig(t, values{"retry_budget": 0}); c.retryBudget() != 0 {
		t.Fatalf("operator's value: %d", c.retryBudget())
	}
	for _, v := range []values{{"retry_budget": -1}, {"retry_budget": 33}} {
		if _, err := configFrom(v); reasonOf(err) != "JUDGE_CONFIGURATION_INVALID" {
			t.Fatalf("%v accepted: %v", v, err)
		}
	}
}

// defaultInts is every integer setting at its declared default, as configFrom reads an empty card.
func defaultInts() map[string]int64 {
	m := map[string]int64{}
	for _, b := range bounds {
		m[b.key] = b.def
	}
	return m
}
