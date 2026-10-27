package cq

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type progressJudge struct {
	calls        int
	remaining    int
	docsFail     bool
	locationFail bool
	observed     []map[string]any
}

func (j *progressJudge) Model() string { return "fixture-pinned" }
func (j *progressJudge) Ask(s map[string]any, q map[string]any) (map[string]float64, error) {
	j.calls++
	j.observed = append(j.observed, s)
	if j.remaining == 0 {
		return nil, fmt.Errorf("%w: fixture budget", ErrJudge)
	}
	j.remaining--
	if j.docsFail && s["source"] != nil {
		return nil, fmt.Errorf("%w: fixture docs", ErrJudge)
	}
	a := map[string]float64{}
	for id := range q {
		a[id] = 0.123456789
	}
	if _, ok := q["EH-01"]; ok {
		a["EH-01"] = 0.987654321
	}
	return a, nil
}
func (j *progressJudge) Choose(s map[string]any, q map[string]any) (map[string]Choice, error) {
	if j.locationFail {
		return nil, ErrJudge
	}
	out := map[string]Choice{}
	for id := range q {
		out[id] = Choice{Choice: "cannot_localize", Confidence: 0.99}
	}
	return out, nil
}

const progressSource = "package p\n\n// f processes work; sample contract only.\nfunc f() {\n println(\"work-item-long-enough-for-code-assessment\")\n}\n\nfunc g() { println(\"second-unit-for-localization\") }\n"

func TestPartialFindingsSurvive(t *testing.T) {
	table, err := LoadTable()
	if err != nil {
		t.Fatal(err)
	}
	j := &progressJudge{remaining: 20, docsFail: true}
	var durable []Record
	save := func(r Record) error {
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		var copy Record
		if e = json.Unmarshal(b, &copy); e != nil {
			return e
		}
		durable = DeduplicateRecords(append(durable, copy))
		return nil
	}
	recs, err := JudgeTextProgress(table, j, nil, "f.go", "go", progressSource, ChunkBytes, nil, save)
	if !errors.Is(err, ErrJudge) || len(recs) != 2 || len(durable) != 2 {
		t.Fatalf("records %d durable %d err %v", len(recs), len(durable), err)
	}
	if recs[0].Answers["EH-01"] != 0.987654321 || len(recs[0].Findings) != 1 {
		t.Fatalf("lost raw finding: %+v", recs[0])
	}
	if AssessmentOf(recs[1]).Status != "unknown" {
		t.Fatal("failed docs assessed")
	}
	j = &progressJudge{remaining: 20, locationFail: true}
	recs, err = JudgeTextProgress(table, j, nil, "f.go", "go", progressSource, ChunkBytes, nil, nil)
	if err != nil || len(recs[0].Findings) != 1 || recs[0].Findings[0].LocationReason != "localization_failed" {
		t.Fatalf("optional failure erased evidence: %v %+v", err, recs)
	}
	if AssessmentOf(recs[0]).Missing != 0 {
		t.Fatal("localization changed checklist coverage")
	}
}

func TestProgressIndependentOfCache(t *testing.T) {
	table, _ := LoadTable()
	var durable []Record
	save := func(r Record) error {
		b, _ := json.Marshal(r)
		var copy Record
		if err := json.Unmarshal(b, &copy); err != nil {
			return err
		}
		durable = DeduplicateRecords(append(durable, copy))
		return nil
	}
	j := &progressJudge{remaining: 1}
	_, err := JudgeTextProgress(table, j, nil, "f.go", "go", progressSource, ChunkBytes, nil, save)
	if !errors.Is(err, ErrJudge) {
		t.Fatalf("need interrupted first step: %v", err)
	}
	j = &progressJudge{remaining: 1}
	recs, err := JudgeTextProgress(table, j, nil, "f.go", "go", progressSource, ChunkBytes, durable, save)
	if err != nil || j.calls != 1 || len(recs) != 2 || !batteryAnswered(recs[0]) || !batteryAnswered(recs[1]) {
		t.Fatalf("no-cache resume: %v calls %d records %+v", err, j.calls, recs)
	}
	if len(durable) != 2 {
		t.Fatalf("replay doubled progress: %d", len(durable))
	}
	j = &progressJudge{remaining: 20}
	changed, err := JudgeTextProgress(table, j, nil, "f.go", "go", strings.ReplaceAll(progressSource, "work-item", "changed-item"), ChunkBytes, durable, nil)
	if err != nil || j.calls != 2 || changed[0].SourceDigest == recs[0].SourceDigest {
		t.Fatalf("changed source reused progress: %v calls %d", err, j.calls)
	}
}

func TestProgressWriteFailureStopsBeforeNextBattery(t *testing.T) {
	table, _ := LoadTable()
	j := &progressJudge{remaining: 20}
	recs, err := JudgeTextProgress(table, j, nil, "f.go", "go", progressSource, ChunkBytes, nil, func(Record) error { return errors.New("disk full") })
	if !errors.Is(err, ErrProgress) || j.calls != 1 || len(recs) != 0 {
		t.Fatalf("advanced after write failure: %v calls %d recs %d", err, j.calls, len(recs))
	}
}

func TestContextualDocsCacheIdentity(t *testing.T) {
	table, _ := LoadTable()
	cache := MemCache{}
	j := &progressJudge{remaining: 30}
	first, err := JudgeTextChunked(table, j, cache, "f.go", "go", progressSource, ChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range j.observed {
		if src, ok := s["source"].(string); ok {
			found = strings.Contains(src, "sample contract") && strings.Contains(src, "func f")
		}
	}
	if !found {
		t.Fatal("docs did not receive both comment and code")
	}
	before := j.calls
	_, err = JudgeTextChunked(table, j, cache, "f.go", "go", progressSource, ChunkBytes)
	if err != nil || j.calls != before {
		t.Fatalf("stable cache miss %v", err)
	}
	_, err = JudgeTextChunked(table, j, cache, "f.go", "go", strings.ReplaceAll(progressSource, "work-item", "different-item"), ChunkBytes)
	if err != nil || j.calls != before+2 {
		t.Fatalf("changed adjacent code reused docs: %v calls %d", err, j.calls)
	}
	if first[0].Preprocessing != PreprocessingVersion {
		t.Fatal("missing preprocessing provenance")
	}
}
