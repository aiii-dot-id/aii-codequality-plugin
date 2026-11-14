package cq

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type splitJudge struct {
	plantJudge
	left int
}

func (j *splitJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	if j.left <= 0 {
		return nil, ErrStepCalls
	}
	j.left--
	return j.plantJudge.Ask(state, qs)
}

func stepsSplitWithACheckpoint(t *testing.T, dir string, most, calls int) (*Scan, *memStore) {
	t.Helper()
	fs := &OSFS{Root: dir}
	m := newMemStore()
	m.files["journal"], m.files["intent"], m.files["cp0"], m.files["cp1"] = nil, nil, nil, nil
	pair := CheckpointPair{Store: m, Paths: [2]string{"cp0", "cp1"}}
	first, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := json.Marshal(first)
	load := func() *Scan {
		t.Helper()
		var s Scan
		if err := json.Unmarshal(initial, &s); err != nil {
			t.Fatal(err)
		}
		state, _, _, err := pair.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(state) > 0 {
			if err := json.Unmarshal(state, &s); err != nil {
				t.Fatal(err)
			}
		}
		return &s
	}
	at := &memAttempts{}
	judge := &splitJudge{plantJudge: plantJudge{seen: map[string]bool{}}}
	for i := 0; ; i++ {
		s := load()
		if s.Status != "running" || i == most {
			return s, m
		}
		judge.left = calls
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"},
			Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		s.Attempts = at
		save := func() error {
			b, err := json.Marshal(s)
			if err != nil {
				return err
			}
			err = pair.Save(b)
			if !errors.Is(err, ErrCheckpointFull) {
				return err
			}
			s.Status = "storage_cap"
			s.Stack, s.Pending, s.Refused, s.NotFinished, s.File, s.Dir = nil, nil, nil, nil, nil, ""
			if b, err = json.Marshal(s); err != nil {
				return err
			}
			return pair.Save(b)
		}
		s.Checkpoint = save
		if _, err := s.Step(table(t), fs, judge, nil, 1000, 400); err != nil {
			t.Fatal(err)
		}
		if err := save(); err != nil {
			t.Fatal(err)
		}
		if err := s.Settle(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestR4JAFileWhosePlanIsTooLargeForTheStateIsRefusedByNameAndTheScanGoesOn(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < 160; i++ {
		fmt.Fprintf(&b, "func F%03d(a [1%s]int) {}\n", i, strings.Repeat("<<1>>1", 30))
	}
	for i := 0; b.Len() < MaxSourceBytes-64; i++ {
		fmt.Fprintf(&b, "var v%07d = %d\n", i, i)
	}
	last := goodFile("Last")
	writeFiles(t, dir, map[string]string{"0big.go": b.String(), "zlast.go": last, "zz/z.go": goodFile("Z")})

	long := strings.Repeat("x", 240)
	pending := []Entry{{Name: "0big.go", Size: int64(b.Len())}, {Name: "zlast.go", Size: int64(len(last))}}
	var names []string
	for i := 0; ; i++ {
		e := Entry{Name: fmt.Sprintf("f%04d_%s.zzq", i, long), Size: 2}
		if !frontierFits(FrontierBytes, "", nil, []string{"zz"}, append(pending, e)) {
			break
		}
		pending = append(pending, e)
		names = append(names, e.Name)
	}
	if len(names) < 100 {
		t.Fatalf("the frontier admitted only %d of the long names", len(names))
	}
	names = names[:len(names)-3]
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cur, _, err := PlanFile(table(t), &OSFS{Root: dir}, "0big.go", "0big.go", "go", ChunkBytes, false)
	if err != nil || cur == nil {
		t.Fatalf("the large file was not planned: %v", err)
	}
	cur.Lang, cur.Langs = "go", []string{"go"}
	cursor, _ := json.Marshal(cur)
	frontier, _ := json.Marshal(pending[:len(pending)-3])
	t.Logf("the file's cursor encodes to %d bytes (%d outline items, %d segments), the folder's frontier to %d (%d entries); a checkpoint holds %d",
		len(cursor), len(cur.Outline), len(cur.Segs), len(frontier), len(pending)-3, CheckpointBytes)

	s, m := stepsSplitWithACheckpoint(t, dir, 40, 60)
	if s.Status != "done" || s.FilesJudged != 2 {
		t.Fatalf("the scan is %q with %d files judged (last error %q); want it done with zlast.go and zz/z.go judged", s.Status, s.FilesJudged, s.LastError)
	}
	state, _ := json.Marshal(s)
	if !strings.Contains(string(state), `"plan_too_large":["0big.go"]`) || !strings.Contains(string(state), `"plan_too_large":1`) {
		t.Errorf("the scan's state does not name the file and count it once: excluded %v", s.Excluded)
	}
	journal := string(m.files["journal"])
	if n := strings.Count(journal, `"plan_too_large"`); n != 1 {
		t.Errorf("the journal has %d rows with the file's reason; want one", n)
	}
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(*FileRun) error { return nil }); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}
