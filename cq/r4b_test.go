package cq

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type hostRun struct {
	dir          string
	fs           FS
	judge        Judge
	attempts     AttemptStore
	calls, most  int
	maxFiles     int
	newStep      func()
	noCheckpoint bool

	endInSave func(*Scan) bool
}

func stepsAsTheHost(t *testing.T, run hostRun) (*Scan, *memStore, int, bool) {
	t.Helper()
	fs := run.fs
	if fs == nil {
		fs = &OSFS{Root: run.dir}
	}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	maxFiles := run.maxFiles
	if maxFiles == 0 {
		maxFiles = 100
	}
	first, err := NewScan(&OSFS{Root: run.dir}, "s", ".", "", nil, nil, false, maxFiles, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(first)
	ended := 0
	for i := 0; i <= run.most; i++ {
		var s Scan
		if err := json.Unmarshal(saved, &s); err != nil {
			t.Fatal(err)
		}
		if s.Status != "running" && s.Status != "judge_unavailable" {
			return &s, m, ended, true
		}
		if i == run.most {
			return &s, m, ended, false
		}
		if run.newStep != nil {
			run.newStep()
		}
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"},
			Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		if run.attempts != nil {
			s.Attempts = run.attempts
		}
		if !run.noCheckpoint {
			s.Checkpoint = func() error { saved, _ = json.Marshal(&s); return nil }
		}
		died := func() (died bool) {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(workerEnded); !ok {
						panic(r)
					}
					died = true
				}
			}()
			if _, err := s.Step(table(t), fs, run.judge, nil, run.calls, 400); err != nil {
				t.Fatal(err)
			}
			return false
		}()
		if died {
			ended++
			continue
		}
		if run.endInSave != nil && run.endInSave(&s) {
			ended++
			continue
		}
		saved, _ = json.Marshal(&s)
		if err := s.Settle(); err != nil {
			t.Fatal(err)
		}
	}
	return nil, m, ended, false
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func foldOf(m *memStore) error {
	_, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(*FileRun) error { return nil })
	return err
}

func TestR4AnEndedStepLeavesAJournalTheReportReads(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "aa.go": goodFile("AA"), "b.go": endsTheWorker, "c.go": goodFile("C")})
	judge := func() *endingJudge {
		return &endingJudge{Judge: &plantJudge{seen: map[string]bool{}}, mark: "func EndsTheWorker"}
	}

	_, cm, _, stopped := stepsAsTheHost(t, hostRun{dir: dir, judge: judge(), attempts: &memAttempts{}, calls: 1000, most: 12, noCheckpoint: true})
	if err := foldOf(cm); !stopped || !errors.Is(err, ErrJournalInterleaved) {
		t.Fatalf("control: with the place saved only at a step's end the scan stopped=%v and its journal folds with %v; want it stopped and %v", stopped, err, ErrJournalInterleaved)
	}

	j, at := judge(), &memAttempts{}
	s, m, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, judge: j, attempts: at, calls: 1000, most: 12})
	if !stopped || s.Status != "done" {
		t.Fatalf("the scan did not end done: stopped=%v after %d endings", stopped, ended)
	}
	if ended != MaxAttempts {
		t.Errorf("the worker was ended %d times; want %d", ended, MaxAttempts)
	}
	if err := foldOf(m); err != nil {
		t.Fatalf("the journal of a scan whose step was ended is not one the report reads: %v", err)
	}
	for _, ref := range []string{"a.go", "aa.go", "c.go"} {
		if c, ok := coverageOf(t, m, ref); !ok || c.Disposition == "unknown" || contains(c.Reasons, NotFinished) || c.CompletedChunks != 1 {
			t.Errorf("coverage of %s: %+v (found %v); want it judged and not given up", ref, c, ok)
		}
	}
	if c, ok := coverageOf(t, m, "b.go"); !ok || c.Disposition != "unknown" || !contains(c.Reasons, NotFinished) {
		t.Errorf("coverage of b.go: %+v (found %v); want unknown, %s", c, ok, NotFinished)
	}
	if s.FilesJudged != 3 || s.FilesSeen != 4 || len(s.NotFinished) != 1 || s.NotFinished[0] != "b.go" {
		t.Errorf("judged %d of %d seen, named %v; want 3 of 4, b.go", s.FilesJudged, s.FilesSeen, s.NotFinished)
	}
	if at.a != (Attempt{}) {
		t.Errorf("the record is left at %+v after the scan ended", at.a)
	}
}

type endingFS struct {
	*OSFS
	dir     string
	endings int
}

func (f *endingFS) List(dir string) ([]Entry, bool, error) {
	if dir == f.dir {
		f.endings++
		panic(workerEnded{})
	}
	return f.OSFS.List(dir)
}

func TestR4AnEndingInAListingIsNotCountedAgainstTheFileBefore(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "sub/x.go": goodFile("X")})
	fs := &endingFS{OSFS: &OSFS{Root: dir}, dir: "sub"}
	at := &memAttempts{}
	s, m, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, fs: fs, judge: &plantJudge{seen: map[string]bool{}}, attempts: at, calls: 1000, most: 8})
	if s == nil {
		t.Fatal("no scan was saved")
	}
	if stopped || ended != 8 || fs.endings != 8 {
		t.Errorf("stopped=%v after %d endings (%d in the listing); want the worker ended in the listing at each of 8 steps", stopped, ended, fs.endings)
	}
	if len(s.NotFinished) != 0 || s.Excluded[NotFinished] != 0 {
		t.Errorf("a.go finished and is given up: named %v, counted %d", s.NotFinished, s.Excluded[NotFinished])
	}
	if s.FilesJudged != 1 {
		t.Errorf("judged %d; want a.go judged once", s.FilesJudged)
	}
	if c, ok := coverageOf(t, m, "a.go"); !ok || c.Disposition == "unknown" || contains(c.Reasons, NotFinished) || c.CompletedChunks != 1 {
		t.Errorf("coverage of a.go: %+v (found %v); want it judged and not given up", c, ok)
	}
	if at.a != (Attempt{}) {
		t.Errorf("the record still names %+v after its file was finished and saved", at.a)
	}
}

func TestR4AFileGivenUpKeepsItsPackagesRulesOff(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"p/a.go": "package p\n\n// helper returns one.\nfunc helper() int {\n\treturn 1\n}\n",
		"p/b.go": "package p\n\n// EndsTheWorker is the function no step gets through.\nfunc EndsTheWorker(x int) int {\n\treturn x + helper()\n}\n",
	})
	j := &endingJudge{Judge: &plantJudge{seen: map[string]bool{}}, mark: "func EndsTheWorker"}
	s, m, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, judge: j, attempts: &memAttempts{}, calls: 1000, most: 12})
	if !stopped || s.Status != "done" || len(s.NotFinished) != 1 || s.NotFinished[0] != "p/b.go" {
		t.Fatalf("stopped=%v status=%q after %d endings, named %v; want done with p/b.go given up", stopped, s.Status, ended, s.NotFinished)
	}
	found, notes, _, err := PackageFindings(&Journal{Store: m, Path: "intent"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for ref, fs := range found {
		for _, f := range fs {
			if f.ID == "SL-UNUSEDFUNC" || f.ID == "SL-UNUSEDPARAM" {
				t.Errorf("%s emitted for %s in a package one file of which was given up", f.ID, ref)
			}
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Note, "b.go ("+NotFinished+")") {
		t.Errorf("notes: %+v; want one naming b.go (%s)", notes, NotFinished)
	}
}

func TestR4TheNamesOfFilesGivenUpAreBoundedByTheirSize(t *testing.T) {
	s := &Scan{Excluded: map[string]int{}}
	for i := 0; i < NotFinishedListed; i++ {
		name := strings.Repeat("&", 240) + strings.Repeat("d", 200) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go"
		if err := s.notFinished(Entry{Name: name, Size: 1}, name); err != nil {
			t.Fatal(err)
		}
	}
	b, err := json.Marshal(s.NotFinished)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > NamesBytes {
		t.Errorf("the names kept encode to %d bytes; the bound is %d", len(b), NamesBytes)
	}
	if len(s.NotFinished) == 0 || len(s.NotFinished) >= NotFinishedListed {
		t.Errorf("%d names kept of %d; want some and not all", len(s.NotFinished), NotFinishedListed)
	}
	if s.Excluded[NotFinished] != NotFinishedListed || len(s.Observed) != NotFinishedListed {
		t.Errorf("counted %d, covered %d; want %d of each", s.Excluded[NotFinished], len(s.Observed), NotFinishedListed)
	}
}
