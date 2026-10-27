package cq

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type memoryProgress struct {
	records  []Record
	coverage []Coverage
	end      int64
	failSave bool
}

func (p *memoryProgress) Load(ref string) ([]Record, error) {
	var out []Record
	for _, r := range p.records {
		if r.Ref == ref {
			out = append(out, r)
		}
	}
	return out, nil
}
func (p *memoryProgress) Save(r Record) error {
	if p.failSave {
		return errors.New("storage fixture")
	}
	b, _ := json.Marshal(r)
	var copy Record
	if err := json.Unmarshal(b, &copy); err != nil {
		return err
	}
	p.records = DeduplicateRecords(append(p.records, copy))
	p.end += int64(len(b))
	return nil
}
func (p *memoryProgress) Cover(c Coverage) error {
	p.coverage = append(p.coverage, c)
	p.end++
	return nil
}
func (p *memoryProgress) End() int64 { return p.end }
func progressScan(t *testing.T) (*Scan, *Table, *OSFS) {
	t.Helper()
	table, e := LoadTable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e = os.WriteFile(filepath.Join(dir, "f.go"), []byte(progressSource), 0600); e != nil {
		t.Fatal(e)
	}
	fs := &OSFS{Root: dir}
	s, e := NewScan(fs, "fixture", dir, "", nil, nil, true, 100, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	return s, table, fs
}

func TestWalkerPartialFindingsSurvive(t *testing.T) {
	s, table, fs := progressScan(t)
	p := &memoryProgress{}
	s.Progress = p
	recs, e := s.Step(table, fs, &progressJudge{remaining: 1}, nil, 1, 20)
	if e != nil || s.Status != "judge_unavailable" || len(recs) != 2 || len(recs[0].Findings) != 1 || len(s.Pending) != 1 {
		t.Fatalf("lost partial: %v %+v recs %d", e, s, len(recs))
	}

	b, _ := json.Marshal(s)
	var resumed Scan
	if e = json.Unmarshal(b, &resumed); e != nil {
		t.Fatal(e)
	}
	resumed.Progress = p
	recs, e = resumed.Step(table, fs, &progressJudge{remaining: 1}, nil, 1, 20)
	if e != nil || resumed.Status != "done" || len(recs) != 2 || resumed.FilesJudged != 1 || resumed.Chunks != 1 {
		t.Fatalf("resume: %v %+v recs %d", e, resumed, len(recs))
	}
	if len(p.records) != 2 || p.coverage[len(p.coverage)-1].Disposition != "complete" {
		t.Fatalf("bad durable progress: %+v", p)
	}
}
func TestChangedSourceNotMerged(t *testing.T) {
	s, table, fs := progressScan(t)
	p := &memoryProgress{}
	s.Progress = p
	if _, e := s.Step(table, fs, &progressJudge{remaining: 1}, nil, 1, 20); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(fs.Root, "f.go"), []byte(progressSource+"\n// changed\n"), 0600); e != nil {
		t.Fatal(e)
	}
	j := &progressJudge{remaining: 20}
	_, e := s.Step(table, fs, j, nil, 20, 20)
	if e != nil || j.calls != 0 || s.Excluded["source_changed"] != 1 || p.coverage[len(p.coverage)-1].Disposition != "stale" {
		t.Fatalf("mixed source generations: %v %+v calls %d", e, s, j.calls)
	}
}
func TestWalkerSaveFailureKeepsPending(t *testing.T) {
	s, table, fs := progressScan(t)
	p := &memoryProgress{failSave: true}
	s.Progress = p
	_, e := s.Step(table, fs, &progressJudge{remaining: 20}, nil, 20, 20)
	if !errors.Is(e, ErrProgress) || len(s.Pending) != 1 || s.FilesSeen != 0 || s.FilesJudged != 0 || s.ProgressOffset != 0 {
		t.Fatalf("advanced past failed save: %v %+v", e, s)
	}
}
