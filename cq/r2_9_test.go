package cq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const planted = `package p

import (
	"fmt"
)

// Used calls everything here, so nothing but orphan is unused.
func Used() {
	_ = forward(1)
	_ = deep(1)
	_ = many(1, 2, 3, 4, 5, 6, 7)
	_ = rewrapped(nil)
	stubbed()
	_ = after()
	dupA()
	dupB()
	_ = unusedParam(1, 2)
}

func forward(x int) int { return Clean(x) }

func deep(x int) int {
	if x > 0 {
		if x > 1 {
			if x > 2 {
				if x > 3 {
					if x > 4 {
						return 1
					}
				}
			}
		}
	}
	return 0
}

func many(a, b, c, d, e, f, g int) int { return a + b + c + d + e + f + g }

func rewrapped(err error) error { return fmt.Errorf("%w", err) }

func stubbed() {
	// TODO: say what this is for
	fmt.Println("stub")
}

func after() int {
	return 1
	fmt.Println("never")
}

func dupA() {
	alpha := 1
	bravo := alpha + 2
	charlie := bravo * 3
	delta := charlie - 4
	echo := delta / 5
	fmt.Println(alpha, bravo, charlie, delta, echo)
}

func dupB() {
	alpha := 1
	bravo := alpha + 2
	charlie := bravo * 3
	delta := charlie - 4
	echo := delta / 5
	fmt.Println(alpha, bravo, charlie, delta, echo)
}

func unusedParam(a, b int) int { return a + 1 }

func orphan() { fmt.Println("orphan") }
`

const control = `package p

// Clean adds one.
func Clean(x int) int {
	if x < 0 {
		return -x + 1
	}
	return x + 1
}
`

func slopScan(t *testing.T, files map[string]string, exclude []string) (*Scan, *memStore, *fakeJudge) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(&OSFS{Root: dir}, "s", ".", "", exclude, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := &fakeJudge{p: 0.05}
	for i := 0; i < 50 && s.Status == "running"; i++ {
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}, Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		if _, err := s.Step(table(t), &OSFS{Root: dir}, j, nil, 64, 400); err != nil {
			t.Fatal(err)
		}
	}
	if s.Status != "done" {
		t.Fatalf("scan %s", s.Status)
	}
	return s, m, j
}

func slopPage(t *testing.T, m *memStore, th Thresholds) (map[string][]string, ReviewPage) {
	t.Helper()
	intent := &Journal{Store: m, Path: "intent"}
	pkg, notes, _, err := PackageFindings(intent, true)
	if err != nil {
		t.Fatal(err)
	}
	q := ReviewQuery{Shape: "review_queue", Lane: "slop", Generation: "g", PageBytes: 200000, Limit: 100, ScanDone: true,
		IntentFile: intent, Size: th, Package: pkg, PackageNotes: notes}
	page, err := ReviewFold(&Journal{Store: m, Path: "journal"}, q)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := page.JSON()
	var p struct {
		Entries []FileReview `json:"entries"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range p.Entries {
		for _, s := range f.Slop {
			out[f.Ref] = append(out[f.Ref], s.ID)
		}
		sort.Strings(out[f.Ref])
	}
	return out, page
}

func TestEachDeterministicRuleIsFoundOnce(t *testing.T) {
	s, m, j := slopScan(t, map[string]string{"p/planted.go": planted, "p/control.go": control}, nil)
	calls, chosen := j.calls, j.chosen

	every := DefaultThresholds
	every.ListAll = true
	found, _ := slopPage(t, m, every)
	want := []string{"SL-DUP", "SL-FORWARD", "SL-NEST", "SL-PARAMS", "SL-REWRAP", "SL-STUB", "SL-UNREACH", "SL-UNUSEDFUNC", "SL-UNUSEDPARAM"}
	if j.calls != calls || j.chosen != chosen {
		t.Fatalf("the report called the judge: %d asks and %d choices, then %d and %d", calls, chosen, j.calls, j.chosen)
	}
	if s.Calls != j.calls+j.chosen {
		t.Fatalf("the scan counts %d calls; the judge was called %d times", s.Calls, j.calls+j.chosen)
	}
	for _, id := range want {
		if j.asked[id] != 0 {
			t.Fatalf("the judge was asked %s %d times", id, j.asked[id])
		}
	}
	if strings.Join(found["p/planted.go"], ",") != strings.Join(want, ",") {
		t.Fatalf("planted: %v, want %v", found["p/planted.go"], want)
	}
	if len(found["p/control.go"]) != 0 {
		t.Fatalf("control: %v", found["p/control.go"])
	}
}

func TestPackageRulesNeedTheWholePackage(t *testing.T) {
	_, m, _ := slopScan(t, map[string]string{"p/planted.go": planted, "p/control.go": control, "p/left.go": "package p\n\nfunc helper() {}\n"}, []string{"p/left.go"})
	found, page := slopPage(t, m, DefaultThresholds)
	for _, id := range found["p/planted.go"] {
		if id == "SL-UNUSEDFUNC" || id == "SL-UNUSEDPARAM" {
			t.Fatalf("%s emitted for a package scanned in part: %v", id, found)
		}
	}
	if len(page.PackageNotes) != 1 || !strings.Contains(page.PackageNotes[0].Note, "left.go (caller_exclude)") {
		t.Fatalf("notes: %+v", page.PackageNotes)
	}
}

func TestAThresholdChangeNeedsNoRescan(t *testing.T) {
	_, m, j := slopScan(t, map[string]string{"p/planted.go": planted, "p/control.go": control}, nil)
	calls := j.calls
	loose, _ := slopPage(t, m, Thresholds{Params: 10, Nesting: 9, DupLines: 7})
	for _, id := range loose["p/planted.go"] {
		if id == "SL-PARAMS" || id == "SL-NEST" || id == "SL-DUP" {
			t.Fatalf("%s at raised thresholds: %v", id, loose)
		}
	}
	tight, page := slopPage(t, m, Thresholds{Params: 1})
	if j.calls != calls || len(tight["p/planted.go"]) <= len(loose["p/planted.go"]) || page.Thresholds["params"] != 1 || page.Thresholds["dup_lines"] != 6 {
		t.Fatalf("calls %d then %d; tight %v; thresholds %v", calls, j.calls, tight, page.Thresholds)
	}
}

func TestFindingThresholdsAreReadByName(t *testing.T) {
	th, bad, _ := ParseThresholds("nesting=3, params=8")
	if bad != "" || th.Nesting != 3 || th.Params != 8 || th.DupLines != 6 || th.Large != 1000 {
		t.Fatalf("%+v %q", th, bad)
	}
	for in, key := range map[string]string{"depth=3": "depth", "params=x": "params", "params=0": "params", "nesting": "nesting", "params=2 params=3": "params"} {
		if _, bad, _ := ParseThresholds(in); bad != key {
			t.Errorf("%q: refused %q, want %q", in, bad, key)
		}
	}
}

func TestASlopAnswerWaitsForTheGuard(t *testing.T) {
	code := tierFixture("a.go", 0.05, 0, false)
	slop := Record{Version: RecordVersion, ID: "slop", Ref: code.Ref, SourceDigest: code.SourceDigest, Chunk: code.Chunk, Lines: code.Lines,
		Battery: SlopBattery, Catalogue: SlopDigest(), Expected: []string{"SL-INDIRECT"}, Answers: map[string]float64{"SL-INDIRECT": 0.95}}
	judged := func(records ...Record) int {
		f := BuildReview(records, nil, false, true, "slop").Files[0]
		DefaultThresholds.markWith(&f, nil)
		n := 0
		for _, s := range f.Slop {
			if s.ID == "SL-INDIRECT" {
				n++
			}
		}
		return n
	}
	if n := judged(code, guardRecord(code, 0.9), slop); n != 0 {
		t.Fatalf("swayed: %d", n)
	}
	if n := judged(code, slop); n != 0 {
		t.Fatalf("no guard: %d", n)
	}
	if n := judged(code, guardRecord(code, 0.05), slop); n != 1 {
		t.Fatalf("guard clear: %d", n)
	}
}

func TestNotCheckedIsSaidAndNotCounted(t *testing.T) {
	b := newShapeBuilder(table(t), "go")
	b.add("func Big() {\n\tx := 1\n", 101)
	shape := b.finish(102, 0, false)
	f := FileReview{Ref: "big.go", shape: &shape}
	DefaultThresholds.markWith(&f, nil)
	if len(f.NotChecked) != 1 || !strings.Contains(f.NotChecked[0], "lines 101-") || f.SlopCount != 0 {
		t.Fatalf("not checked %v, slop %d %v", f.NotChecked, f.SlopCount, f.Slop)
	}
}
