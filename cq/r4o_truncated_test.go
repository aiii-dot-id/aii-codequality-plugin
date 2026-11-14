package cq

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type cutFS struct {
	*OSFS
	cut map[string]bool
}

func (f *cutFS) List(dir string) ([]Entry, bool, error) {
	es, truncated, err := f.OSFS.List(dir)
	return es, truncated || f.cut[dir], err
}

func TestR4OAFolderWhoseListingIsCutShortIsLeftOutByNameAndTheScanGoesOn(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "big/x.go": goodFile("X"), "big/sub/y.go": goodFile("Y"), "zz/z.go": goodFile("Z")})
	fs := &cutFS{OSFS: &OSFS{Root: dir}, cut: map[string]bool{"big": true}}
	s, m := stepsOn(t, fs, 6)
	if s.Status != "done" || s.FilesJudged != 2 {
		t.Fatalf("the scan is %q with %d files judged (last error %q); want it done with a.go and zz/z.go judged", s.Status, s.FilesJudged, s.LastError)
	}
	state, _ := json.Marshal(s)
	if !strings.Contains(string(state), `"listing_truncated":["big"]`) || s.Excluded["dir_listing_truncated"] != 1 {
		t.Errorf("the scan's state does not name the folder and count it once: excluded %v", s.Excluded)
	}
	if s.LastError != "" {
		t.Errorf("a folder left out by name left an error behind: %q", s.LastError)
	}
	journal := string(m.files["journal"])
	if n := strings.Count(journal, `"dir_listing_truncated"`); n != 1 {
		t.Errorf("the journal has %d rows with the folder's reason; want one", n)
	}
	if strings.Contains(journal, "big/x.go") || strings.Contains(journal, "big/sub") {
		t.Errorf("the journal names a file or folder under the folder left out")
	}
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(*FileRun) error { return nil }); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}

func TestR4OAFolderThatGrewPastTheListingAfterItWasListedIsCountedOnce(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "big/s1/x.go": goodFile("X"), "big/s2/y.go": goodFile("Y"), "zz/z.go": goodFile("Z")})
	fs := &cutFS{OSFS: &OSFS{Root: dir}, cut: map[string]bool{}}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	s.AskIntent = true
	s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}}
	s.Attempts = &memAttempts{}
	judge := &plantJudge{seen: map[string]bool{}}
	step := func(examined int) error {
		t.Helper()
		if _, err := s.Step(table(t), fs, judge, nil, 1000, examined); err != nil {
			return err
		}
		return s.Settle()
	}
	if err := step(1); err != nil {
		t.Fatal(err)
	}
	if s.FilesJudged != 1 || s.Dir != "big/s1" || len(s.Pending) != 1 || !reflect.DeepEqual(s.Stack, []string{"zz", "big/s2"}) {
		t.Fatalf("precondition: after one file the scan is in %q with %d files pending, %d judged and %v to list; want it in big/s1 with x.go pending and zz and big/s2 to list",
			s.Dir, len(s.Pending), s.FilesJudged, s.Stack)
	}
	fs.cut["big"] = true
	for i := 0; i < 4 && s.Status == "running"; i++ {
		if err := step(400); err != nil {
			t.Fatalf("step %d after the folder grew: %v", i, err)
		}
	}
	if s.Status != "done" || s.FilesJudged != 2 {
		t.Fatalf("the scan is %q with %d files judged (last error %q); want it done with a.go and zz/z.go judged", s.Status, s.FilesJudged, s.LastError)
	}
	state, _ := json.Marshal(s)
	if !strings.Contains(string(state), `"listing_truncated":["big"]`) || s.Excluded["dir_listing_truncated"] != 1 {
		t.Errorf("the scan's state does not name the folder and count it once: %s; excluded %v", state, s.Excluded)
	}
	journal := string(m.files["journal"])
	if strings.Contains(journal, "big/s1") || strings.Contains(journal, "big/s2") {
		t.Errorf("the journal names a file or folder under the folder left out")
	}
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(*FileRun) error { return nil }); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}

func TestR4OAFolderCutShortWhileAFileUnderItIsPartJudgedClosesTheFile(t *testing.T) {
	dir := t.TempDir()
	large, _ := goOfSize(MaxSourceBytes)
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "big/s1/large.go": large, "big/s2/y.go": goodFile("Y"), "zz/z.go": goodFile("Z")})
	fs := &cutFS{OSFS: &OSFS{Root: dir}, cut: map[string]bool{}}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	s.AskIntent = true
	s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}}
	s.Attempts = &memAttempts{}
	judge := &splitJudge{plantJudge: plantJudge{seen: map[string]bool{}}}
	step := func(calls int) error {
		t.Helper()
		judge.left = calls
		if _, err := s.Step(table(t), fs, judge, nil, 1000, 400); err != nil {
			return err
		}
		return s.Settle()
	}
	for i := 0; i < 60 && (s.File == nil || s.File.Next < 1); i++ {
		if err := step(4); err != nil {
			t.Fatal(err)
		}
	}
	if s.File == nil || s.File.Ref != "big/s1/large.go" || s.File.Next < 1 || s.File.Next >= len(s.File.Segs) || s.Dir != "big/s1" || s.Status != "running" {
		t.Fatalf("precondition: the scan is %q in %q with cursor %+v; want it running in big/s1 with large.go part judged", s.Status, s.Dir, s.File)
	}
	t.Logf("before the folder grew: large.go at segment %d of %d, %d files judged", s.File.Next, len(s.File.Segs), s.FilesJudged)
	fs.cut["big"] = true
	for i := 0; i < 6 && s.Status == "running"; i++ {
		if err := step(1000); err != nil {
			t.Fatalf("step %d after the folder grew: %v", i, err)
		}
	}
	if s.Status != "done" || s.LastError != "" || s.FilesJudged != 2 {
		t.Fatalf("the scan is %q with %d files judged (last error %q); want it done with a.go and zz/z.go judged", s.Status, s.FilesJudged, s.LastError)
	}

	state, _ := json.Marshal(s)
	if !strings.Contains(string(state), `"listing_truncated":["big"]`) || s.Excluded["dir_listing_truncated"] != 1 {
		t.Errorf("the scan's state does not name the folder and count it once: excluded %v", s.Excluded)
	}
	r4pClosed(t, s, m, "unknown", "dir_listing_truncated")
	marks := r4rMarks(t, m)
	t.Logf("the side file's marks: %v", marks)
	if len(marks) != 1 || marks[0] != "big/s1/large.go (dir_listing_truncated)" {
		t.Errorf("the side file's marks are %v; want the one of big/s1/large.go, not read, with the folder's reason", marks)
	}
	if page := reviewJSON(t, m, ReviewQuery{Shape: "coverage"}); !strings.Contains(page, "dir_listing_truncated") {
		t.Errorf("the coverage page does not give the reason the folder and the file were left")
	}
}
