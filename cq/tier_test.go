package cq

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func tierFixture(ref string, p, where float64, contextLimited bool) Record {
	r := reviewFixture(ref, "EH-01")
	r.Battery = "code"
	for i := range r.Findings {
		if r.Findings[i].ID == "EH-01" {
			r.Findings[i].P = p
			if where > 0 {
				r.Findings[i].Where = "lines 3-9: func run()"
				r.Findings[i].WhereConfidence = where
			}
		}
	}
	if contextLimited {
		r.ContextLimited = []string{"EH-01"}
	}
	return r
}

func intentRecord(r Record, p float64) Record {
	k := IntentKey("EH-01", "lines 3-9: func run()")
	return Record{Version: RecordVersion, ID: r.ID + "-intent", Ref: r.Ref, SourceDigest: r.SourceDigest, Chunk: r.Chunk,
		Lines: r.Lines, Language: r.Language, Battery: IntentBattery, Expected: []string{k}, Answers: map[string]float64{k: p}}
}

func guardRecord(r Record, p float64) Record {
	d := Record{Version: RecordVersion, ID: r.ID + "-docs", Ref: r.Ref, SourceDigest: r.SourceDigest, Chunk: r.Chunk, Lines: r.Lines,
		Language: r.Language, Battery: "docs", ThresholdPolicy: ThresholdPolicy, Catalogue: CatalogueDigest(), Model: "fixture-only",
		Expected: []string{"GC-02"}, Answers: map[string]float64{"GC-02": p}}
	if p >= PTrue {
		d.Findings = []Finding{{ID: "GC-02", P: p, Severity: "medium"}}
	}
	return d
}

func tierOf(t *testing.T, records ...Record) string {
	t.Helper()
	rep := BuildReview(records, nil, false, true, "")
	if len(rep.Files) != 1 {
		t.Fatalf("want one file, got %d", len(rep.Files))
	}
	return rep.Files[0].Tier
}

func TestTierBoundaries(t *testing.T) {
	cases := []struct {
		name string
		rec  Record
		want string
	}{
		{"p at the bound", tierFixture("a.go", 0.80, 0.60, false), TierPinned},
		{"p under the bound", tierFixture("a.go", 0.79, 0.60, false), TierUnpinned},
		{"where at the bound", tierFixture("a.go", 0.95, 0.60, false), TierPinned},
		{"where under the bound", tierFixture("a.go", 0.95, 0.59, false), TierUnpinned},
		{"context limited", tierFixture("a.go", 0.95, 0.90, true), TierUnpinned},
		{"unlocated", tierFixture("a.go", 0.95, 0, false), TierUnpinned},
	}
	for _, c := range cases {
		if got := tierOf(t, c.rec); got != c.want {
			t.Errorf("%s: tier %q, want %q", c.name, got, c.want)
		}
	}
	pinned := tierFixture("a.go", 0.95, 0.90, false)
	clear := guardRecord(pinned, 0.05)
	if got := tierOf(t, pinned, clear, intentRecord(pinned, 0.9)); got != TierIntentStated {
		t.Errorf("stated intent, guard clear: tier %q, want %q", got, TierIntentStated)
	}
	if got := tierOf(t, pinned, clear, intentRecord(pinned, 0.3)); got != TierPinned {
		t.Errorf("no stated intent: tier %q, want %q", got, TierPinned)
	}

	failed := guardRecord(pinned, 0.05)
	failed.Answers = map[string]float64{}
	failed.ReasonCodes = []string{"battery_failed"}
	for name, guard := range map[string][]Record{"guard missing": nil, "guard failed": {failed},
		"guard abstained": {guardRecord(pinned, 0.5)}, "guard fired": {guardRecord(pinned, 0.9)}} {
		records := append([]Record{pinned, intentRecord(pinned, 0.9)}, guard...)
		if got := tierOf(t, records...); got != TierPinned {
			t.Errorf("%s: tier %q, want %q", name, got, TierPinned)
		}
	}

	rep := BuildReview([]Record{pinned, clear, intentRecord(pinned, 0.9)}, nil, false, true, "")
	e := rep.Files[0].Findings[0]
	if len(rep.Files[0].Findings) == 0 || !e.IntentStated || e.IntentStatedP == nil || e.IntentGuard != GuardClear {
		t.Errorf("intent removed or unmarked the finding: %+v", rep.Files[0].Findings)
	}
}

func TestIntentOnlyFileLeavesTheDefaultPage(t *testing.T) {
	stated := tierFixture("stated.go", 0.95, 0.90, false)
	plain := tierFixture("plain.go", 0.95, 0.90, false)
	records := []Record{stated, guardRecord(stated, 0.05), intentRecord(stated, 0.95), plain}
	refs := func(tier string) []string {
		rep := BuildReview(records, nil, false, true, "correctness")
		page, err := PageReview(rep, ReviewQuery{Shape: "review_queue", Lane: "correctness", Tier: tier, Generation: "g", PageBytes: 200000, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var rows []FileReview
		if err := json.Unmarshal(page.Entries, &rows); err != nil {
			t.Fatal(err)
		}
		if page.TierCounts[TierPinned] != 1 || page.TierCounts[TierIntentStated] != 1 || page.TierCounts[TierUnpinned] != 0 {
			t.Fatalf("tier %q: tier counts %v", tier, page.TierCounts)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.Ref)
		}
		return out
	}
	if got := refs(""); len(got) != 1 || got[0] != "plain.go" {
		t.Errorf("default page: %v", got)
	}
	if got := refs(TierIntentStated); len(got) != 1 || got[0] != "stated.go" {
		t.Errorf("intent_stated page: %v", got)
	}
	if got := refs(TierAll); len(got) != 2 || got[0] != "plain.go" {
		t.Errorf("all: %v, want pinned before intent_stated", got)
	}
}

func TestTriagePageIsSmall(t *testing.T) {
	var records []Record
	for i := 0; i < 50; i++ {
		ref := fmt.Sprintf("internal/some/deeply/nested/package/path/with/a/long/name/file_%03d_%s.go", i, strings.Repeat("x", 20))
		if len(ref) >= 120 {
			t.Fatalf("fixture ref too long: %d", len(ref))
		}
		r := tierFixture(ref, 0.95, 0.90, false)
		r.ID = ref
		records = append(records, r)
	}
	rep := BuildReview(records, nil, false, true, "correctness")
	page, err := PageReview(rep, ReviewQuery{Shape: "triage", Generation: "g", PageBytes: 200000, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(page.Entries, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 50 {
		t.Fatalf("%d rows, want 50", len(rows))
	}
	for _, r := range rows {
		if len(r) >= 500 {
			t.Errorf("row of %d bytes: %s", len(r), r)
		}
	}
	whole, _ := json.Marshal(page)
	if len(whole) > 32<<10 {
		t.Errorf("triage page of 50 files is %d bytes, over 32 KiB", len(whole))
	}
	var row TriageRow
	if err := json.Unmarshal(rows[0], &row); err != nil || row.Top == nil || row.Top.ID != "EH-01" || row.Top.Fault == "" || row.Tier != TierPinned {
		t.Errorf("first row: %+v (%v)", row, err)
	}
}

func TestPathNamesOneFile(t *testing.T) {
	a, b := tierFixture("pkg/a.go", 0.95, 0.90, false), tierFixture("pkg/b.go", 0.95, 0.90, false)
	b.ID = "b"
	rep := BuildReview([]Record{a, b}, nil, false, true, "")
	page, err := PageReview(rep, ReviewQuery{Shape: "review_queue", Path: "pkg/b.go", Generation: "g", PageBytes: 200000, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var rows []FileReview
	if err := json.Unmarshal(page.Entries, &rows); err != nil || len(rows) != 1 || rows[0].Ref != "pkg/b.go" || page.Total != 1 {
		t.Fatalf("path pkg/b.go: %d rows, total %d (%v)", len(rows), page.Total, err)
	}
}

func TestTierNeedsTheCorrectnessLane(t *testing.T) {
	rep := BuildReview(nil, nil, false, true, "maintainability")
	for _, q := range []ReviewQuery{
		{Shape: "coverage", Tier: TierPinned},
		{Shape: "review_queue", Lane: "maintainability", Tier: TierPinned},
		{Shape: "review_queue", Lane: "correctness", Tier: "best"},
	} {
		q.Generation, q.PageBytes, q.Limit = "g", 200000, 10
		if _, err := PageReview(rep, q); err == nil {
			t.Errorf("%+v accepted", q)
		}
	}
}

func TestFindingsAssessmentAgreesWithReview(t *testing.T) {
	r := tierFixture("agree.go", 0.95, 0.90, false)
	rep := BuildReview([]Record{r}, []Coverage{completeFixture(r)}, true, true, "")
	want := rep.Files[0].Answered
	if want == 0 {
		t.Fatal("fixture has no answers")
	}
	page, _ := Page([]Record{r}, 0, 200000)
	fold := NewFindingsFold("", "low", "", 0, 200000)
	fold.Add(r)
	for name, got := range map[string][]Record{"Page": page, "FindingsFold": fold.Page} {
		if len(got) != 1 {
			t.Fatalf("%s: %d records", name, len(got))
		}
		if a := AssessmentOf(got[0]); a.Answered != want || a.Status == "unknown" {
			t.Errorf("%s: answered %d, status %s; the review says %d", name, a.Answered, a.Status, want)
		}
	}
}

func TestSummaryListsThePinnedFiles(t *testing.T) {
	strong, weak, loose := tierFixture("strong.go", 0.95, 0.90, false), tierFixture("weak.go", 0.85, 0.90, false), tierFixture("loose.go", 0.95, 0, false)
	weak.ID, loose.ID = "weak", "loose"
	records := []Record{weak, loose, strong}
	fold := NewSummaryFold(10)
	for _, r := range records {
		fold.Add(r)
		fold.AddRun([]Record{r})
	}
	for name, out := range map[string]map[string]any{"fold": fold.Result(), "legacy": Summary(records, 10)} {
		rows, _ := out["top_correctness"].([]any)
		if len(rows) != 2 || rows[0].(map[string]any)["ref"] != "strong.go" || rows[1].(map[string]any)["ref"] != "weak.go" || out["worst_basis"] != worstBasis {
			t.Errorf("%s: top_correctness %v, worst_basis %v", name, out["top_correctness"], out["worst_basis"])
		}
		if _, err := sdk.ResultSize(out); err != nil {
			t.Errorf("%s: the summary is no result: %v", name, err)
		}
	}
}

func TestIntentOutcomesAreVisibleAndAddUp(t *testing.T) {
	saved := tierFixture("saved.go", 0.95, 0.90, false)
	failed := tierFixture("failed.go", 0.95, 0.90, false)
	failed.ID = "failed"
	notAsked := tierFixture("not-asked.go", 0.95, 0.90, false)
	notAsked.ID = "not-asked"
	unreadable := tierFixture("unreadable.go", 0.95, 0.90, false)
	unreadable.ID = "unreadable"
	f := intentRecord(failed, 0.9)
	f.Answers = map[string]float64{}
	f.ReasonCodes = []string{IntentFailed, "answered_in_part"}
	runs := [][]Record{{saved, intentRecord(saved, 0.2)}, {failed, f}, {notAsked}, {unreadable, IntentUnreadable("unreadable.go")}}
	want := map[string]struct {
		none   string
		counts IntentCounts
	}{
		"saved.go": {"", IntentCounts{Asked: 1, Saved: 1}}, "failed.go": {IntentNoneFailed, IntentCounts{Asked: 1, Failed: 1}},
		"not-asked.go": {IntentNoneNotAsked, IntentCounts{}}, "unreadable.go": {IntentNoneUnreadable, IntentCounts{}},
	}
	fold := NewSummaryFold(10)
	var sum IntentCounts
	for _, run := range runs {
		fold.AddRun(run)
		rep := BuildReview(run, nil, false, true, "correctness")
		got := rep.Files[0]
		w := want[got.Ref]
		e := got.Findings[0]
		if e.IntentNone != w.none || (w.none == "") != (e.IntentStatedP != nil) || got.Intent == nil || *got.Intent != w.counts {
			t.Errorf("%s: none %q, p %v, counts %+v", got.Ref, e.IntentNone, e.IntentStatedP, got.Intent)
		}
		if row := triageRow(got, "correctness"); row.Intent == nil || *row.Intent != w.counts {
			t.Errorf("%s: triage row intent %+v", got.Ref, row.Intent)
		}
		sum.Add(*got.Intent)
	}
	if got := fold.Result()["intent"]; !reflect.DeepEqual(got, sum.result()) || sum != (IntentCounts{Asked: 2, Saved: 1, Failed: 1}) {
		t.Fatalf("summary totals %v, sum over files %+v", got, sum)
	}
}
