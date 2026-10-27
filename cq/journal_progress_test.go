package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestJournalResumeWithoutCacheOrCheckpoint(t *testing.T) {
	s, table, fs := progressScan(t)
	before, _ := json.Marshal(s)
	j, store := journalFixture(t)
	s.Progress = &JournalProgress{Journal: j}
	if _, err := s.Step(table, fs, &progressJudge{remaining: 1}, nil, 1, 20); err != nil {
		t.Fatal(err)
	}
	records, coverage, _, complete, err := JournalReport(j, 2<<20)
	if err != nil || !complete || len(records) != 2 || len(records[0].Findings) != 1 {
		t.Fatalf("lost result before checkpoint: %v %+v", err, records)
	}
	view := BuildReview(records, coverage, false, complete, "")
	if len(view.Files) != 1 || view.Files[0].Status == "assessed" {
		t.Fatalf("partial not visible: %+v", view)
	}

	var restart Scan
	if err := json.Unmarshal(before, &restart); err != nil {
		t.Fatal(err)
	}
	restart.Progress = &JournalProgress{Journal: &Journal{Store: store, Path: "journal"}}
	judge := &progressJudge{remaining: 1}
	if _, err := restart.Step(table, fs, judge, nil, 1, 20); err != nil {
		t.Fatal(err)
	}
	if restart.Status != "done" || judge.calls != 1 || restart.FilesJudged != 1 {
		t.Fatalf("replayed inference or wrong checkpoint: %+v calls=%d", restart, judge.calls)
	}
	records, coverage, _, complete, err = JournalReport(&Journal{Store: store, Path: "journal"}, 2<<20)
	if err != nil || len(records) != 2 || !complete {
		t.Fatalf("replay not idempotent: %v %+v", err, records)
	}
	view = BuildReview(records, coverage, true, complete, "")
	if len(view.Files) != 1 || len(view.Files[0].Findings) != 1 {
		t.Fatalf("duplicate findings: %+v", view)
	}
}

func TestJournalProgressAppendFailures(t *testing.T) {
	for _, n := range []int{0, 7, -1} {
		t.Run(string(rune(n+80)), func(t *testing.T) {
			j, store := journalFixture(t)
			store.appendErr, store.writeBeforeError = errors.New("injected append"), n
			p := &JournalProgress{Journal: j}
			r := *journalRecord("stable").Record
			if err := p.Save(r); err == nil || p.End() != 0 {
				t.Fatalf("acknowledged failed append: %v end %d", err, p.End())
			}
			store.appendErr = nil
			resumed := &JournalProgress{Journal: &Journal{Store: store, Path: "journal"}}
			rs, err := resumed.Load("a.go")
			if n == 7 {
				if !errors.Is(err, ErrJournalCorrupt) {
					t.Fatalf("torn tail not refused: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (n == -1 && len(rs) != 1) || (n == 0 && len(rs) != 0) {
				t.Fatalf("wrong replay: %+v", rs)
			}
			if err := resumed.Save(r); err != nil {
				t.Fatal(err)
			}
			rs, err = resumed.Load("a.go")
			if err != nil || len(rs) != 1 {
				t.Fatalf("duplicate: %v %+v", err, rs)
			}
		})
	}
}

func TestJournalReportBoundAndGeneration(t *testing.T) {
	j, _ := journalFixture(t)
	if _, err := j.Append(journalRecord("a")); err != nil {
		t.Fatal(err)
	}
	rs, _, d1, complete, err := JournalReport(j, 1)
	if err != nil || complete || len(rs) != 0 {
		t.Fatalf("prefix claimed complete: %v %v %+v", err, complete, rs)
	}
	if _, err := j.Append(journalRecord("b")); err != nil {
		t.Fatal(err)
	}
	_, _, d2, _, err := JournalReport(j, 1)
	if err != nil || bytes.Equal(d1, d2) {
		t.Fatalf("generation ignores unretained suffix: %v", err)
	}
}
