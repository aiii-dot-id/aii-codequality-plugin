package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func journalOf(t *testing.T, events []JournalEvent) *Journal {
	t.Helper()
	s := newMemStore()
	var b []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b = append(append(b, line...), '\n')
	}
	s.files["j"] = b
	return &Journal{Store: s, Path: "j"}
}

func oldReview(t *testing.T, events []JournalEvent, done bool, lane string) ReviewReport {
	t.Helper()
	var recs []Record
	var cov []Coverage
	for _, e := range events {
		switch {
		case frontierRow(e.Coverage):
		case e.Record != nil:
			recs = append(recs, *e.Record)
		default:
			cov = append(cov, *e.Coverage)
		}
	}
	return BuildReview(recs, cov, done, true, lane)
}

func allRecords(events []JournalEvent) []Record {
	var recs []Record
	for _, e := range events {
		if e.Record != nil {
			recs = append(recs, *e.Record)
		}
	}
	return DeduplicateRecords(recs)
}

func pagesOf(t *testing.T, page func(cursor string) (ReviewPage, error)) ([]json.RawMessage, ReviewPage) {
	t.Helper()
	var all []json.RawMessage
	var first ReviewPage
	cursor := ""
	for i := 0; ; i++ {
		p, err := page(cursor)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = p
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(p.Entries, &rows); err != nil {
			t.Fatal(err)
		}
		all = append(all, rows...)
		if p.Next == "" {
			return all, first
		}
		if i > 10000 {
			t.Fatal("pages never end")
		}
		cursor = p.Next
	}
}

func requireSameReports(t *testing.T, events []JournalEvent, done bool, limits ...int) {
	t.Helper()
	j := journalOf(t, events)
	type variant struct{ shape, lane, tier, path string }
	var variants []variant
	for _, shape := range []string{"review_queue", "coverage", "triage"} {
		for _, lane := range []string{"", "correctness", "maintainability"} {
			if shape == "triage" && lane == "" {
				continue
			}
			tiers := []string{""}
			if shape != "coverage" && lane == "correctness" {
				tiers = []string{"", TierPinned, TierUnpinned, TierIntentStated, TierAll}
			}
			for _, tier := range tiers {
				for _, p := range []string{"", "a*", "missing/**"} {
					variants = append(variants, variant{shape, lane, tier, p})
				}
			}
		}
	}
	for _, v := range variants {
		for _, limit := range limits {
			lane := v.lane
			if v.shape == "triage" && lane == "" {
				lane = "correctness"
			}
			want := oldReview(t, events, done, lane)
			old, oldFirst := pagesOf(t, func(c string) (ReviewPage, error) {
				return PageReview(want, ReviewQuery{Shape: v.shape, Lane: v.lane, Tier: v.tier, Path: v.path, Cursor: c, Generation: "g", PageBytes: 200000, Limit: limit})
			})
			got, first := pagesOf(t, func(c string) (ReviewPage, error) {
				return ReviewFold(j, ReviewQuery{Shape: v.shape, Lane: v.lane, Tier: v.tier, Path: v.path, Cursor: c, Generation: "g", PageBytes: 200000, Limit: limit, ScanDone: done})
			})
			if len(old) != len(got) {
				t.Fatalf("%+v limit %d: %d entries, the old reading %d", v, limit, len(got), len(old))
			}
			for i := range old {
				if !bytes.Equal(old[i], got[i]) {
					t.Fatalf("%+v limit %d: entry %d differs:\n%s\n%s", v, limit, i, got[i], old[i])
				}
			}
			a, _ := json.Marshal([]any{first.TierCounts, first.EarlierFiles, first.EarlierCount, first.IntentTotals})
			b, _ := json.Marshal([]any{oldFirst.TierCounts, oldFirst.EarlierFiles, oldFirst.EarlierCount, oldFirst.IntentTotals})
			if first.Total != oldFirst.Total || first.GapCount != oldFirst.GapCount || !bytes.Equal(a, b) ||
				first.InventoryComplete != oldFirst.InventoryComplete || first.AssessmentComplete != oldFirst.AssessmentComplete || !first.ReportComplete {
				t.Fatalf("%+v: totals, counts or flags differ: %+v against %+v", v, first, oldFirst)
			}
		}
	}
	recs := allRecords(events)
	sum := NewSummaryFold(10)
	fold := func(add func(Record)) {
		if _, err := FoldJournal(j, func(run *FileRun) error {
			for _, r := range DeduplicateRecords(run.Records) {
				add(r)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := FoldJournal(j, func(run *FileRun) error {
		records := DeduplicateRecords(run.Records)
		for _, r := range records {
			sum.Add(r)
		}
		sum.AddRun(records, run.Coverage...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(Summary(recs, 10))
	b, _ := json.Marshal(sum.Result())
	if !bytes.Equal(a, b) {
		t.Fatalf("summary differs:\n%s\n%s", b, a)
	}
	for _, q := range []struct {
		glob, sev, lang string
		cursor, bytes   int
	}{{"", "low", "", 0, 4096}, {"", "high", "", 0, 200000}, {"pkg/**", "low", "go", 0, 4096}, {"", "low", "", 3, 8192}} {
		want, next := Page(Select(recs, q.glob, q.sev, q.lang), q.cursor, q.bytes)
		f := NewFindingsFold(q.glob, q.sev, q.lang, q.cursor, q.bytes)
		fold(f.Add)
		x, _ := json.Marshal(want)
		y, _ := json.Marshal(f.Page)
		if !bytes.Equal(x, y) || next != f.Next || f.Total != len(Select(recs, q.glob, q.sev, q.lang)) {
			t.Fatalf("findings %+v: page or cursor differs (%d/%d, next %d/%d)", q, len(f.Page), len(want), f.Next, next)
		}
	}
}

func synthetic(files int) []JournalEvent {
	ids := []string{"EH-01", "DF-03", "ST-03", "ST-01", "DC-01", "ID-GO-01", "ST-02", "EH-02"}
	sev := map[string]string{"EH-01": "high", "DF-03": "high", "ST-03": "high", "ST-01": "medium", "DC-01": "low", "ID-GO-01": "low", "ST-02": "medium", "EH-02": "high"}
	var ev []JournalEvent
	frontier := func(ref string) {
		ev = append(ev, JournalEvent{Coverage: &Coverage{Ref: ref, Kind: "file", Disposition: "pending", Reasons: []string{"step_budget"}}})
	}
	rec := func(ref, digest string, chunk, n int, findings ...string) Record {
		r := Record{Version: RecordVersion, ID: fmt.Sprintf("%s-%s-%d", ref, digest, chunk), SourceDigest: digest, Ref: ref, Chunk: chunk,
			Lines: [2]int{1 + chunk*100, 100 + chunk*100}, Language: "go", Index: 100 - 7*len(findings) - n%5, Band: "clean",
			ThresholdPolicy: ThresholdPolicy, Expected: []string{"EH-01", "DF-03"}, Answers: map[string]float64{"EH-01": 0.1, "DF-03": 0.1},
			Model: "jev", Backend: "local:x", Catalogue: "c"}
		for _, id := range findings {
			r.Findings = append(r.Findings, Finding{ID: id, P: 0.9, Severity: sev[id]})
			r.Answers[id] = 0.9
		}
		return r
	}
	for i := range files {
		ref := fmt.Sprintf("pkg/d%d/f%03d.go", i%4, i)
		digest := fmt.Sprintf("%064x", i)
		if i%9 == 0 {
			frontier(ref)
		}
		var fs []string
		for k := 0; k < i%4; k++ {
			fs = append(fs, ids[(i+k)%len(ids)])
		}
		r0 := rec(ref, digest, 0, i, fs...)
		ev = append(ev, JournalEvent{Record: &r0})
		if i%5 == 0 {
			frontier(fmt.Sprintf("pkg/zz%d.go", i))
			r1 := rec(ref, digest, 1, i, ids[i%len(ids)])
			ev = append(ev, JournalEvent{Record: &r1})
		}
		if i%7 == 0 {
			again := r0
			ev = append(ev, JournalEvent{Record: &again})
		}
		chunks := 1
		if i%5 == 0 {
			chunks = 2
		}
		switch {
		case i%11 == 0:
			ev = append(ev, JournalEvent{Coverage: &Coverage{Ref: ref, SourceDigest: digest, Kind: "file", Disposition: "pending", Reasons: []string{"assessment_interrupted"}, ExpectedChunks: chunks, CompletedChunks: 0}})
		case i%13 == 0:
		case i%17 == 0:
			ev = append(ev, JournalEvent{Coverage: &Coverage{Ref: ref, SourceDigest: digest, Kind: "file", Disposition: "stale", Reasons: []string{"source_changed", "fresh_assessment_required"}}})
		default:
			ev = append(ev, JournalEvent{Coverage: &Coverage{Ref: ref, SourceDigest: digest, Kind: "file", Disposition: "complete", Reasons: []string{}, ExpectedChunks: chunks, CompletedChunks: chunks}})
		}
		if i%10 == 3 {
			ev = append(ev, JournalEvent{Coverage: &Coverage{Ref: fmt.Sprintf("pkg/x%d_test.go", i), Kind: "file", Disposition: "excluded", Reasons: []string{"test"}}})
		}
		if i%25 == 0 {
			ev = append(ev, JournalEvent{Coverage: &Coverage{Ref: fmt.Sprintf("pkg/sub%d", i), Kind: "subtree", Disposition: "unknown", Reasons: []string{"rules_error", "children_not_enumerated"}}})
			ev = append(ev, JournalEvent{Coverage: &Coverage{Ref: fmt.Sprintf("pkg/sub%d", i+1), Kind: "subtree", Disposition: "excluded", Reasons: []string{"gitignore", "children_not_enumerated"}}})
		}
	}
	return ev
}

func TestTheFoldIsTheWholeJournalsReport(t *testing.T) {
	for _, done := range []bool{true, false} {
		requireSameReports(t, synthetic(240), done, 1, 7, 100)
	}
}

func TestAnInterleavedJournalIsRefused(t *testing.T) {
	ev := synthetic(3)
	a := Record{Version: RecordVersion, ID: "late", Ref: "pkg/d0/f000.go", SourceDigest: fmt.Sprintf("%064x", 0), Language: "go"}
	ev = append(ev, JournalEvent{Record: &a})
	_, err := ReviewFold(journalOf(t, ev), ReviewQuery{Shape: "review_queue", Tier: TierAll, PageBytes: 200000, Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "not in one run") {
		t.Fatalf("an interleaved journal must be refused: %v", err)
	}
}

func TestARunningScansFrontierIsCoverage(t *testing.T) {
	ev := synthetic(5)
	front := []Coverage{{Ref: "pkg/d0/f000.go", Kind: "file", Disposition: "pending", Reasons: []string{"not_yet_assessed"}},
		{Ref: "pkg/next.go", Kind: "file", Disposition: "pending", Reasons: []string{"not_yet_assessed"}},
		{Ref: "pkg/later", Kind: "subtree", Disposition: "unknown", Reasons: []string{"not_yet_listed", "children_not_enumerated"}}}
	p, err := ReviewFold(journalOf(t, ev), ReviewQuery{Shape: "coverage", PageBytes: 200000, Limit: 100, Frontier: front})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.Entries), "pkg/next.go") || !strings.Contains(string(p.Entries), "pkg/later") || strings.Count(string(p.Entries), `"pkg/d0/f000.go"`) != 1 || p.InventoryComplete {
		t.Fatalf("frontier: %s", p.Entries)
	}
}

func TestTheFoldAgreesOnARealJournal(t *testing.T) {
	name := os.Getenv("CQ_FOLD_JOURNAL")
	if name == "" {
		t.Skip("set CQ_FOLD_JOURNAL to a scan journal to compare on real data")
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var events []JournalEvent
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var e JournalEvent
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	requireSameReports(t, events, true, 100)
}

func TestATornJournalTailFailsTheReportClosed(t *testing.T) {
	j := journalOf(t, synthetic(4))
	s := j.Store.(*memStore)
	s.files["j"] = append(s.files["j"], []byte(`{"record":{"ver`)...)
	if _, err := ReviewFold(j, ReviewQuery{Shape: "review_queue", Tier: TierAll, PageBytes: 200000, Limit: 10}); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("a torn tail must refuse the report: %v", err)
	}
}
