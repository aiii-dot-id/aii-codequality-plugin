package cq

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type r7nFS struct {
	*OSFS
	cut   map[string]bool
	after map[string]func()
}

func (f *r7nFS) List(dir string) ([]Entry, bool, error) {
	es, truncated, err := f.OSFS.List(dir)
	if fn := f.after[dir]; fn != nil {
		delete(f.after, dir)
		fn()
	}
	return es, truncated || f.cut[dir], err
}

func r7nScan(t *testing.T, fs FS) (s *Scan, m *memStore, step func(examined int) error) {
	t.Helper()
	m = newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	s.AskIntent = true
	s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}}
	s.Attempts = &memAttempts{}
	judge := &plantJudge{seen: map[string]bool{}}
	step = func(examined int) error {
		t.Helper()
		if _, err := s.Step(table(t), fs, judge, nil, 1000, examined); err != nil {
			return err
		}
		return s.Settle()
	}
	return s, m, step
}

func TestR7NAFolderLeftOutAfterItWasListedIsCountedOnceWhenTheListOfNamesIsFull(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"a.go": goodFile("A"), "big/s1/x.go": goodFile("X"), "big/s2/y.go": goodFile("Y"), "big/s3/v.go": goodFile("V"), "big2/w.go": goodFile("W"), "zz/z.go": goodFile("Z")}
	fs := &r7nFS{OSFS: &OSFS{Root: dir}, cut: map[string]bool{}}
	for i := 1; i <= TooWideListed; i++ {
		name := fmt.Sprintf("a%02d", i)
		files[name+"/f.go"] = goodFile(fmt.Sprintf("F%02d", i))
		fs.cut[name] = true
	}
	writeFiles(t, dir, files)
	s, m, step := r7nScan(t, fs)
	if err := step(1); err != nil {
		t.Fatal(err)
	}
	if s.FilesJudged != 1 || s.Dir != "big/s1" || len(s.Pending) != 1 || !reflect.DeepEqual(s.Stack, []string{"zz", "big2", "big/s3", "big/s2"}) ||
		s.Excluded[ListingTruncated] != TooWideListed || len(s.TooWide)+len(s.Truncated) != TooWideListed {
		t.Fatalf("precondition: after one file the scan is in %q with %d files pending, %d judged, %v to list, excluded %v and %d folders named; want it in big/s1 with x.go pending, zz, big2, big/s3 and big/s2 to list, and the sixteen cut short counted and named",
			s.Dir, len(s.Pending), s.FilesJudged, s.Stack, s.Excluded, len(s.TooWide)+len(s.Truncated))
	}
	fs.cut["big"] = true
	for i := 0; i < 6 && s.Status == "running"; i++ {
		if err := step(400); err != nil {
			t.Fatalf("step %d after the folder grew: %v", i, err)
		}
	}
	if s.Status != "done" || s.FilesJudged != 3 {
		t.Fatalf("the scan is %q with %d files judged (last error %q); want it done with a.go, big2/w.go and zz/z.go judged", s.Status, s.FilesJudged, s.LastError)
	}
	if n := s.Excluded[ListingTruncated]; n != TooWideListed+1 {
		t.Errorf("%d folders are counted as listed cut short; want %d, the sixteen and big once", n, TooWideListed+1)
	}
	journal := string(m.files["journal"])
	if n := strings.Count(journal, `"`+ListingTruncated+`"`); n != TooWideListed+1 {
		t.Errorf("the journal has %d rows with the reason; want %d, one for each folder", n, TooWideListed+1)
	}
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(*FileRun) error { return nil }); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}

func TestR7NAFolderThatBecameARepositoryIsCountedOnceForTheFoldersUnderIt(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "big/s2/y.go": goodFile("Y"), "big/s3/v.go": goodFile("V"), "big2/w.go": goodFile("W"), "zz/z.go": goodFile("Z")})
	if err := os.MkdirAll(filepath.Join(dir, "big", "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &r7nFS{OSFS: &OSFS{Root: dir}, cut: map[string]bool{}, after: map[string]func(){}}
	s, m, step := r7nScan(t, fs)
	var stackAt []string
	fs.after["big/s1"] = func() {
		stackAt = append([]string(nil), s.Stack...)
		if err := os.Mkdir(filepath.Join(dir, "big", ".git"), 0o755); err != nil {
			t.Errorf("making big a repository: %v", err)
		}
	}
	for i := 0; i < 8 && s.Status == "running"; i++ {
		if err := step(400); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if !reflect.DeepEqual(stackAt, []string{"zz", "big2", "big/s3", "big/s2", "big/s1"}) {
		t.Fatalf("precondition: when big/s1 was listed the scan had %v to list; want zz, big2, big/s3, big/s2 and big/s1 itself", stackAt)
	}
	if s.Status != "done" || s.FilesJudged != 3 {
		t.Fatalf("the scan is %q with %d files judged (last error %q); want it done with a.go, big2/w.go and zz/z.go judged", s.Status, s.FilesJudged, s.LastError)
	}
	if n := s.Excluded["nested_repository"]; n != 1 {
		t.Errorf("big is counted %d times as a nested repository; want once (excluded %v)", n, s.Excluded)
	}
	journal := string(m.files["journal"])
	if n := strings.Count(journal, `"nested_repository"`); n != 1 {
		t.Errorf("the journal has %d rows with the reason; want one", n)
	}
	if strings.Contains(journal, "big/s2") || strings.Contains(journal, "big/s3") {
		t.Errorf("the journal names a file or folder under the folder left out")
	}
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(*FileRun) error { return nil }); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}
