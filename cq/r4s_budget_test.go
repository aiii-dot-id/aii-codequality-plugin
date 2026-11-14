package cq

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestR4SAScanStartedWithHeavyPatternsLeavesAWideFolderOutAndItsStateStillSaves(t *testing.T) {
	dir := t.TempDir()
	amp := func(n int) string { return strings.Repeat("&", n) }
	exclude := make([]string, 64)
	for i := range exclude {
		exclude[i] = amp(256)
	}
	runtime, err := json.Marshal(map[string]any{
		"pin":   map[string]string{"backend": "local", "endpoint": "http://h/" + amp(4087), "requested": "m", "model": "m"},
		"retry": map[string]int{"attempts": 0, "not_before_ms": 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("n", 242)
	one, _ := json.Marshal(Entry{Name: fmt.Sprintf("%04d%s.txt", 0, long), Size: 1})
	files := map[string]string{"zz/a.go": goodFile("A")}
	wide := (FrontierBytes - 600) / (len(one) + 1)
	for i := 0; i < wide; i++ {
		files[fmt.Sprintf("wide/%04d%s.txt", i, long)] = "x"
	}
	writeFiles(t, dir, files)

	fs := &OSFS{Root: dir}
	m := newMemStore()
	m.files["journal"], m.files["intent"], m.files["cp0"], m.files["cp1"] = nil, nil, nil, nil
	pair := CheckpointPair{Store: m, Paths: [2]string{"cp0", "cp1"}}
	first, err := NewScan(fs, "s", "/"+amp(511), "", exclude, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	first.Runtime = runtime
	initial, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) > 240<<10 {
		t.Fatalf("precondition: the scan as started is %d bytes, more than a start admits", len(initial))
	}
	at := &memAttempts{}
	judge := &plantJudge{seen: map[string]bool{}}
	var s *Scan
	for i := 0; ; i++ {
		s = &Scan{}
		if err := json.Unmarshal(initial, s); err != nil {
			t.Fatal(err)
		}
		state, _, _, err := pair.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(state) > 0 {
			if err := json.Unmarshal(state, s); err != nil {
				t.Fatal(err)
			}
		}
		if s.Status != "running" {
			break
		}
		if i == 40 {
			t.Fatalf("the scan is still running after %d steps", i)
		}
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"},
			Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		s.Attempts = at
		step := s
		save := func() error {
			b, err := json.Marshal(step)
			if err != nil {
				return err
			}
			err = pair.Save(b)
			if errors.Is(err, ErrCheckpointFull) {
				frontier, _ := json.Marshal(step.Pending)
				patterns, _ := json.Marshal(step.Exclude)
				return fmt.Errorf("the state the walk admitted is %d bytes and does not fit the checkpoint's %d: in %q, %d files left to visit are %d bytes, the patterns %d, the request state %d: %w",
					len(b), CheckpointBytes, step.Dir, len(step.Pending), len(frontier), len(patterns), len(step.Runtime), err)
			}
			return err
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
	if s.Status != "done" {
		t.Fatalf("the scan ended as %q (%s); want done", s.Status, s.LastError)
	}
	if len(s.TooWide) != 1 || s.TooWide[0] != "wide" || s.Excluded[FolderTooWide] != 1 {
		t.Fatalf("folders left out as too wide: %q, counted %d; want wide, once", s.TooWide, s.Excluded[FolderTooWide])
	}
	if s.FilesJudged != 1 || s.DirsListed != 2 {
		t.Errorf("judged %d files in %d folders listed; want zz/a.go, and the folder scanned and zz", s.FilesJudged, s.DirsListed)
	}
}
