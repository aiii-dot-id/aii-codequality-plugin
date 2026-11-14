package cq

import (
	"strings"
	"testing"
)

func TestR4AnEndingInTheSaveAfterAStepIsCounted(t *testing.T) {
	dir := t.TempDir()
	src, _ := goOfSize(MaxSourceBytes)
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A"), "big.go": src, "c.go": goodFile("C")})
	inner := &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12}
	at := &memAttempts{}
	s, m, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, judge: inner, attempts: at, calls: 12, most: 12, newStep: inner.newStep,
		endInSave: func(s *Scan) bool { return s.File != nil && s.File.Ref == "big.go" }})
	if !stopped {
		t.Fatalf("the scan was still running after 12 steps, %d of them ended in the save after a step: those endings were not counted", ended)
	}
	if s.Status != "done" || ended != MaxAttempts {
		t.Fatalf("status %q after %d endings; want done after %d", s.Status, ended, MaxAttempts)
	}
	if len(s.NotFinished) != 1 || s.NotFinished[0] != "big.go" || s.Excluded[NotFinished] != 1 || s.File != nil {
		t.Errorf("named %v, counted %d, cursor %+v; want big.go, 1, none", s.NotFinished, s.Excluded[NotFinished], s.File)
	}
	if c, ok := coverageOf(t, m, "big.go"); !ok || c.Disposition != "unknown" || !contains(c.Reasons, NotFinished) {
		t.Errorf("coverage of big.go: %+v (found %v); want unknown, %s", c, ok, NotFinished)
	}
	if !inner.seen["A(x"] || !inner.seen["C(x"] {
		t.Errorf("the files on either side were not both judged: A %v, C %v", inner.seen["A(x"], inner.seen["C(x"])
	}
	if err := foldOf(m); err != nil {
		t.Errorf("the report cannot read the journal: %v", err)
	}
	if at.a != (Attempt{}) {
		t.Errorf("the record is left at %+v after the scan ended", at.a)
	}
}

type turnsJudge struct {
	*cappedJudge
	step, endings int
}

func (j *turnsJudge) newStep() { j.step++; j.cappedJudge.newStep() }
func (j *turnsJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	if code, _ := state["code"].(string); j.step%2 == 1 && strings.Contains(code, "func F") {
		j.endings++
		panic(workerEnded{})
	}
	return j.cappedJudge.Ask(state, qs)
}

func TestR4EndingsOnOneFileAreCountedInTotal(t *testing.T) {
	dir := t.TempDir()
	src, _ := goOfSize(MaxSourceBytes)
	writeFiles(t, dir, map[string]string{"big.go": src, "z.go": goodFile("Z")})
	j := &turnsJudge{cappedJudge: &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12}}
	at := &memAttempts{}
	s, _, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, judge: j, attempts: at, calls: 12, most: 40, newStep: j.newStep})
	if ended > MaxAttempts {
		t.Fatalf("the worker was ended %d times on one file (stopped=%v); want %d in total", ended, stopped, MaxAttempts)
	}
	if !stopped || s.Status != "done" || ended != MaxAttempts || j.endings != MaxAttempts {
		t.Fatalf("stopped=%v status %q after %d endings (the judge saw %d); want done after %d", stopped, s.Status, ended, j.endings, MaxAttempts)
	}
	if len(s.NotFinished) != 1 || s.NotFinished[0] != "big.go" || s.Excluded[NotFinished] != 1 || s.File != nil {
		t.Errorf("named %v, counted %d, cursor %+v; want big.go, 1, none", s.NotFinished, s.Excluded[NotFinished], s.File)
	}
	if len(j.seen) < 5 || !j.seen["Z(x"] {
		t.Errorf("%d functions judged, z.go judged %v: want part of big.go judged between its endings, and the file after it", len(j.seen), j.seen["Z(x"])
	}
}
