package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	records, coverage, _, complete, err := JournalReport(j, 2<<20, 0)
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
	records, coverage, _, complete, err = JournalReport(&Journal{Store: store, Path: "journal"}, 2<<20, 0)
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
	rs, _, d1, complete, err := JournalReport(j, 1, 0)
	if err != nil || complete || len(rs) != 0 {
		t.Fatalf("prefix claimed complete: %v %v %+v", err, complete, rs)
	}
	if _, err := j.Append(journalRecord("b")); err != nil {
		t.Fatal(err)
	}
	_, _, d2, _, err := JournalReport(j, 1, 0)
	if err != nil || bytes.Equal(d1, d2) {
		t.Fatalf("generation ignores unretained suffix: %v", err)
	}
}

type countingStore struct {
	*memStore
	read int
}

func (c *countingStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	b, eof, err := c.memStore.Read(p, off, n)
	c.read += len(b)
	return b, eof, err
}

func TestAStepValidatesOnlyItsSuffix(t *testing.T) {
	m := newMemStore()
	store := &countingStore{memStore: m}
	j := &Journal{Store: store, Path: "journal"}
	m.files["journal"] = nil
	for i := 0; m.files["journal"] == nil || len(m.files["journal"]) < 2<<20; i++ {
		if _, err := j.appendAt(int64(len(m.files["journal"])), JournalEvent{Coverage: &Coverage{Ref: "pkg/file.go", Kind: "file", Disposition: "excluded", Reasons: []string{"gitignore"}}}); err != nil {
			t.Fatal(err)
		}
	}
	offset := int64(len(m.files["journal"]))
	for _, first := range []string{"verify", "cover"} {
		store.read = 0
		p := &JournalProgress{Journal: j, Offset: offset}
		if first == "verify" {
			if err := p.Verify(); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.Cover(Coverage{Ref: "next.go", Kind: "file", Disposition: "excluded", Reasons: []string{"test"}}); err != nil {
			t.Fatal(err)
		}
		if store.read > 64<<10 {
			t.Fatalf("%s: a step read %d bytes of a %d-byte journal", first, store.read, offset)
		}
		offset = p.End()
	}

	m.files["journal"] = append(m.files["journal"], []byte("{torn")...)
	if err := (&JournalProgress{Journal: j, Offset: offset}).Verify(); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("torn tail admitted: %v", err)
	}
}

func TestAReportReadsOnlyWhatItRanks(t *testing.T) {
	m := newMemStore()
	store := &countingStore{memStore: m}
	j := &Journal{Store: store, Path: "journal"}
	m.files["journal"] = nil
	add := func(ref string) {
		t.Helper()
		if _, err := j.appendAt(int64(len(m.files["journal"])), JournalEvent{Coverage: &Coverage{Ref: ref, Kind: "file", Disposition: "excluded", Reasons: []string{"gitignore"}}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; len(m.files["journal"]) < 4<<20; i++ {
		add(fmt.Sprintf("pkg/f%06d.go", i))
	}
	committed := int64(len(m.files["journal"]))
	add("uncommitted.go")
	store.read = 0
	_, coverage, d1, complete, err := JournalReport(j, 256<<10, committed)
	if err != nil || complete || len(coverage) == 0 || store.read > 512<<10 {
		t.Fatalf("err %v complete %v retained %d read %d of %d bytes", err, complete, len(coverage), store.read, len(m.files["journal"]))
	}
	add("next.go")
	if _, _, d2, _, err := JournalReport(j, 256<<10, committed); err != nil || bytes.Equal(d1, d2) {
		t.Fatalf("generation ignores an append past the prefix: %v", err)
	}
	m.files["journal"] = append(m.files["journal"], []byte("{torn")...)
	if _, _, _, _, err := JournalReport(j, 256<<10, committed); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("torn uncommitted tail admitted: %v", err)
	}

	small := &Journal{Store: newMemStore(), Path: "small"}
	small.Store.(*memStore).files["small"] = nil
	if _, err := small.Append(JournalEvent{Coverage: &Coverage{Ref: "a.go", Kind: "file", Disposition: "complete", Reasons: []string{}}}); err != nil {
		t.Fatal(err)
	}
	if _, c, _, complete, err := JournalReport(small, 256<<10, 0); err != nil || !complete || len(c) != 1 {
		t.Fatalf("small journal: %v %v %v", err, complete, c)
	}
}
