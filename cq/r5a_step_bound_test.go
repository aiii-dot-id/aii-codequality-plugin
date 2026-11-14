package cq

import (
	"encoding/json"
	"fmt"
	"testing"
)

const r5aStepFiles = StepFiles

const r5aCeiling = 64

type r5aJudge struct {
	Judge
	live int
}

func (c *r5aJudge) Ask(st, qs map[string]any) (map[string]float64, error) {
	c.live++
	return c.Judge.Ask(st, qs)
}

func (c *r5aJudge) Choose(st, qs map[string]any) (map[string]Choice, error) {
	c.live++
	return c.Judge.Choose(st, qs)
}

func r5aRun(t *testing.T, dir string, j Judge, c Cache, maxCalls, maxExamined, steps int) (s *Scan, m *memStore, saves, firstSeen int) {
	t.Helper()
	fs := &OSFS{Root: dir}
	m = newMemStore()
	m.files["journal"], m.files["intent"], m.files["cp0"], m.files["cp1"] = nil, nil, nil, nil
	pair := CheckpointPair{Store: m, Paths: [2]string{"cp0", "cp1"}}
	first, err := NewScan(fs, "s", ".", "", nil, nil, false, 1000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	at := &memAttempts{}
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
		if s.Status != "running" || i == steps {
			return s, m, saves, firstSeen
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
			saves++
			return pair.Save(b)
		}
		s.Checkpoint = save
		if _, err := s.Step(table(t), fs, j, c, maxCalls, maxExamined); err != nil {
			t.Fatal(err)
		}
		if err := save(); err != nil {
			t.Fatal(err)
		}
		if err := s.Settle(); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstSeen = s.FilesSeen
		}
	}
}

func r5aCoverage(t *testing.T, m *memStore) map[string]string {
	t.Helper()
	out := map[string]string{}
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(run *FileRun) error {
		if _, twice := out[run.Ref]; twice {
			return fmt.Errorf("%s has two runs", run.Ref)
		}
		last := Coverage{}
		if n := len(run.Coverage); n > 0 {
			last = run.Coverage[n-1]
		}
		b, err := json.Marshal(last)
		if err != nil {
			return err
		}
		out[run.Ref] = fmt.Sprintf("%d records, last row %s", len(run.Records), b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func r5aDiffer(want, got map[string]string) (named []string, n int) {
	add := func(s string) {
		if n++; len(named) < 5 {
			named = append(named, s)
		}
	}
	for ref, w := range want {
		if g, ok := got[ref]; !ok {
			add(ref + ": missing")
		} else if g != w {
			add(ref + ": " + g + "; want " + w)
		}
	}
	for ref := range got {
		if _, ok := want[ref]; !ok {
			add(ref + ": not in the filling scan")
		}
	}
	return named, n
}

func TestR5AStepOverCachedAnswersEndsAtTheStepBound(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 150; i++ {
		files[fmt.Sprintf("d%02d/f%03d.go", i/30, i)] = goodFile(fmt.Sprintf("F%03d", i))
		files[fmt.Sprintf("d%02d/f%03d_test.go", i/30, i)] = goFile
	}
	writeFiles(t, dir, files)
	cache := MemCache{}

	fill := &r5aJudge{Judge: &plantJudge{seen: map[string]bool{}}}
	filled, filledStore, _, _ := r5aRun(t, dir, fill, cache, 1000, 400, 40)
	if filled.Status != "done" || filled.FilesJudged != 150 || fill.live == 0 {
		t.Fatalf("precondition: the filling scan ended %q with %d files judged and %d live calls; want done, 150, some",
			filled.Status, filled.FilesJudged, fill.live)
	}
	want := r5aCoverage(t, filledStore)
	if len(want) < 300 {
		t.Fatalf("precondition: the filling scan left coverage for %d refs; want the 300 files at least", len(want))
	}
	t.Logf("compared for %d refs, such as d00/f000.go: %s; d00/f000_test.go: %s", len(want), want["d00/f000.go"], want["d00/f000_test.go"])

	again := &r5aJudge{Judge: &plantJudge{seen: map[string]bool{}}}
	one, _, saves, _ := r5aRun(t, dir, again, cache, 12, r5aStepFiles, 1)
	if again.live != 0 {
		t.Fatalf("precondition: %d calls reached the judge in a step over answers already kept; want none", again.live)
	}
	if one.FilesSeen > r5aCeiling {
		t.Fatalf("one step with no live call examined %d files (%d judged) and saved the scan %d times; want at most %d examined",
			one.FilesSeen, one.FilesJudged, saves, r5aCeiling)
	}
	if one.FilesSeen > r5aStepFiles || saves > 2*r5aStepFiles+2 {
		t.Fatalf("one step examined %d files and saved %d times; its bound is %d files", one.FilesSeen, saves, r5aStepFiles)
	}

	whole := &r5aJudge{Judge: &plantJudge{seen: map[string]bool{}}}
	done, doneStore, _, firstSeen := r5aRun(t, dir, whole, cache, 12, r5aStepFiles, 300/r5aStepFiles+20)
	if done.Status != "done" || done.FilesJudged != 150 || done.FilesSeen != 300 || whole.live != 0 {
		t.Fatalf("the second scan ended %q with %d judged of %d seen and %d live calls (first step saw %d); want done, 150 of 300, none",
			done.Status, done.FilesJudged, done.FilesSeen, whole.live, firstSeen)
	}
	if named, n := r5aDiffer(want, r5aCoverage(t, doneStore)); n != 0 {
		t.Fatalf("the bounded scan's durable coverage differs from the filling scan's at %d of %d refs: %v", n, len(want), named)
	}

	short := &r5aJudge{Judge: &plantJudge{seen: map[string]bool{}}}
	stopped, stoppedStore, _, _ := r5aRun(t, dir, short, cache, 12, r5aStepFiles, 2)
	if _, n := r5aDiffer(want, r5aCoverage(t, stoppedStore)); stopped.Status != "running" || n == 0 {
		t.Fatalf("control: a scan stopped after two steps is %q and differs from the filling scan at %d refs; want running and some",
			stopped.Status, n)
	}
}
