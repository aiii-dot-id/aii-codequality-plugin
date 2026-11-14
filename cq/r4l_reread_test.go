package cq

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func r4lFile(ref string) []JournalEvent {
	r := reviewFixture(ref, "EH-01")
	r.Battery = "code"
	c := completeFixture(r)
	return []JournalEvent{{Record: &r}, {Coverage: &c}}
}

func r4lRow(c Coverage) JournalEvent { return JournalEvent{Coverage: &c} }

func r4lPend(ref, why string, size int64) JournalEvent {
	return r4lRow(Coverage{Ref: ref, Kind: "file", Disposition: "pending", Reasons: []string{why}, Size: size})
}

func r4lSub(ref, why string) JournalEvent {
	return r4lRow(Coverage{Ref: ref, Kind: "subtree", Disposition: "unknown", Reasons: []string{why, "children_not_enumerated"}})
}

func r4lJoin(parts ...[]JournalEvent) []JournalEvent {
	var out []JournalEvent
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestR4LARunReadAgainIsTheRunTheFoldRead(t *testing.T) {
	const fb, eb = "file_budget", "examined_budget"
	one := func(e JournalEvent) []JournalEvent { return []JournalEvent{e} }
	for _, c := range []struct {
		name string
		ev   []JournalEvent
		runs int
	}{
		{"a folder boundary comes back inside a judged file's run",
			r4lJoin(one(r4lSub("pkg/gone", "unreadable")), r4lFile("pkg/a.go"), one(r4lSub("pkg/gone", "unreadable")), r4lFile("pkg/b.go")), 3},
		{"a judged file's budget marker is written twice with its run open",
			r4lJoin(r4lFile("pkg/a.go")[:1], one(r4lPend("pkg/a.go", eb, 7)), one(r4lPend("pkg/a.go", eb, 7))), 1},
		{"a budget batch is written twice",
			[]JournalEvent{r4lPend("b.go", fb, 7), r4lPend("c.go", fb, 9), r4lSub("s", fb), r4lPend("b.go", fb, 7), r4lPend("c.go", fb, 9), r4lSub("s", fb)}, 3},
		{"a listing's boundaries are written twice",
			[]JournalEvent{r4lSub("l1", "symlink"), r4lSub("l2", "symlink"), r4lSub("l1", "symlink"), r4lSub("l2", "symlink")}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			j := journalOf(t, c.ev)
			var runs []FileRun
			if _, err := FoldJournal(j, func(r *FileRun) error {
				runs = append(runs, *r)
				return nil
			}); err != nil {
				t.Fatalf("the fold refuses the journal: %v", err)
			}
			if len(runs) != c.runs {
				t.Fatalf("the fold reads %d runs; want %d", len(runs), c.runs)
			}

			skipped := 0
			for _, r := range runs {
				skipped += r.Events - len(r.Records) - len(r.Coverage)
			}
			if skipped == 0 {
				t.Fatal("no run's span holds a row the fold left out, so this test shows nothing")
			}
			for _, r := range runs {
				again, err := ReadRun(j, r.Start, r.Events)
				if err != nil {
					t.Errorf("%s: read again with %v", r.Ref, err)
					continue
				}
				if again.Ref != r.Ref {
					t.Errorf("the run of %s is read again as the run of %s", r.Ref, again.Ref)
				}
				if again.Events != r.Events {
					t.Errorf("%s: %d events read again; the fold counted %d", r.Ref, again.Events, r.Events)
				}
				if !reflect.DeepEqual(again.Records, r.Records) {
					t.Errorf("%s: %d records read again; the fold read %d, or they differ", r.Ref, len(again.Records), len(r.Records))
				}
				if !reflect.DeepEqual(again.Coverage, r.Coverage) {
					t.Errorf("%s: coverage read again is %+v; the fold read %+v", r.Ref, again.Coverage, r.Coverage)
				}
			}
		})
	}
}

func TestR4LAPageAfterABoundaryCameBackIsThePageWithoutIt(t *testing.T) {
	gone := []JournalEvent{r4lSub("pkg/gone", "unreadable")}
	plain := r4lJoin(gone, r4lFile("pkg/a.go"), r4lFile("pkg/b.go"))
	replay := r4lJoin(gone, r4lFile("pkg/a.go"), gone, r4lFile("pkg/b.go"))
	page := func(name string, ev []JournalEvent) ReviewPage {
		p, err := ReviewFold(journalOf(t, ev), ReviewQuery{Shape: "review_queue", Tier: TierAll, Generation: "g", PageBytes: 200000, Limit: 100, ScanDone: true,
			IntentUnread: "the side file could not be read"})
		if err != nil {
			t.Fatalf("the page of the %s journal: %v", name, err)
		}
		return p
	}
	want := page("plain", plain)

	if n := bytes.Count(want.Entries, []byte(IntentNoneUnreadable)); n != 2 || bytes.Contains(want.Entries, []byte(IntentNoneNotAsked)) {
		t.Fatalf("the plain page says %q %d times; want it once for each of the two files and %q nowhere, or this test shows nothing:\n%s", IntentNoneUnreadable, n, IntentNoneNotAsked, want.Entries)
	}
	got := page("replayed", replay)
	if !bytes.Equal(got.Entries, want.Entries) || got.Total != want.Total {
		t.Fatalf("the page after the boundary came back is not the page without it (%d files against %d):\n%s\n%s", got.Total, want.Total, got.Entries, want.Entries)
	}
}

func TestR4LARunReadAgainRefusesASpanTheFoldDidNotMake(t *testing.T) {
	j := journalOf(t, r4lJoin(r4lFile("pkg/a.go"), r4lFile("pkg/b.go")))
	run, err := ReadRun(j, 0, 2)
	if err != nil || run.Ref != "pkg/a.go" || len(run.Records) != 1 || len(run.Coverage) != 1 {
		t.Fatalf("the run of pkg/a.go is read as %+v, %v", run, err)
	}
	if run, err := ReadRun(j, 0, 3); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("a span that runs into the next file's record is read as %+v, %v; want %v", run, err, ErrJournalCorrupt)
	}
}
