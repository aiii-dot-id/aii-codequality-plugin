package cq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func r4pPartJudged(t *testing.T) (dir string, s *Scan, m *memStore, step func(calls int) error) {
	t.Helper()
	dir = t.TempDir()
	large, _ := goOfSize(MaxSourceBytes)
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "big/s1/large.go": large, "big/s2/y.go": goodFile("Y"), "zz/z.go": goodFile("Z")})
	fs := &OSFS{Root: dir}
	m = newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	s.AskIntent = true
	s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}}
	s.Attempts = &memAttempts{}
	judge := &splitJudge{plantJudge: plantJudge{seen: map[string]bool{}}}
	step = func(calls int) error {
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
	t.Logf("part judged: large.go at segment %d of %d, %d of %d windows done, %d files judged", s.File.Next, len(s.File.Segs), s.File.Done, s.File.Windows, s.FilesJudged)
	return dir, s, m, step
}

func r4pClosed(t *testing.T, s *Scan, m *memStore, disposition, reason string) {
	t.Helper()
	if s.File != nil {
		t.Errorf("the scan's state still carries the plan of %s (segment %d of %d), a file it will not come back to", s.File.Ref, s.File.Next, len(s.File.Segs))
	}
	runs := 0
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(r *FileRun) error {
		if r.Ref != "big/s1/large.go" {
			return nil
		}
		runs++
		t.Logf("the fold's run of %s: %d events, %d records, coverage %+v", r.Ref, r.Events, len(r.Records), r.Coverage)
		if len(r.Records) == 0 {
			t.Errorf("the run of large.go holds none of the windows judged before the scan left it")
		}
		if len(r.Coverage) != 1 {
			t.Errorf("the run of large.go has %d coverage rows; want the one that closes it", len(r.Coverage))
			return nil
		}
		c := r.Coverage[0]
		if c.Kind != "file" || c.Disposition != disposition || len(c.Reasons) != 1 || c.Reasons[0] != reason {
			t.Errorf("the closing row is %s %s %v; want file %s [%s]", c.Kind, c.Disposition, c.Reasons, disposition, reason)
		}
		if c.SourceDigest == "" || c.SourceDigest != r.Records[0].SourceDigest {
			t.Errorf("the closing row's digest is %q and its windows' is %q: the report joins a file's row to its windows by the digest", c.SourceDigest, r.Records[0].SourceDigest)
		}
		if c.ExpectedChunks < 2 || c.CompletedChunks < 1 || c.CompletedChunks >= c.ExpectedChunks {
			t.Errorf("the closing row says %d of %d windows; want some and not all", c.CompletedChunks, c.ExpectedChunks)
		}
		return nil
	}); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
	if runs != 1 {
		t.Errorf("the journal holds %d runs of large.go; want one", runs)
	}
	page := reviewJSON(t, m, ReviewQuery{Shape: "coverage"})
	i := strings.Index(page, "big/s1/large.go")
	if i < 0 {
		t.Errorf("the coverage page does not name the file the scan left")
		return
	}
	t.Logf("the coverage page near the file: %s", page[max(0, i-300):min(len(page), i+300)])
	if n := strings.Count(page, "big/s1/large.go"); n != 1 {
		t.Errorf("the coverage page names the file %d times; want once, its row joined to its windows", n)
	}
}

func TestR4PAFolderLeftOutWhileAFileUnderItIsPartJudgedClosesTheFile(t *testing.T) {
	dir, s, m, step := r4pPartJudged(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("big/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6 && s.Status == "running"; i++ {
		if err := step(1000); err != nil {
			t.Fatalf("step %d after the rule: %v", i, err)
		}
	}
	if s.Status != "done" || s.LastError != "" || s.FilesJudged != 2 || s.Excluded["gitignore"] < 1 {
		t.Fatalf("precondition: the scan is %q with %d files judged, excluded %v (last error %q); want it done with a.go and zz/z.go judged and big left out by the rule", s.Status, s.FilesJudged, s.Excluded, s.LastError)
	}
	r4pClosed(t, s, m, "excluded", "gitignore")
}

func TestR4PAFileLeftOutByARuleWhileItIsPartJudgedIsClosed(t *testing.T) {
	dir, s, m, step := r4pPartJudged(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("large.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6 && s.Status == "running"; i++ {
		if err := step(1000); err != nil {
			t.Fatalf("step %d after the rule: %v", i, err)
		}
	}
	if s.Status != "done" || s.LastError != "" || s.FilesJudged != 3 || s.Excluded["gitignore"] != 1 {
		t.Fatalf("precondition: the scan is %q with %d files judged, excluded %v (last error %q); want it done with a.go, big/s2/y.go and zz/z.go judged and large.go left out by the rule", s.Status, s.FilesJudged, s.Excluded, s.LastError)
	}
	r4pClosed(t, s, m, "excluded", "gitignore")
}
