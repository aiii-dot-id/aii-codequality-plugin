package main

import (
	"encoding/json"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
)

func TestPageRecordsCarryTheirWholeAssessment(t *testing.T) {
	r := cq.Record{Version: cq.RecordVersion, ID: "x", Ref: "x.go", ThresholdPolicy: cq.ThresholdPolicy, Language: "go",
		Answers: map[string]float64{}, Expected: []string{}}
	for _, it := range cq.CodeItems("go") {
		r.Expected = append(r.Expected, it.ID)
		r.Answers[it.ID] = 0.01
	}
	raw, err := pageRecordsJSON([]cq.Record{r})
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != 1 {
		t.Fatalf("decode: %v", err)
	}
	if a, ok := got[0]["answers"]; ok && string(a) != "null" {
		t.Errorf("the page shows the answers: %s", a)
	}
	var a cq.Assessment
	if err := json.Unmarshal(got[0]["assessment"], &a); err != nil {
		t.Fatal(err)
	}
	if a.Answered != len(r.Expected) || a.Missing != 0 || a.Status != "assessed" {
		t.Errorf("assessment %+v, want all %d answered", a, len(r.Expected))
	}
}
