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

func stepsWithACheckpoint(t *testing.T, dir string, most int) (*Scan, *memStore) {
	t.Helper()
	return stepsOn(t, &OSFS{Root: dir}, most)
}

func stepsOn(t *testing.T, fs FS, most int) (*Scan, *memStore) {
	t.Helper()
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
	judge := &plantJudge{seen: map[string]bool{}}
	for i := 0; ; i++ {
		s := load()
		if s.Status != "running" || i == most {
			return s, m
		}
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

func TestR4IAFolderTooWideForTheCheckpointIsLeftOutByNameAndTheScanGoesOn(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "wide/sub/y.go": goodFile("Y"), "zz/z.go": goodFile("Z")})
	long := strings.Repeat("x", 241)
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("f%04d_%s.go", i, long)
		if err := os.WriteFile(filepath.Join(dir, "wide", name), []byte("package wide\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, m := stepsWithACheckpoint(t, dir, 12)
	if s.Status != "done" || s.FilesJudged != 2 {
		t.Fatalf("the scan is %q with %d files judged (last error %q); want it done with a.go and zz/z.go judged", s.Status, s.FilesJudged, s.LastError)
	}
	state, _ := json.Marshal(s)
	if !strings.Contains(string(state), `"too_wide":["wide"]`) || !strings.Contains(string(state), `"listing_too_wide":1`) {
		t.Errorf("the scan's state does not name the folder and count it once: excluded %v", s.Excluded)
	}
	journal := string(m.files["journal"])
	if n := strings.Count(journal, `"listing_too_wide"`); n != 1 {
		t.Errorf("the journal has %d rows with the folder's reason; want one", n)
	}
	if strings.Contains(journal, long) || strings.Contains(journal, "wide/sub") {
		t.Errorf("the journal names a file or folder under the folder left out")
	}
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(*FileRun) error { return nil }); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}
