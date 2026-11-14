package cq

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnEarlierSizeSkipReadsAsNotAssessedByAnEarlierVersion(t *testing.T) {
	old := Coverage{Ref: "big.go", Kind: "file", Disposition: "unknown", Reasons: []string{"oversize"}, Size: 2 << 20}
	rep := BuildReview(nil, []Coverage{old}, true, true, "")
	b, _ := json.Marshal(rep)
	if len(rep.Gaps) != 1 || !contains(rep.Gaps[0].Reasons, NotAssessedEarlier) || strings.Contains(string(b), "oversize") || rep.AssessmentComplete {
		t.Fatalf("%s", b)
	}
	page, err := PageReview(rep, ReviewQuery{Shape: "triage", Generation: "g", PageBytes: 200000, Limit: 50, EarlierVersion: "0.2.0 or earlier"})
	if err != nil || page.EarlierCount == nil || *page.EarlierCount != 1 || len(page.EarlierFiles) != 1 || page.EarlierVersion != "0.2.0 or earlier" {
		t.Fatalf("%+v %v", page, err)
	}
	fold := NewSummaryFold(10)
	fold.AddRun(nil, old)
	if fold.Result()[NotAssessedEarlier] != 1 {
		t.Fatalf("summary: %v", fold.Result()[NotAssessedEarlier])
	}
}

func tableForCoverage(t *testing.T) *Table {
	t.Helper()
	v, e := LoadTable()
	if e != nil {
		t.Fatal(e)
	}
	return v
}
