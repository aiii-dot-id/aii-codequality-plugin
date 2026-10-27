package cq

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func reviewFixture(ref string, positive ...string) Record {
	r := Record{Version: RecordVersion, ID: ref + "-0-code", Ref: ref, SourceDigest: "frozen-source", ThresholdPolicy: ThresholdPolicy, Preprocessing: "preview-v2", Language: "go", Catalogue: CatalogueDigest(), Model: "fixture-only", Lines: [2]int{1, 20}, Answers: map[string]float64{}}
	for _, it := range CodeItems("go") {
		r.Expected = append(r.Expected, it.ID)
		r.Answers[it.ID] = 0.01
	}
	for _, id := range positive {
		r.Answers[id] = 0.99
	}
	r.Findings, r.Abstained, r.Dimensions, r.Index, r.Uncertain = Score("go", r.Answers, false)
	r.Band = Band(r.Index)
	return r
}
func completeFixture(r Record) Coverage {
	return Coverage{Ref: r.Ref, SourceDigest: r.SourceDigest, Kind: "file", Disposition: "complete", Reasons: []string{}, ExpectedChunks: 1, CompletedChunks: 1}
}

func TestAssessmentAllAbstain(t *testing.T) {
	r := reviewFixture("unknown.go")
	for id := range r.Answers {
		r.Answers[id] = 0.5
	}
	a := AssessmentOf(r)
	if a.Status != "unknown" || a.Abstained != len(r.Expected) || a.Missing != 0 {
		t.Fatalf("abstentions portrayed as assessed: %+v", a)
	}
	report := BuildReview([]Record{r}, []Coverage{completeFixture(r)}, true, true, "")
	if report.AssessmentComplete || len(report.Gaps) != 1 || report.Gaps[0].Disposition != "unknown" {
		t.Fatalf("unknown file hidden: %+v", report)
	}
}

func TestThresholdPolicyBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name               string
		p                  float64
		status             string
		abstained, missing int
	}{
		{"below", math.Nextafter(.4, 0), "assessed", 0, 0}, {"at_low", .4, "unknown", 1, 0},
		{"below_high", math.Nextafter(.6, 0), "unknown", 1, 0}, {"at_high", .6, "assessed", 0, 0},
		{"nan", math.NaN(), "unknown", 0, 1}, {"infinity", math.Inf(1), "unknown", 0, 1}, {"outside", 1.1, "unknown", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Record{Version: RecordVersion, ThresholdPolicy: ThresholdPolicy, Expected: []string{"EH-01"}, Answers: map[string]float64{"EH-01": tc.p}}
			a := AssessmentOf(r)
			if a.Status != tc.status || a.Abstained != tc.abstained || a.Missing != tc.missing {
				t.Fatalf("%+v", a)
			}
		})
	}
	r := reviewFixture("missing.go")
	delete(r.Answers, "EH-01")
	if a := AssessmentOf(r); a.Status != "partial" || a.Missing != 1 {
		t.Fatalf("missing answer: %+v", a)
	}
	r.ThresholdPolicy = "future-policy"
	if a := AssessmentOf(r); a.Status != "unknown" {
		t.Fatalf("reinterpreted historical threshold: %+v", a)
	}
}

func TestSeriousFindingNotAveragedAway(t *testing.T) {
	serious, style := reviewFixture("z-error.go", "EH-01"), reviewFixture("a-style.go", "RD-01", "RD-02", "RD-04", "ST-01", "ST-02")
	if serious.Index != 95 || serious.Band != "clean" {
		t.Fatalf("legacy counterexample changed: %+v", serious)
	}
	report := BuildReview([]Record{style, serious}, []Coverage{completeFixture(style), completeFixture(serious)}, true, true, "")
	if len(report.Files) != 2 || report.Files[0].Ref != serious.Ref || !strings.Contains(report.Files[0].SelectionReason, "high") {
		t.Fatalf("averaged away: %+v", report.Files)
	}
	if !report.AssessmentComplete {
		t.Fatal("complete fixture was not assessed")
	}
}

func TestReviewQueueDistinctFiles(t *testing.T) {
	a, b := reviewFixture("a.go", "EH-01"), reviewFixture("b.go", "EH-02")
	a2 := a
	a2.ID = "second-window"
	a2.Chunk = 1
	a2.Lines = [2]int{21, 40}
	ca := completeFixture(a)
	ca.ExpectedChunks = 2
	ca.CompletedChunks = 2
	report := BuildReview([]Record{a, a2, a, b}, []Coverage{ca, completeFixture(b)}, true, true, "")
	if len(report.Files) != 2 {
		t.Fatalf("chunks consumed file positions: %d", len(report.Files))
	}
	f := report.Files[0]
	if f.Applicable != 2*len(a.Expected) || len(f.Findings) != 2 {
		t.Fatalf("replay counted twice: %+v", f)
	}
	if _, n := lanePriority(f, "correctness"); n != 1 {
		t.Fatalf("locations inflated category breadth: %d", n)
	}
	changed := a
	changed.SourceDigest = "changed-source"
	report = BuildReview([]Record{a, changed}, nil, false, true, "")
	if len(report.Files) != 2 {
		t.Fatal("different source digests merged")
	}
}

func TestReviewCandidateCounterexample(t *testing.T) {
	weak := reviewFixture("many-leads.go", "EH-04", "DF-03", "DF-04")
	strong := reviewFixture("one-error.go", "EH-01")
	report := BuildReview([]Record{strong, weak}, nil, false, true, "")
	if len(report.Files) != 2 || report.Files[0].Ref != weak.Ref {
		t.Fatal("candidate policy changed without versioned decision")
	}
	if !strings.Contains(report.Caveat, "unvalidated") {
		t.Fatal("heuristic advertised as proven")
	}
	for _, e := range report.Files[0].Findings {
		if !e.Lead {
			t.Fatal("historical weak lead hidden")
		}
	}
}

func TestReviewLanes(t *testing.T) {
	for _, tc := range []struct{ id, severity, want string }{
		{"EH-01", "high", "correctness"}, {"DF-01", "medium", "correctness"}, {"ST-03", "high", "correctness"},
		{"ID-GO-04", "high", "correctness"}, {"ID-JS-01", "high", "correctness"}, {"ID-GO-01", "low", "maintainability"},
		{"GC-02", "medium", "maintainability"}, {"ST-04", "medium", "maintainability"}, {"DC-03", "medium", "maintainability"},
	} {
		if got := findingLane(Finding{ID: tc.id, Severity: tc.severity}); got != tc.want {
			t.Errorf("%s: %s", tc.id, got)
		}
	}
	mixed := reviewFixture("mixed.go", "EH-01", "RD-01")
	for _, lane := range []string{"", "correctness", "maintainability"} {
		r := BuildReview([]Record{mixed}, nil, false, true, lane)
		if len(r.Files) != 1 || len(r.Files[0].Lanes) != 2 {
			t.Fatalf("mixed file hidden for %q", lane)
		}
	}
}

func TestLegacyReviewCoverageUnknown(t *testing.T) {
	r := reviewFixture("legacy.go", "EH-01")
	r.Version = 0
	r.Expected = nil
	report := BuildReview([]Record{r}, []Coverage{completeFixture(r)}, true, true, "")
	if len(report.Files) != 1 || report.Files[0].Status != "unknown" || !contains(report.Files[0].Reasons, "legacy_coverage_unknown") || report.AssessmentComplete {
		t.Fatalf("legacy completeness invented: %+v", report)
	}

	raw := `{"ref":"old.go","chunk":0,"index":95,"band":"clean","findings":[{"id":"EH-01","p":0.99,"severity":"high"}]}`
	records, err := ParseRecords([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := BuildReview(records, nil, false, true, ""); len(got.Files) != 1 || got.Files[0].Status != "unknown" {
		t.Fatal("old findings lost")
	}
}

func TestReviewMissingContextAndReportPrefix(t *testing.T) {
	r := reviewFixture("ownership.go", "EH-04")
	r.ContextLimited = []string{"EH-04"}
	report := BuildReview([]Record{r}, []Coverage{completeFixture(r)}, true, false, "")
	if report.ReportComplete || report.AssessmentComplete || report.Files[0].Status != "partial" || !report.Files[0].Findings[0].ContextLimited {
		t.Fatalf("context or prefix falsely complete: %+v", report)
	}
}

func TestReviewPreview(t *testing.T) {
	rs := []Record{reviewFixture("ignored-error.go", "EH-01"), reviewFixture("mixed.go", "EH-02", "RD-01"), reviewFixture("many-leads.go", "EH-04", "DF-03", "DF-04"), reviewFixture("uncertain.go")}
	for id := range rs[3].Answers {
		rs[3].Answers[id] = .5
	}
	coverage := []Coverage{}
	for _, r := range rs {
		coverage = append(coverage, completeFixture(r))
	}
	coverage = append(coverage, Coverage{Ref: "oversized.go", Kind: "file", Disposition: "unknown", Reasons: []string{"oversize"}, Size: 2 << 20}, Coverage{Ref: "vendor", Kind: "subtree", Disposition: "excluded", Reasons: []string{"excluded_subtree_children_not_enumerated"}})
	report := BuildReview(rs, coverage, false, true, "")
	if len(report.Files) != 3 || len(report.Gaps) != 3 {
		t.Fatalf("preview dropped evidence: %+v", report)
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("SYNTHETIC PREVIEW — not a repository scan or a model-quality result:\n" + string(b))
}
