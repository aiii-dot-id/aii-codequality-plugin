package main

import (
	"encoding/json"
	"errors"
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
	j, e := newJudge(cfg, 12, cq.LoadMemo(nil))
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
	cfg := config{backend: backendJev, model: "jev-latest", handle: "test-handle", ints: map[string]int64{}}
	j, s, durable := scanJudgeT(t, cfg, nil, 1000)
	j.memo.Learn(j.at.backend, cfg.model, "jev-1.0.0")
	if j.CacheEligible() || j.Model() != "jev-latest" {
		t.Fatal("stale alias memo admitted")
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
	other, e := newJudge(changed, 2, cq.LoadMemo(nil))
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
	cfg := config{backend: backendJev, model: "jev-latest", handle: "test-handle", ints: map[string]int64{}}
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

	local := config{backend: backendLocal, localBase: "https://fixture.invalid/a", model: "fixed", ints: map[string]int64{}}
	a, _, _ := scanJudgeT(t, local, nil, 1000)
	local.localBase = "https://fixture.invalid/b"
	b, _, _ := scanJudgeT(t, local, nil, 1000)
	if a.Backend() == b.Backend() {
		t.Fatal("local endpoint cache collision")
	}
}

func TestScanBackoffSurvivesRestartAndStopsBeforeSend(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: map[string]int64{}}
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
		if _, e := j.Ask(nil, nil); e == nil {
			t.Fatal("terminal failure accepted")
		}
		r, _, _ := scanJudgeT(t, cfg, *saved, 999999)
		r.send = func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) {
			t.Fatal("terminal request retried")
			return sdk.HTTPResult{}, nil
		}
		if _, e := r.Ask(nil, nil); e == nil {
			t.Fatal("missing blocked error")
		}
	}
}
