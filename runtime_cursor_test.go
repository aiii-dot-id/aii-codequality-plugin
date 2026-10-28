package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

type oneFileFS struct{}

func (oneFileFS) List(string) ([]cq.Entry, bool, error) {
	return []cq.Entry{{Name: "x.go", Size: int64(len(goSource))}}, false, nil
}
func (oneFileFS) Read(string, int64) ([]byte, error) { return []byte(goSource), nil }

func TestRuntimeCheckpointKeepsCommittedCursor(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	s, e := cq.NewScan(oneFileFS{}, "cursor", ".", "", nil, nil, false, 1, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	binding, e := scanBinding(cfg)
	if e != nil {
		t.Fatal(e)
	}
	s.Runtime, _ = json.Marshal(binding)
	j, e := newJudge(cfg, 12)
	if e != nil {
		t.Fatal(e)
	}
	var first []byte
	j.writeRuntimeCheckpoint = func(b []byte) error {
		if first == nil {
			first = append([]byte(nil), b...)
		}
		return nil
	}
	if e = j.bindScan(cfg, s, 1000); e != nil {
		t.Fatal(e)
	}
	j.send = (&capture{model: "fixed"}).send
	if _, e = s.Step(tableT(t), oneFileFS{}, j, cq.MemCache{}, 12, 10); e != nil {
		t.Fatal(e)
	}
	var restored cq.Scan
	if e = json.Unmarshal(first, &restored); e != nil {
		t.Fatal(e)
	}
	if restored.FilesSeen != 0 || restored.Status != "running" {
		t.Fatalf("unfinished counter acknowledged: %+v", restored)
	}
	r, e := newJudge(cfg, 12)
	if e != nil {
		t.Fatal(e)
	}
	r.writeRuntimeCheckpoint = func([]byte) error { return nil }
	if e = r.bindScan(cfg, &restored, 2000); e != nil {
		t.Fatal(e)
	}
	r.send = (&capture{model: "fixed"}).send
	records, e := restored.Step(tableT(t), oneFileFS{}, r, cq.MemCache{}, 12, 10)
	if e != nil || restored.Status != "done" || restored.FilesJudged != 1 || len(records) != 2 {
		t.Fatalf("restart falsely exhausted budget: %s records=%d judged=%d err=%v", restored.Status, len(records), restored.FilesJudged, e)
	}
}

func TestRuntimeCheckpointOverflowCannotMutateLiveTraversal(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	binding, e := scanBinding(cfg)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(binding)
	s := &cq.Scan{Runtime: raw, Status: "running", Stack: []string{"child"}, Pending: []cq.Entry{{Name: strings.Repeat("x", 260<<10)}}}
	j, e := newJudge(cfg, 12)
	if e != nil {
		t.Fatal(e)
	}
	j.writeRuntimeCheckpoint = func(b []byte) error {
		if len(b) > 250<<10 {
			return cq.ErrCheckpointFull
		}
		return nil
	}
	if e = j.bindScan(cfg, s, 1000); e != nil {
		t.Fatal(e)
	}
	j.send = (&capture{model: "fixed"}).send
	if _, e = j.Ask(nil, map[string]any{"q": map[string]any{"type": "noul"}}); !errors.Is(e, cq.ErrCheckpointFull) {
		t.Fatalf("overflow not propagated: %v", e)
	}
	if s.Status != "running" || len(s.Stack) != 1 || len(s.Pending) != 1 {
		t.Fatal("runtime save mutated active walk")
	}
}

func TestOptionalLocalizationRefusalDoesNotBlockChecklist(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	for _, status := range []int{400, 413, 422} {
		j, _, _ := scanJudgeT(t, cfg, nil, 1000)
		j.send = func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) { return sdk.HTTPResult{Status: status}, nil }
		if _, e := j.Choose(nil, nil); e == nil {
			t.Fatal("missing optional refusal")
		}
		if j.runtime.Blocked != "" {
			t.Fatalf("optional refusal blocked checklist: %s", j.runtime.Blocked)
		}
		j.send = (&capture{model: "fixed"}).send
		if _, e := j.Ask(nil, map[string]any{"q": map[string]any{"type": "noul"}}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestDirectInvocationFreshAliasAndVisibleAssessment(t *testing.T) {
	cfg := config{backend: backendJev, model: "jev-latest", handle: "test", ints: defaultInts()}
	j, e := newJudge(cfg, 12)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.bindInvocation(cfg, 1000); e != nil {
		t.Fatal(e)
	}
	if j.CacheEligible() || j.Model() != cfg.model {
		t.Fatal("direct judge trusts an unresolved alias")
	}
	j.send = (&capture{model: "jev-1.14.0"}).send
	records, e := cq.JudgeText(tableT(t), j, cq.MemCache{}, "x.go", "go", goSource)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := recordsJSON(records)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), `"assessment":`) {
		t.Fatal("direct output lacks assessment")
	}
}
