package cq

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func r7mLink(t *testing.T, dir, name, src string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "target-of-"+name)
	if err := os.WriteFile(target, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "pkg", name)); err != nil {
		t.Skipf("this file system makes no symlink: %v", err)
	}
}

func r7mDone(t *testing.T, s *Scan, step func(calls, most int) error) {
	t.Helper()
	for i := 0; i < 20 && s.Status == "running"; i++ {
		if err := step(1000, 400); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
}

func TestR7MAGoFileThatIsASymlinkKeepsItsPackagesRulesOff(t *testing.T) {
	dir, s, m, step := r4rScan(t, map[string]string{"other/o.go": goodFile("O"), "pkg/a.go": r4rHelper})
	r7mLink(t, dir, "z.go", fmt.Sprintf(r4rCaller, "Z", "Z"))
	r7mLink(t, dir, "z_test.go", "package p\n")
	r7mLink(t, dir, "notes.txt", "notes\n")
	r7mDone(t, s, step)
	if s.Status != "done" || s.Excluded["symlink"] != 3 || s.FilesJudged != 2 {
		t.Fatalf("precondition: the scan is %q, excluded %v, %d files judged; want it done with the three symlinks of pkg left out and a.go and other/o.go judged", s.Status, s.Excluded, s.FilesJudged)
	}
	marks := r4rMarks(t, m)
	t.Logf("marks: %v", marks)
	if len(marks) != 1 || marks[0] != "pkg/z.go (symlink)" {
		t.Errorf("the marks of files left out are %v; want pkg/z.go (symlink) and no other", marks)
	}
	r4rPackageOff(t, m, "z.go (symlink)")
	if err := foldOf(m); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}

func TestR7MSymlinkedGoFilesAreMarkedUpToTheBound(t *testing.T) {
	dir, s, m, step := r4rScan(t, map[string]string{"other/o.go": goodFile("O"), "pkg/a.go": r4rHelper})
	for i := 1; i <= 9; i++ {
		r7mLink(t, dir, fmt.Sprintf("z%02d.go", i), goodFile(fmt.Sprintf("Z%02d", i)))
	}
	r7mLink(t, dir, "z10.go", fmt.Sprintf(r4rCaller, "Z10", "Z10"))
	r7mDone(t, s, step)
	if s.Status != "done" || s.Excluded["symlink"] != 10 || s.FilesJudged != 2 {
		t.Fatalf("precondition: the scan is %q, excluded %v, %d files judged; want it done with the ten symlinks of pkg left out and a.go and other/o.go judged", s.Status, s.Excluded, s.FilesJudged)
	}
	marks := r4rMarks(t, m)
	t.Logf("marks: %v", marks)
	if len(marks) != PackageLeftListedForTest {
		t.Errorf("%d files of pkg are marked as left out; want %d of the ten symlinks", len(marks), PackageLeftListedForTest)
	}
	r4rPackageOff(t, m, "z01.go (symlink)", "z08.go (symlink, and 2 more Go files of this folder not read)")
	if err := foldOf(m); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
}
