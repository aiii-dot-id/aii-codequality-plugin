package cq

import (
	"errors"
	"fmt"
	"testing"
)

func TestR4KAnEndingAfterTheBudgetRowsLeavesAJournalTheReportReads(t *testing.T) {
	for _, c := range []struct {
		reason, status string
		files          map[string]string
		pending        []string
		judged         int
	}{
		{"file_budget", StatusFileCap, map[string]string{"a.go": goodFile("A"), "b.go": goodFile("B"), "c.go": goodFile("C"), "sub/x.go": goodFile("X")}, []string{"b.go", "c.go"}, 1},
		{"examined_budget", StatusExaminedCap, func() map[string]string {
			m := map[string]string{"t.go": goodFile("T"), "u.go": goodFile("U"), "sub/x.go": goodFile("X")}
			for i := 0; i < ExaminedPerFile; i++ {
				m[fmt.Sprintf("s%02d.png", i)] = "not source"
			}
			return m
		}(), []string{"t.go", "u.go"}, 0},
	} {
		t.Run(c.reason, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, c.files)
			inSave := 0
			s, m, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, judge: &plantJudge{seen: map[string]bool{}}, attempts: &memAttempts{}, calls: 1000, most: 8, maxFiles: 1,
				endInSave: func(s *Scan) bool {
					if s.Status == c.status && inSave == 0 {
						inSave++
						return true
					}
					return false
				}})
			if s == nil {
				t.Fatal("no scan was saved")
			}
			if !stopped || s.Status != c.status || ended != 1 || inSave != 1 || s.FilesJudged != c.judged {
				t.Fatalf("stopped=%v status=%s after %d endings (%d in the save at the budget), judged %d; want %s after one ending in that save, %d judged", stopped, s.Status, ended, inSave, s.FilesJudged, c.status, c.judged)
			}

			rows := map[string]int{}
			if _, err := (&Journal{Store: m, Path: "journal"}).WalkAt(0, func(e JournalEvent, _, _ int64) error {
				if e.Coverage != nil {
					rows[e.Coverage.Ref]++
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for ref, n := range rows {
				want := 1
				if ref == "sub" || ref == c.pending[0] || ref == c.pending[1] {
					want = 2
				}
				if n != want {
					t.Fatalf("the journal holds %d coverage rows for %s; want %d (the budget rows written twice and no other), or this test shows nothing", n, ref, want)
				}
			}
			if len(rows) < 3 {
				t.Fatalf("the journal covers %d refs; want the budget rows among them", len(rows))
			}

			runs, err := runsOf(m)
			if err != nil {
				t.Fatalf("the journal of a scan whose worker was ended after its %s rows is not one the report reads: %v", c.reason, err)
			}
			for _, ref := range c.pending {
				rs := runs[ref]
				if len(rs) != 1 || len(rs[0].Records) != 0 || len(rs[0].Coverage) != 1 || rs[0].Coverage[0].Disposition != "pending" || !contains(rs[0].Coverage[0].Reasons, c.reason) {
					t.Errorf("%s is read as %+v; want one run of one pending row for %s", ref, rs, c.reason)
				}
			}
			if rs := runs["sub"]; len(rs) != 1 || len(rs[0].Coverage) != 1 || !contains(rs[0].Coverage[0].Reasons, c.reason) {
				t.Errorf("sub is read as %+v; want one run of one boundary for %s", rs, c.reason)
			}
		})
	}
}

func TestR4KOnlyABudgetMarkerSayingWhatItSaidMayComeBack(t *testing.T) {
	pend := func(ref, why string, size int64) Coverage {
		return Coverage{Ref: ref, Kind: "file", Disposition: "pending", Reasons: []string{why}, Size: size}
	}
	sub := func(ref, why string) Coverage {
		return Coverage{Ref: ref, Kind: "subtree", Disposition: "unknown", Reasons: []string{why, "children_not_enumerated"}}
	}
	file := func(ref string) Coverage {
		return Coverage{Ref: ref, Kind: "file", Disposition: "unknown", Reasons: []string{"unreadable"}}
	}
	const fb, eb = "file_budget", "examined_budget"
	for _, c := range []struct {
		name        string
		rows        []Coverage
		interleaved bool
		runs        int
		rowsOf      map[string]int
	}{
		{"a budget batch written twice", []Coverage{pend("b.go", fb, 7), pend("c.go", fb, 9), sub("s", fb), pend("b.go", fb, 7), pend("c.go", fb, 9), sub("s", fb)}, false, 3, map[string]int{"b.go": 1, "c.go": 1, "s": 1}},
		{"a budget batch written three times", []Coverage{pend("b.go", eb, 7), pend("c.go", eb, 9), pend("b.go", eb, 7), pend("c.go", eb, 9), pend("b.go", eb, 7), pend("c.go", eb, 9)}, false, 2, map[string]int{"b.go": 1, "c.go": 1}},
		{"a marker written twice with its run still open", []Coverage{pend("b.go", fb, 7), pend("b.go", fb, 7)}, false, 1, map[string]int{"b.go": 1}},
		{"the marker of a file whose run holds more comes back", []Coverage{file("a.go"), pend("a.go", eb, 7), pend("b.go", eb, 9), pend("a.go", eb, 7), pend("b.go", eb, 9)}, false, 2, map[string]int{"a.go": 2, "b.go": 1}},
		{"a marker that comes back for the other budget", []Coverage{pend("b.go", fb, 7), pend("c.go", fb, 9), pend("b.go", eb, 7)}, true, 0, nil},
		{"a marker that comes back with another size", []Coverage{pend("b.go", fb, 7), pend("c.go", fb, 9), pend("b.go", fb, 8)}, true, 0, nil},
		{"a file with evidence that comes back as a marker", []Coverage{file("a.go"), pend("b.go", fb, 9), pend("a.go", fb, 7)}, true, 0, nil},
		{"evidence that comes back after its file's marker", []Coverage{pend("a.go", fb, 7), pend("b.go", fb, 9), file("a.go")}, true, 0, nil},
		{"a marker that comes back as a folder", []Coverage{pend("a", fb, 0), pend("b.go", fb, 9), sub("a", fb)}, true, 0, nil},
		{"a folder that comes back as a marker", []Coverage{sub("a", fb), pend("b.go", fb, 9), pend("a", fb, 0)}, true, 0, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMemStore()
			m.files["journal"], m.files["intent"] = nil, nil
			p := &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}}
			for _, row := range c.rows {
				if err := p.Cover(row); err != nil {
					t.Fatal(err)
				}
			}
			runs, err := runsOf(m)
			if c.interleaved {
				if !errors.Is(err, ErrJournalInterleaved) {
					t.Fatalf("folds with %v; want %v", err, ErrJournalInterleaved)
				}
				return
			}
			if err != nil {
				t.Fatalf("folds with %v; want it read", err)
			}
			n := 0
			for ref, rs := range runs {
				n += len(rs)
				if len(rs) != 1 || len(rs[0].Coverage) != c.rowsOf[ref] {
					t.Errorf("%s is read as %d runs, the first with %d rows; want one run of %d", ref, len(rs), len(rs[0].Coverage), c.rowsOf[ref])
				}
			}
			if n != c.runs {
				t.Errorf("%d runs; want %d", n, c.runs)
			}
		})
	}
}
