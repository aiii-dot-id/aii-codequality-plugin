package cq

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type r4qFS struct {
	*OSFS
	root           string
	ruleAt, listed int
	ruled          bool
	endings        int
	failBig        int
}

func (f *r4qFS) List(dir string) ([]Entry, bool, error) {
	if dir == "big/s1" && f.ruleAt > 0 {
		if f.listed++; f.listed == f.ruleAt {
			if err := os.WriteFile(filepath.Join(f.root, ".gitignore"), []byte("big/\n"), 0o644); err != nil {
				panic(err)
			}
			f.ruled = true
		}
	}
	if dir == "zz" && f.ruled && f.endings == 0 {
		f.endings++
		panic(workerEnded{})
	}
	if dir == "big" && f.failBig > 0 {
		f.failBig--
		return nil, false, errors.New("r4q: big cannot be listed now")
	}
	return f.OSFS.List(dir)
}

func r4qTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	large, _ := goOfSize(MaxSourceBytes)
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "big/s1/large.go": large, "big/s2/y.go": goodFile("Y"), "zz/z.go": goodFile("Z")})
	return dir
}

func r4qPartJudged(t *testing.T) (fs *r4qFS, s *Scan, m *memStore, step func(calls int) error) {
	t.Helper()
	dir := r4qTree(t)
	fs = &r4qFS{OSFS: &OSFS{Root: dir}, root: dir}
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
	return fs, s, m, step
}

func TestR4QAnEndingAfterAPartJudgedFilesClosingRowLeavesAJournalTheReportReads(t *testing.T) {
	dir := r4qTree(t)

	fs := &r4qFS{OSFS: &OSFS{Root: dir}, root: dir, ruleAt: 4}
	judge := &splitJudge{plantJudge: plantJudge{seen: map[string]bool{}}}
	s, m, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, fs: fs, judge: judge, attempts: &memAttempts{}, calls: 1000, most: 40, newStep: func() { judge.left = 12 }})
	if s == nil {
		t.Fatal("no scan was saved")
	}
	if !stopped || s.Status != "done" || ended != 1 || fs.endings != 1 || !fs.ruled || s.FilesJudged != 2 {
		t.Fatalf("precondition: stopped=%v status=%s after %d endings (%d in the listing of zz), rule written=%v, judged %d; want done after one ending, with a.go and zz/z.go judged", stopped, s.Status, ended, fs.endings, fs.ruled, s.FilesJudged)
	}

	var order []string
	closing := 0
	if _, err := (&Journal{Store: m, Path: "journal"}).WalkAt(0, func(e JournalEvent, _, _ int64) error {
		if e.Coverage == nil {
			return nil
		}
		switch {
		case e.Coverage.Ref == "big/s1/large.go" && e.Coverage.SourceDigest != "":
			closing++
			order = append(order, "file")
		case e.Coverage.Ref == "big":
			order = append(order, "folder")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("closing rows and the folder's rows, in journal order: %v", order)
	if closing != 2 || len(order) < 3 || order[0] != "file" || order[1] != "folder" || !contains(order[2:], "file") {
		t.Fatalf("precondition: the journal holds %d closing rows of large.go in the order %v; want two with the folder's row between, or this test shows nothing", closing, order)
	}
	if err := foldOf(m); err != nil {
		t.Fatalf("the journal of a scan whose worker was ended after a part-judged file's closing row is not one the report reads: %v", err)
	}
	r4pClosed(t, s, m, "excluded", "gitignore")
}

func TestR4QAFileTheScanLeftKeepsNoAttemptRecord(t *testing.T) {
	fs, s, _, step := r4qPartJudged(t)
	att := s.Attempts.(*memAttempts)
	if att.a != (Attempt{}) {
		t.Fatalf("precondition: the record is %+v before the test sets it; want none", att.a)
	}
	att.a = Attempt{Ref: "big/s1/large.go", Endings: 1}
	if err := os.WriteFile(filepath.Join(fs.root, ".gitignore"), []byte("big/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6 && s.Status == "running"; i++ {
		if err := step(1000); err != nil {
			t.Fatalf("step %d after the rule: %v", i, err)
		}
	}
	if s.Status != "done" || s.File != nil || s.Excluded["gitignore"] < 1 {
		t.Fatalf("precondition: the scan is %q with plan %+v, excluded %v; want it done with large.go closed and big left out", s.Status, s.File, s.Excluded)
	}
	if att.a != (Attempt{}) {
		t.Errorf("the attempt record still names %s with %d endings after the scan closed that file and left it; want no record", att.a.Ref, att.a.Endings)
	}
}

func TestR4QAFolderThatCannotBeReadWhileAFileIsPartJudgedLeavesAJournalTheReportReads(t *testing.T) {
	fs, s, m, step := r4qPartJudged(t)
	fs.failBig = 1
	err := step(1000)
	if err == nil || fs.failBig != 0 || s.File == nil || s.Status != "running" || !strings.Contains(s.LastError, "big: ") {
		t.Fatalf("precondition: the step returned %v with the scan %q, plan %+v, last error %q; want the step's error, the scan running with the plan kept and the folder named", err, s.Status, s.File, s.LastError)
	}
	t.Logf("the failed step: %v; last error %q", err, s.LastError)
	for i := 0; i < 20 && s.Status == "running"; i++ {
		if err := step(1000); err != nil {
			t.Fatalf("step %d after the folder could be read again: %v", i, err)
		}
	}
	if s.Status != "done" || s.File != nil || s.FilesJudged < 3 {
		t.Fatalf("precondition: the scan is %q with plan %+v and %d files judged; want it done", s.Status, s.File, s.FilesJudged)
	}
	t.Logf("done with %d files judged", s.FilesJudged)
	if err := foldOf(m); err != nil {
		t.Fatalf("the journal of a scan that could not read a folder once while a file under it was part judged is not one the report reads: %v", err)
	}
	runs, _ := runsOf(m)
	if n := len(runs["big/s1/large.go"]); n != 1 {
		t.Errorf("the journal holds %d runs of large.go; want one", n)
	}
}

func TestR4QAPlanOfAFileTheScanIsNotOnGetsNoRow(t *testing.T) {
	fs, s, m, step := r4qPartJudged(t)
	stale := *s.File
	stale.Ref = "gone/old.go"
	s.File = &stale
	if err := os.WriteFile(filepath.Join(fs.root, ".gitignore"), []byte("big/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6 && s.Status == "running"; i++ {
		if err := step(1000); err != nil {
			t.Fatalf("step %d after the rule: %v", i, err)
		}
	}
	if s.Status != "done" || s.Excluded["gitignore"] < 1 {
		t.Fatalf("precondition: the scan is %q, excluded %v; want it done with big left out", s.Status, s.Excluded)
	}
	if s.File != nil {
		t.Errorf("the scan's state still carries the plan of %s", s.File.Ref)
	}
	if _, err := (&Journal{Store: m, Path: "journal"}).WalkAt(0, func(e JournalEvent, _, _ int64) error {
		if e.Coverage != nil && e.Coverage.Ref == "gone/old.go" {
			t.Errorf("the journal holds a row for %s, a file the scan was not on: %+v", e.Coverage.Ref, *e.Coverage)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestR4QOnlyAClosingRowSayingAllItSaidMayComeBack(t *testing.T) {
	left := func(done int, digest, why string) Coverage {
		return Coverage{Ref: "big/s1/large.go", SourceDigest: digest, Kind: "file", Disposition: "excluded", Reasons: []string{why}, Size: 1 << 20, ExpectedChunks: 38, CompletedChunks: done}
	}
	a := left(7, "d1", "gitignore")
	big := Coverage{Ref: "big", Kind: "subtree", Disposition: "excluded", Reasons: []string{"gitignore", "children_not_enumerated"}}
	z := Coverage{Ref: "zz/z.go", Kind: "file", Disposition: "unknown", Reasons: []string{"unreadable"}}
	bare := Coverage{Ref: "big/s1/large.go", Kind: "file", Disposition: "excluded", Reasons: []string{"gitignore"}, Size: 1 << 20}
	for _, c := range []struct {
		name        string
		rows        []Coverage
		interleaved bool
		runs        int
	}{
		{"the closing row and the folder's written twice", []Coverage{a, big, a, big, z}, false, 3},
		{"the closing row written twice in a row", []Coverage{a, a, big, z}, false, 3},
		{"the closing row written three times", []Coverage{a, big, a, a, big, z}, false, 3},
		{"a closing row that comes back saying more windows were done", []Coverage{a, big, left(8, "d1", "gitignore")}, true, 0},
		{"a closing row that comes back under another digest", []Coverage{a, big, left(7, "d2", "gitignore")}, true, 0},
		{"a closing row that comes back with another reason", []Coverage{a, big, left(7, "d1", "caller_exclude")}, true, 0},
		{"a closing row that comes back with no digest", []Coverage{a, big, bare}, true, 0},
		{"a row with no digest that comes back as a closing row", []Coverage{bare, big, a}, true, 0},
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
				if len(rs) != 1 || len(rs[0].Coverage) != 1 {
					t.Errorf("%s is read as %d runs, the first with %d rows; want one run of one row", ref, len(rs), len(rs[0].Coverage))
				}
			}
			if n != c.runs {
				t.Errorf("%d runs; want %d", n, c.runs)
			}
		})
	}
}
