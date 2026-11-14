package cq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memAttempts struct {
	a             Attempt
	saves, clears int
}

func (m *memAttempts) Load() (Attempt, error) { return m.a, nil }
func (m *memAttempts) Save(a Attempt) error   { m.a = a; m.saves++; return nil }
func (m *memAttempts) Clear() error           { m.a = Attempt{}; m.clears++; return nil }

type workerEnded struct{}

type endingJudge struct {
	Judge
	mark    string
	endings int
}

func (e *endingJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	if code, _ := state["code"].(string); strings.Contains(code, e.mark) {
		e.endings++
		panic(workerEnded{})
	}
	return e.Judge.Ask(state, qs)
}

func stepAsTheHostDoes(t *testing.T, dir string, j Judge, at AttemptStore, calls, most int, newStep func()) (*Scan, *memStore, int, bool) {
	t.Helper()
	return stepsAsTheHost(t, hostRun{dir: dir, judge: j, attempts: at, calls: calls, most: most, newStep: newStep})
}

func coverageOf(t *testing.T, m *memStore, ref string) (Coverage, bool) {
	t.Helper()
	var c struct {
		Entries []Coverage `json:"entries"`
	}
	if err := json.Unmarshal([]byte(reviewJSON(t, m, ReviewQuery{Shape: "coverage"})), &c); err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Entries {
		if e.Ref == ref {
			return e, true
		}
	}
	return Coverage{}, false
}

const endsTheWorker = "package p\n\n// EndsTheWorker is the function no step gets through.\nfunc EndsTheWorker(x int) int {\n\treturn x + 1\n}\n"

func goodFile(name string) string {
	return "package p\n\n// " + name + " returns x doubled.\nfunc " + name + "(x int) int {\n\treturn x * 2\n}\n"
}

func TestR4AFileTheWorkerIsEndedOnIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"a.go": goodFile("A"), "b.go": endsTheWorker, "c.go": goodFile("C")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	judge := func() *endingJudge {
		return &endingJudge{Judge: &plantJudge{seen: map[string]bool{}}, mark: "func EndsTheWorker"}
	}

	control := judge()
	if _, _, ended, stopped := stepAsTheHostDoes(t, dir, control, nil, 1000, 12, nil); stopped || ended != 12 {
		t.Fatalf("control: with no attempt record the scan stopped=%v after %d endings; want it ended at every one of 12 steps", stopped, ended)
	}

	j, at := judge(), &memAttempts{}
	s, m, ended, stopped := stepAsTheHostDoes(t, dir, j, at, 1000, 12, nil)
	if !stopped || s.Status != "done" {
		t.Fatalf("the scan did not end done: stopped=%v status=%q after %d endings", stopped, s.Status, ended)
	}
	if ended != MaxAttempts || j.endings != MaxAttempts {
		t.Errorf("the worker was ended %d times (the judge saw %d); want %d", ended, j.endings, MaxAttempts)
	}
	if len(s.NotFinished) != 1 || s.NotFinished[0] != "b.go" || s.Excluded[NotFinished] != 1 {
		t.Errorf("named %v, counted %d; want b.go, 1", s.NotFinished, s.Excluded[NotFinished])
	}
	if s.FilesJudged != 2 || s.FilesSeen != 3 {
		t.Errorf("judged %d of %d seen; want 2 of 3", s.FilesJudged, s.FilesSeen)
	}
	c, ok := coverageOf(t, m, "b.go")
	if !ok || c.Disposition != "unknown" || !contains(c.Reasons, NotFinished) {
		t.Errorf("coverage of b.go: %+v (found %v); want unknown, %s", c, ok, NotFinished)
	}
	seen := j.Judge.(*plantJudge).seen
	if !seen["A(x"] || !seen["C(x"] {
		t.Errorf("the files on either side were not both judged: %v", seen)
	}
	if at.a != (Attempt{}) {
		t.Errorf("the record is left at %+v after the scan ended", at.a)
	}
}

func TestR4ALargeFileEndedOnPartJudgedIsRefusedAndItsCursorDropped(t *testing.T) {
	dir := t.TempDir()
	src, _ := goOfSize(MaxSourceBytes)
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12}
	j, at := &endingJudge{Judge: inner, mark: "func Planted"}, &memAttempts{}
	s, m, ended, stopped := stepAsTheHostDoes(t, dir, j, at, 12, 400, inner.newStep)
	if !stopped || s.Status != "done" {
		t.Fatalf("the scan did not end done: stopped=%v after %d endings", stopped, ended)
	}
	if ended != MaxAttempts {
		t.Errorf("the worker was ended %d times; want %d", ended, MaxAttempts)
	}
	if len(inner.plantJudge.seen) < 100 {
		t.Errorf("only %d functions were judged before the ending: the file was not part judged over steps", len(inner.plantJudge.seen))
	}
	if s.File != nil || len(s.NotFinished) != 1 || s.NotFinished[0] != "big.go" || s.Excluded[NotFinished] != 1 {
		t.Errorf("cursor %+v, named %v, counted %d", s.File, s.NotFinished, s.Excluded[NotFinished])
	}
	c, ok := coverageOf(t, m, "big.go")
	if !ok || c.Disposition != "unknown" || !contains(c.Reasons, NotFinished) {
		t.Errorf("coverage of big.go: %+v (found %v); want unknown, %s", c, ok, NotFinished)
	}
}
