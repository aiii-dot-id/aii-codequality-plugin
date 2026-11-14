package cq

import "testing"

func TestR4TheRecordCountsEndingsAndKeepsThem(t *testing.T) {
	at := &memAttempts{a: Attempt{Ref: "b.go", Live: true}}
	a, _ := loadAttempts(at)
	if at.a != (Attempt{Ref: "b.go", Endings: 1}) {
		t.Fatalf("a live record found at a step's start became %+v; want one ending, not live", at.a)
	}
	if give, _ := a.begin("b.go"); give || at.a != (Attempt{Ref: "b.go", Endings: 1, Live: true}) {
		t.Errorf("b.go after one ending: give up %v, record %+v", give, at.a)
	}
	if err := a.settle(); err != nil || at.a != (Attempt{Ref: "b.go", Endings: 1}) {
		t.Errorf("a step that returned with b.go unfinished left %+v (%v); want its one ending kept", at.a, err)
	}

	a, _ = loadAttempts(at)
	if at.a != (Attempt{Ref: "b.go", Endings: 1}) {
		t.Errorf("a record that was not live was counted: %+v", at.a)
	}
	a.begin("b.go")
	a, _ = loadAttempts(at)
	if give, _ := a.begin("b.go"); !give {
		t.Errorf("b.go after %d endings in total was not given up: %+v", MaxAttempts, at.a)
	}
	a.finished()
	if err := a.done(); err != nil || at.a != (Attempt{}) {
		t.Errorf("done left %+v (%v)", at.a, err)
	}

	at = &memAttempts{}
	a, _ = loadAttempts(at)
	a.begin("a.go")
	if err := a.settle(); err != nil || at.a != (Attempt{}) {
		t.Errorf("a file with no ending left a record: %+v (%v)", at.a, err)
	}

	at = &memAttempts{a: Attempt{Ref: "c.go", Endings: 1}}
	a, _ = loadAttempts(at)
	if give, _ := a.begin("a.go"); give || at.a != (Attempt{Ref: "a.go", Live: true}) {
		t.Errorf("a.go on c.go's record: give up %v, record %+v", give, at.a)
	}
	a.finished()
	if err := a.settle(); err != nil || at.a != (Attempt{Ref: "c.go", Endings: 1}) {
		t.Errorf("a step that stopped before c.go left %+v (%v); want c.go's record as found", at.a, err)
	}

	a, _ = loadAttempts(nil)
	if give, err := a.begin("a.go"); give || err != nil || a.settle() != nil || a.done() != nil {
		t.Error("a scan with no attempt store must step as before")
	}
}
