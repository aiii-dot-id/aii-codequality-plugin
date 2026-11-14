package cq

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type endsOnceFS struct {
	*OSFS
	dir           string
	endings, most int
}

func (f *endsOnceFS) List(dir string) ([]Entry, bool, error) {
	if dir == f.dir && f.endings < f.most {
		f.endings++
		panic(workerEnded{})
	}
	return f.OSFS.List(dir)
}

func runsOf(m *memStore) (map[string][]FileRun, error) {
	runs := map[string][]FileRun{}
	_, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(r *FileRun) error {
		runs[r.Ref] = append(runs[r.Ref], *r)
		return nil
	})
	return runs, err
}

func TestR4FAnEndingAfterAListingsBoundariesLeavesAJournalTheReportReads(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"sub/x.go": goodFile("X")})
	for _, l := range []string{"l1", "l2"} {
		if err := os.Symlink("sub", filepath.Join(dir, l)); err != nil {
			t.Fatal(err)
		}
	}
	fs := &endsOnceFS{OSFS: &OSFS{Root: dir}, dir: "sub", most: 1}
	s, m, ended, stopped := stepsAsTheHost(t, hostRun{dir: dir, fs: fs, judge: &plantJudge{seen: map[string]bool{}}, attempts: &memAttempts{}, calls: 1000, most: 8})
	if s == nil {
		t.Fatal("no scan was saved")
	}
	if !stopped || s.Status != "done" || ended != 1 || fs.endings != 1 || s.FilesJudged != 1 {
		t.Fatalf("stopped=%v status=%s after %d endings (%d in the listing), judged %d; want done after one ending in the listing, x.go judged", stopped, s.Status, ended, fs.endings, s.FilesJudged)
	}
	if s.Excluded["symlink"] != 2 {
		t.Errorf("the scan counts %d symlinks; want 2, each once", s.Excluded["symlink"])
	}

	written := 0
	if _, err := (&Journal{Store: m, Path: "journal"}).WalkAt(0, func(e JournalEvent, _, _ int64) error {
		if e.Coverage != nil && contains(e.Coverage.Reasons, "symlink") {
			written++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if written != 4 {
		t.Fatalf("the journal holds %d symlink rows; want 4 (two written twice), or this test shows nothing", written)
	}

	runs, err := runsOf(m)
	if err != nil {
		t.Fatalf("the journal of a scan whose worker was ended after a listing's rows is not one the report reads: %v", err)
	}
	boundaries := 0
	for ref, rs := range runs {
		for _, r := range rs {
			if len(r.Coverage) == 0 || !contains(r.Coverage[0].Reasons, "symlink") {
				continue
			}
			boundaries++
			if len(rs) != 1 || len(r.Coverage) != 1 || len(r.Records) != 0 {
				t.Errorf("%s is read as %d runs, this one with %d rows and %d records; want it read once", ref, len(rs), len(r.Coverage), len(r.Records))
			}
		}
	}
	if boundaries != 2 {
		t.Errorf("the report reads %d symlink boundaries; want 2", boundaries)
	}
}

func TestR4FOnlyABoundarySayingWhatItSaidMayComeBack(t *testing.T) {
	sub := func(ref, why string) Coverage {
		return Coverage{Ref: ref, Kind: "subtree", Disposition: "excluded", Reasons: []string{why, "children_not_enumerated"}}
	}
	file := func(ref string) Coverage {
		return Coverage{Ref: ref, Kind: "file", Disposition: "unknown", Reasons: []string{"unreadable"}}
	}
	for _, c := range []struct {
		name        string
		rows        []Coverage
		interleaved bool
		runs        int
	}{
		{"one listing written twice", []Coverage{sub("l1", "symlink"), sub("l2", "symlink"), sub("l1", "symlink"), sub("l2", "symlink"), file("x.go")}, false, 3},
		{"two listings written twice", []Coverage{sub("l1", "symlink"), sub("d/l2", "symlink"), sub("d/v", "gitignore"), sub("l1", "symlink"), sub("d/l2", "symlink"), sub("d/v", "gitignore"), file("d/x.go")}, false, 4},
		{"a listing written three times", []Coverage{sub("l1", "symlink"), sub("l2", "symlink"), sub("l1", "symlink"), sub("l1", "symlink"), sub("l2", "symlink")}, false, 2},
		{"a boundary that comes back saying something else", []Coverage{sub("l1", "symlink"), sub("l2", "symlink"), sub("l1", "gitignore")}, true, 0},
		{"a file that comes back", []Coverage{file("a.go"), sub("l2", "symlink"), file("a.go")}, true, 0},
		{"a boundary that comes back as a file", []Coverage{sub("l1", "symlink"), sub("l2", "symlink"), file("l1")}, true, 0},
		{"a file that comes back as a boundary", []Coverage{file("l1"), sub("l2", "symlink"), sub("l1", "symlink")}, true, 0},
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
