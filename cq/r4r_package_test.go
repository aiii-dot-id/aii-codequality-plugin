package cq

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	r4rHelper = "package p\n\n// helper returns one.\nfunc helper() int {\n\treturn 1\n}\n"
	r4rCaller = "package p\n\n// %s returns helper's one.\nfunc %s() int {\n\treturn helper()\n}\n"
)

func r4rScan(t *testing.T, files map[string]string) (dir string, s *Scan, m *memStore, step func(calls, most int) error) {
	t.Helper()
	dir = t.TempDir()
	writeFiles(t, dir, files)
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
	step = func(calls, most int) error {
		t.Helper()
		judge.left = calls
		if _, err := s.Step(table(t), fs, judge, nil, 1000, most); err != nil {
			return err
		}
		return s.Settle()
	}
	return dir, s, m, step
}

func r4rMarks(t *testing.T, m *memStore) (marks []string) {
	t.Helper()
	if _, err := (&Journal{Store: m, Path: "intent"}).Walk(0, func(e JournalEvent) error {
		if r := e.Record; r != nil && r.Shape != nil && r.Shape.Skipped != "" {
			marks = append(marks, r.Ref+" ("+r.Shape.Skipped+")")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return marks
}

func r4rPackageOff(t *testing.T, m *memStore, want ...string) {
	t.Helper()
	found, notes, _, err := PackageFindings(&Journal{Store: m, Path: "intent"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for ref, fs := range found {
		for _, f := range fs {
			if strings.HasPrefix(ref, "pkg/") && (f.ID == "SL-UNUSEDFUNC" || f.ID == "SL-UNUSEDPARAM") {
				t.Errorf("%s emitted for %s in %s (%s): the scan left the folder with Go files not read, and one of them calls it", f.ID, f.Func, ref, f.Detail)
			}
		}
	}
	t.Logf("notes: %+v", notes)
	n, note := 0, ""
	for _, p := range notes {
		if p.Dir == "pkg" {
			n, note = n+1, p.Note
		}
	}
	if n != 1 {
		t.Errorf("%d notes name pkg; want one saying its rules were not applied", n)
		return
	}
	for _, w := range want {
		if !strings.Contains(note, w) {
			t.Errorf("the note on pkg does not say %q: %s", w, note)
		}
	}
}

func TestR4RAFolderLeftPartWayKeepsItsPackagesRulesOff(t *testing.T) {
	files := map[string]string{"other/o.go": goodFile("O"), "pkg/a.go": r4rHelper}
	for i := 1; i <= 11; i++ {
		files[fmt.Sprintf("pkg/z%02d.go", i)] = goodFile(fmt.Sprintf("Z%02d", i))
	}
	files["pkg/z12.go"] = fmt.Sprintf(r4rCaller, "Z12", "Z12")
	dir, s, m, step := r4rScan(t, files)
	for i := 0; i < 20 && !(s.Dir == "pkg" && len(s.Pending) == 12); i++ {
		if err := step(1000, 1); err != nil {
			t.Fatal(err)
		}
	}
	if s.Dir != "pkg" || len(s.Pending) != 12 || s.Pending[0].Name != "z01.go" || s.FilesJudged != 2 || s.Status != "running" {
		t.Fatalf("precondition: the scan is %q in %q with %d files to assess and %d judged; want it running in pkg with a.go judged and z01.go to z12.go not read", s.Status, s.Dir, len(s.Pending), s.FilesJudged)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("pkg/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6 && s.Status == "running"; i++ {
		if err := step(1000, 400); err != nil {
			t.Fatalf("step %d after the rule: %v", i, err)
		}
	}
	if s.Status != "done" || s.Excluded["gitignore"] < 1 || s.FilesJudged != 2 {
		t.Fatalf("precondition: the scan is %q, excluded %v, %d files judged; want it done with pkg left out and no file of it read after the rule", s.Status, s.Excluded, s.FilesJudged)
	}
	marks := r4rMarks(t, m)
	t.Logf("marks: %v", marks)
	if len(marks) != PackageLeftListedForTest {
		t.Errorf("%d files of pkg are marked as left out; want %d of the twelve not read", len(marks), PackageLeftListedForTest)
	}
	r4rPackageOff(t, m, "z01.go (gitignore)", "z08.go (gitignore, and 4 more Go files of this folder not read)")
	if err := foldOf(m); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}

const PackageLeftListedForTest = 8

func TestR4RAFolderLeftWithAFilePartJudgedKeepsItsPackagesRulesOff(t *testing.T) {
	for _, c := range []struct {
		name string
		fail bool
	}{{"the step that leaves the folder writes", false}, {"the step that leaves the folder cannot write once", true}} {
		t.Run(c.name, func(t *testing.T) {
			large, _ := goOfSize(MaxSourceBytes)
			large = strings.Replace(large, "package big", "package p", 1)
			dir, s, m, step := r4rScan(t, map[string]string{"pkg/a.go": r4rHelper, "pkg/m_large.go": large, "pkg/z.go": fmt.Sprintf(r4rCaller, "Z", "Z"), "zz/y.go": goodFile("Y")})
			for i := 0; i < 60 && (s.File == nil || s.File.Next < 1); i++ {
				if err := step(4, 400); err != nil {
					t.Fatal(err)
				}
			}
			if s.File == nil || s.File.Ref != "pkg/m_large.go" || s.File.Next < 1 || s.File.Next >= len(s.File.Segs) || s.Dir != "pkg" || len(s.Pending) != 2 || s.Status != "running" {
				t.Fatalf("precondition: the scan is %q in %q with %d files to assess and plan %+v; want it running in pkg with m_large.go part judged and z.go not read", s.Status, s.Dir, len(s.Pending), s.File)
			}
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("pkg/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.fail {
				m.failWrites = true
				err := step(1000, 400)
				m.failWrites = false
				if err == nil || s.File == nil || len(s.Pending) != 2 || s.Status != "running" || s.Excluded["gitignore"] != 0 {
					t.Fatalf("precondition: the step that could not write returned %v with the scan %q, %d files to assess, plan %+v, excluded %v; want its error and the scan where it was", err, s.Status, len(s.Pending), s.File, s.Excluded)
				}
				t.Logf("the step that could not write: %v", err)
			}
			for i := 0; i < 6 && s.Status == "running"; i++ {
				if err := step(1000, 400); err != nil {
					t.Fatalf("step %d after the rule: %v", i, err)
				}
			}
			if s.Status != "done" || s.File != nil || s.Excluded["gitignore"] != 1 || s.FilesJudged != 2 {
				t.Fatalf("precondition: the scan is %q with plan %+v, excluded %v, %d files judged; want it done with m_large.go closed, pkg left out once, and a.go and zz/y.go judged", s.Status, s.File, s.Excluded, s.FilesJudged)
			}
			t.Logf("marks: %v", r4rMarks(t, m))
			r4rPackageOff(t, m, "m_large.go (gitignore)", "z.go (gitignore)")
			if err := foldOf(m); err != nil {
				t.Errorf("the journal is not one the report reads: %v", err)
			}
			runs, err := runsOf(m)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(runs["pkg/m_large.go"]); n != 1 {
				t.Errorf("the journal holds %d runs of m_large.go; want one", n)
			}
		})
	}
}
