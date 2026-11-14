package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

type digestFS struct {
	*OSFS
	read int64
}

func (d *digestFS) ReadAt(file string, off int64, n int) ([]byte, bool, error) {
	b, eof, err := d.OSFS.ReadAt(file, off, n)
	d.read += int64(len(b))
	return b, eof, err
}

func (d *digestFS) Digest(file string) (string, int64, error) {
	b, err := os.ReadFile(filepath.Join(d.Root, file))
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), int64(len(b)), nil
}

func TestAStepReadsOnlyTheSegmentsItJudges(t *testing.T) {
	dir := t.TempDir()
	src, _ := goOfSize(3 * SegmentBytes)
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &digestFS{OSFS: &OSFS{Root: dir}}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12}
	var segs [][5]int64
	segBytes := func(k int) int64 {
		end := int64(len(src))
		if k+1 < len(segs) {
			end = segs[k+1][0]
		}
		return end - segs[k][0]
	}
	steps := 0
	for ; steps < 500 && s.Status == "running"; steps++ {
		j.newStep()
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}, Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		planned, from := s.File != nil, 0
		if planned {
			from = s.File.Next
		}
		before := fs.read
		if _, err := s.Step(table(t), fs, j, nil, 12, 400); err != nil {
			t.Fatal(err)
		}
		if s.File != nil {
			segs = s.File.Segs
		}
		to := len(segs) - 1
		if s.File != nil {
			to = min(s.File.Next, len(segs)-1)
		}
		var want int64
		if !planned {

			want = SegmentBytes
			for _, sg := range segs {
				want += min(SegmentBytes, int64(len(src))-sg[0])
			}
		}
		for k := from; k <= to; k++ {
			want += segBytes(k)
		}
		if got := fs.read - before; got != want {
			t.Fatalf("step %d read %d bytes; its head, plan and segments %d..%d are %d", steps, got, from, to, want)
		}
	}
	if s.Status != "done" || s.FilesJudged != 1 || steps < 4 {
		t.Fatalf("status %s, judged %d in %d steps", s.Status, s.FilesJudged, steps)
	}
}

func TestADigestCatchesAnEditBetweenSteps(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"judged segment": func(s string) string { return strings.Replace(s, "returns x plus 1.", "returns x plus 9.", 1) },
		"appended":       func(s string) string { return s + "// appended after it was planned\n" },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "big.go")
			src, _ := goOfSize(3 * SegmentBytes)
			if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			fs := &digestFS{OSFS: &OSFS{Root: dir}}
			m := newMemStore()
			m.files["journal"], m.files["intent"] = nil, nil
			s, _ := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
			j := &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12}
			final, edited := src, false
			for i := 0; i < 500 && s.Status == "running"; i++ {
				if s.File != nil && s.File.Next >= 2 && !edited {
					edited, final = true, edit(src)
					if err := os.WriteFile(file, []byte(final), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				j.newStep()
				s.AskIntent = true
				s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}, Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
				if _, err := s.Step(table(t), fs, j, nil, 12, 400); err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256([]byte(final))
			var c struct {
				Entries []Coverage `json:"entries"`
			}
			_ = json.Unmarshal([]byte(reviewJSON(t, m, ReviewQuery{Shape: "coverage"})), &c)
			current := 0
			for _, e := range c.Entries {
				if contains(e.Reasons, "source_changed") {
					continue
				}
				current++
				if e.SourceDigest != hex.EncodeToString(sum[:]) {
					t.Fatalf("finished under %s", e.SourceDigest)
				}
			}
			if !edited || current != 1 {
				t.Fatalf("edited %v, %d current coverages: %+v", edited, current, c.Entries)
			}
		})
	}
}

func TestARequestTheBoundCallsSafeIsUnderTheLimit(t *testing.T) {
	r := rand.New(rand.NewSource(20261113))
	alphabet := []string{"a", "\"", "\\", "<", ">", "&", "\x01", "\n", "\xff", "界", " "}
	items := CodeItems("go")
	safe, unsafe := 0, 0
	for i := 0; i < 400; i++ {
		var b strings.Builder
		n := r.Intn(64 << 10)
		for b.Len() < n {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		text := b.String()
		body := Chunk{Text: text, FirstLine: 1, LastLine: 1 + strings.Count(text, "\n")}
		questions := windowQuestions(items, "code", body)
		got, err := RequestBody(strings.Repeat("m", 128), map[string]any{"language": "go", "code": text}, questions)
		if !requestMayExceed(text, questions) {
			safe++
			if err != nil || len(got) >= MaxRequestBytes {
				t.Fatalf("called safe, but %d bytes (%v)", len(got), err)
			}
		} else {
			unsafe++
		}
	}
	if safe == 0 || unsafe == 0 {
		t.Fatalf("the sample never reached both sides: %d safe, %d not", safe, unsafe)
	}
}

func TestAShapeR2WroteIsRead(t *testing.T) {
	broken := "package p\n\nfunc Caller() { orphanish( }\n"
	_, m, _ := slopScan(t, map[string]string{"p/planted.go": planted, "p/broken.go": broken}, nil)
	side := string(m.files["intent"])
	object := `"unchecked":[{"from":1,"to":3,"why":"it does not parse"}]`
	if !strings.Contains(side, object) {
		t.Fatalf("no unchecked range written as r3.1 writes it: %s", side)
	}
	m.files["intent"] = []byte(strings.Replace(side, object, `"unchecked":[[1,3]]`, 1))
	records := 0
	if _, err := (&Journal{Store: m, Path: "intent"}).Walk(0, func(e JournalEvent) error {
		if e.Record != nil && e.Record.Shape != nil && len(e.Record.Shape.Unchecked) > 0 {
			records++
			if u := e.Record.Shape.Unchecked[0]; u.From != 1 || u.To != 3 || u.Why != UncheckedEarlier {
				t.Fatalf("read as %+v", u)
			}
		}
		return nil
	}); err != nil || records != 1 {
		t.Fatalf("the side file did not walk to its end: %v (%d unchecked)", err, records)
	}
	found, page := slopPage(t, m, DefaultThresholds)
	if strings.Contains(strings.Join(found["p/planted.go"], ","), "SL-UNUSEDFUNC") || len(page.PackageNotes) != 1 || !strings.Contains(page.PackageNotes[0].Note, "broken.go") {
		t.Fatalf("package rules beside an unread file: %v %+v", found, page.PackageNotes)
	}
}

func TestAFunctionLargerThanASegmentSaysSo(t *testing.T) {
	var b strings.Builder
	b.WriteString("package big\n\nfunc Huge() int {\n\tx := 0\n")
	for b.Len() < SegmentBytes+SegmentBytes/3 {
		b.WriteString("\tx += 1\n")
	}
	b.WriteString("\treturn x\n}\n\nfunc Small() int { return 1 }\n")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "huge.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	_, shape, err := PlanFile(table(t), &OSFS{Root: dir}, "huge.go", "huge.go", "go", ChunkBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(shape.Unchecked) == 0 {
		t.Fatal("a function over a segment was read whole")
	}
	for _, u := range shape.Unchecked {
		if u.Why != UncheckedLarge {
			t.Fatalf("lines %d-%d said not checked because %q", u.From, u.To, u.Why)
		}
	}
}

func TestTheJudgesYesesAreCountedByGuard(t *testing.T) {
	cases := []struct {
		ref, guard, tier string
		p, gc02          float64
	}{
		{"clear.go", GuardClear, TierIntentStated, 0.9, 0.1},
		{"fired.go", GuardFired, TierPinned, 0.9, 0.8},
		{"abstained.go", GuardUnknown, TierPinned, 0.9, 0.5},
		{"unanswered.go", GuardUnknown, TierPinned, 0.9, -1},
		{"no.go", GuardClear, TierPinned, 0.2, 0.1},
	}
	fold := NewSummaryFold(10)
	for i, c := range cases {
		r := tierFixture(c.ref, 0.95, 0.90, false)
		r.ID = fmt.Sprintf("r%d", i)
		run := []Record{r, intentRecord(r, c.p)}
		if c.gc02 >= 0 {
			run = append(run, guardRecord(r, c.gc02))
		}
		fold.AddRun(run)
		rep := BuildReview(run, nil, false, true, "")
		f := rep.Files[0]
		e := f.Findings[0]
		if f.Tier != c.tier || e.IntentGuard != c.guard {
			t.Errorf("%s: tier %s, guard %s; want %s, %s", c.ref, f.Tier, e.IntentGuard, c.tier, c.guard)
		}
		claim := c.p >= PTrue
		if (e.IntentClaim != "") != claim || claim && (!strings.HasPrefix(e.IntentClaim, "Judge says") || (c.guard != GuardClear) != strings.HasSuffix(e.IntentClaim, "priority unchanged")) {
			t.Errorf("%s: intent_claim %q", c.ref, e.IntentClaim)
		}
	}
	want := IntentCounts{Asked: 5, Saved: 5, YesClear: 1, YesFired: 1, YesUnknown: 2}
	if got := fold.Result()["intent"]; !reflect.DeepEqual(got, want.result()) {
		t.Fatalf("summary intent %v, want %v", got, want.result())
	}
}

func slopOf(t *testing.T, files map[string]string, th Thresholds) (map[string][]SlopFinding, ReviewPage, *Scan, *fakeJudge) {
	t.Helper()
	s, m, j := slopScan(t, files, nil)
	_, page := slopPage(t, m, th)
	b, _ := page.JSON()
	var p struct {
		Entries []FileReview `json:"entries"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	out := map[string][]SlopFinding{}
	for _, f := range p.Entries {
		out[f.Ref] = f.Slop
	}
	return out, page, s, j
}

func has(fs []SlopFinding, id string) (SlopFinding, bool) {
	for _, f := range fs {
		if f.ID == id {
			return f, true
		}
	}
	return SlopFinding{}, false
}

func TestAPlatformVariantsUnusedParameterIsNotFlagged(t *testing.T) {
	fn := func(name string) string {
		return "func " + name + "(a, b int) int {\n\treturn a\n}\n"
	}
	files := map[string]string{
		"p/use.go":         "package p\n\nvar _ = []int{plain(1, 2), suffixed(1, 2), tagged(1, 2), arch(1, 2)}\n",
		"p/plain.go":       "package p\n\n" + fn("plain"),
		"p/sys_windows.go": "package p\n\n" + fn("suffixed"),
		"p/sys_arm64.go":   "package p\n\n" + fn("arch"),
		"p/sys.go":         "// Copyright\n\n//go:build linux || android\n\npackage p\n\n" + fn("tagged"),
	}
	found, _, _, _ := slopOf(t, files, DefaultThresholds)
	if f, ok := has(found["p/plain.go"], "SL-UNUSEDPARAM"); !ok || f.Detail != "unused: b" {
		t.Fatalf("plain.go: %+v", found["p/plain.go"])
	}
	for _, ref := range []string{"p/sys_windows.go", "p/sys_arm64.go", "p/sys.go"} {
		if _, ok := has(found[ref], "SL-UNUSEDPARAM"); ok {
			t.Errorf("%s, a platform variant, flagged: %+v", ref, found[ref])
		}
	}
}

func TestAnElseIfChainAddsNoNesting(t *testing.T) {
	chain := "func chain(n int) int {\n\tfor i := 0; i < n; i++ {\n\t\tif i == 1 {\n\t\t\treturn 1\n\t\t} else if i == 2 {\n\t\t\treturn 2\n\t\t} else if i == 3 {\n\t\t\treturn 3\n\t\t} else if i == 4 {\n\t\t\treturn 4\n\t\t} else if i == 5 {\n\t\t\treturn 5\n\t\t} else {\n\t\t\treturn 6\n\t\t}\n\t}\n\treturn 0\n}\n"
	literal := "func literal(n int) func() int {\n\tif n > 0 {\n\t\treturn func() int {\n\t\t\tfor i := 0; i < n; i++ {\n\t\t\t\tif i > 2 {\n\t\t\t\t\tswitch i {\n\t\t\t\t\tcase 3:\n\t\t\t\t\t\treturn i\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t}\n\t\t\treturn 0\n\t\t}\n\t}\n\treturn nil\n}\n"
	found, _, _, _ := slopOf(t, map[string]string{"p/a.go": "package p\n\nvar _, _ = chain(1), literal(1)\n\n" + chain + "\n" + literal}, DefaultThresholds)
	var nest []SlopFinding
	for _, f := range found["p/a.go"] {
		if f.ID == "SL-NEST" {
			nest = append(nest, f)
		}
	}
	if len(nest) != 1 || nest[0].Func != "literal" || !strings.HasSuffix(nest[0].Detail, ", through a function literal") {
		t.Fatalf("SL-NEST: %+v", nest)
	}
}

func TestAStubMarkerQuotedInACommentIsNotAStub(t *testing.T) {
	quoted := "package p\n\n// find lists lines like \"// TODO: x\" and \"FIXME\" for the reader.\nfunc find() int {\n\treturn 1\n}\n\nvar _ = find()\n"
	real := "package p\n\n// TODO: make it count.\nfunc count() int {\n\treturn 1\n}\n\nvar _ = count()\n"
	found, _, _, _ := slopOf(t, map[string]string{"p/quoted.go": quoted, "p/real.go": real}, DefaultThresholds)
	if _, ok := has(found["p/quoted.go"], "SL-STUB"); ok {
		t.Errorf("quoted.go: %+v", found["p/quoted.go"])
	}
	if _, ok := has(found["p/real.go"], "SL-STUB"); !ok {
		t.Errorf("real.go: %+v", found["p/real.go"])
	}
}

func TestFoldersGoIgnoresAreOutOfScope(t *testing.T) {
	body := "package q\n\nfunc F(a int) int {\n\tif a > 0 {\n\t\treturn a\n\t}\n\treturn -a\n}\n"
	py := "def helper(a):\n    if a > 0:\n        return a\n    return -a\n"
	files := map[string]string{"p/kept.go": "package p\n\nfunc K() int {\n\treturn 1\n}\n",
		"p/testdata/fake.go": body, "p/_old/x.go": body, "p/.hidden/y.go": body, "p/_internal/helper.py": py}
	s, m, j := slopScan(t, files, nil)
	_, _, alone := slopScan(t, map[string]string{"p/kept.go": files["p/kept.go"], "p/_internal/helper.py": py}, nil)
	if s.FilesJudged != 2 || j.calls != alone.calls || j.chosen != alone.chosen {
		t.Fatalf("judged %d files in %d calls; kept.go and helper.py alone take %d", s.FilesJudged, j.calls+j.chosen, alone.calls+alone.chosen)
	}
	page, err := ReviewFold(&Journal{Store: m, Path: "journal"}, ReviewQuery{Shape: "triage", Lane: "correctness", Generation: "g", PageBytes: 200000, Limit: 50, ScanDone: true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range page.OutOfScopeFiles {
		got[f.Ref] = f.Reason
	}

	for _, ref := range []string{"p/testdata", "p/.hidden", "p/_old/x.go"} {
		if got[ref] != IgnoredDir {
			t.Errorf("%s: %q (out of scope: %+v)", ref, got[ref], page.OutOfScopeFiles)
		}
	}
	if _, listed := got["p/_internal/helper.py"]; listed || page.OutOfScopeCount == nil || *page.OutOfScopeCount != 3 || !page.InventoryComplete {
		t.Errorf("out of scope %+v, count %v, inventory complete %v", page.OutOfScopeFiles, page.OutOfScopeCount, page.InventoryComplete)
	}
}

func TestTheRewrapRuleSaysWhatItChecks(t *testing.T) {
	src := "package p\n\nimport \"fmt\"\n\nfunc wrap(v any) error {\n\treturn fmt.Errorf(\"%v\", v)\n}\n\nvar _ = wrap(1)\n"
	found, _, _, _ := slopOf(t, map[string]string{"p/w.go": src}, DefaultThresholds)
	if f, ok := has(found["p/w.go"], "SL-REWRAP"); !ok || f.Detail != RewrapWords || strings.Contains(f.Detail, "error") && !strings.Contains(f.Detail, "Error()") {
		t.Fatalf("w.go: %+v", found["p/w.go"])
	}
}

func TestPassThroughIsTiered(t *testing.T) {
	src := "package p\n\nimport \"os\"\n\n// Start starts.\nfunc Start(n int) int {\n\treturn start(n)\n}\n\nfunc start(n int) int {\n\treturn n + 1\n}\n\n// Twice is double.\nfunc Twice(n int) int {\n\treturn double(n)\n}\n\nfunc double(n int) int {\n\treturn 2 * n\n}\n\n// Getenv reads it.\nfunc Getenv(k string) string {\n\treturn os.Getenv(k)\n}\n"
	found, page, _, _ := slopOf(t, map[string]string{"p/f.go": src}, DefaultThresholds)
	var fwd []string
	for _, f := range found["p/f.go"] {
		if f.ID == "SL-FORWARD" {
			fwd = append(fwd, f.Func)
		}
	}
	sort.Strings(fwd)
	if strings.Join(fwd, ",") != "Start" || page.Unlisted["SL-FORWARD"] != 2 || page.UnlistedWords != UnlistedWords {
		t.Fatalf("default: SL-FORWARD %v, counted not listed %v", fwd, page.Unlisted)
	}
	all := DefaultThresholds
	all.ListAll = true
	found, page, _, _ = slopOf(t, map[string]string{"p/f.go": src}, all)
	fwd = nil
	for _, f := range found["p/f.go"] {
		if f.ID == "SL-FORWARD" {
			fwd = append(fwd, f.Func)
		}
	}
	sort.Strings(fwd)
	if strings.Join(fwd, ",") != "Getenv,Start,Twice" || page.Unlisted != nil {
		t.Fatalf("all: SL-FORWARD %v, counted not listed %v", fwd, page.Unlisted)
	}
}

func TestTheSideFileUsesTheJournalsUnusedRoom(t *testing.T) {
	const room = 600 << 10
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	p := &JournalProgress{Journal: &Journal{Store: m, Path: "journal", MaxBytes: room - room/8}, Intent: &Journal{Store: m, Path: "intent", MaxBytes: room}, Room: room}
	r := tierFixture("a.go", 0.95, 0.90, false)
	if err := p.Save(r); err != nil {
		t.Fatal(err)
	}
	var err error
	for i := 0; err == nil; i++ {
		side := intentRecord(r, 0.5)
		side.ID = fmt.Sprintf("side-%d", i)
		err = p.Save(side)
	}
	journal, side := len(m.files["journal"]), len(m.files["intent"])
	if !errors.Is(err, ErrIntentFull) || side <= room/8 || journal+side > room {
		t.Fatalf("stopped at %v with journal %d and side file %d of %d", err, journal, side, room)
	}
	if more, err := p.HasRoom(); err != nil || more {
		t.Fatalf("room left: %v, %v", more, err)
	}
	if err := p.Save(r); !errors.Is(err, ErrJournalFull) || len(m.files["journal"]) != journal {
		t.Fatalf("a journal append past the room: %v", err)
	}

	m.files["journal"], m.files["intent"] = nil, nil
	p = &JournalProgress{Journal: &Journal{Store: m, Path: "journal", MaxBytes: room - room/8}, Intent: &Journal{Store: m, Path: "intent", MaxBytes: room / 8}}
	for i := 0; err == nil || i == 0; i++ {
		side := intentRecord(r, 0.5)
		side.ID = fmt.Sprintf("side-%d", i)
		err = p.Save(side)
	}
	if len(m.files["intent"]) > room/8 {
		t.Fatalf("fixed share: side file %d", len(m.files["intent"]))
	}
}

func TestRD04AndST04AreCountedNotListed(t *testing.T) {
	both := reviewFixture("both.go", "RD-04", "ST-04", "ST-02")
	only := reviewFixture("only.go", "RD-04", "ST-04")
	only.ID = "only"
	records := []Record{both, only}
	cov := []Coverage{completeFixture(both), completeFixture(only)}
	page := func(lane string, all bool) (ReviewPage, []FileReview) {
		th := DefaultThresholds
		th.ListAll = all
		rep := BuildReview(records, cov, true, true, lane)
		p, err := PageReview(rep, ReviewQuery{Shape: "review_queue", Lane: lane, Generation: "g", PageBytes: 200000, Limit: 50, ScanDone: true, Size: th})
		if err != nil {
			t.Fatal(err)
		}
		var files []FileReview
		if err := json.Unmarshal(p.Entries, &files); err != nil {
			t.Fatal(err)
		}
		return p, files
	}
	ids := func(f FileReview) string {
		var out []string
		for _, e := range f.Findings {
			out = append(out, e.ID)
		}
		for _, s := range f.Slop {
			out = append(out, "slop:"+s.ID)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	p, files := page("maintainability", false)
	if len(files) != 1 || files[0].Ref != "both.go" || strings.Contains(ids(files[0]), "-04") || files[0].Unlisted["RD-04"] != 1 || files[0].Unlisted["ST-04"] != 1 {
		t.Fatalf("default maintainability: %d files, %+v", len(files), files)
	}
	if p.Unlisted["RD-04"] != 2 || p.Unlisted["ST-04"] != 2 || p.UnlistedWords != UnlistedWords || p.Total != 1 {
		t.Fatalf("default page: total %d, counted not listed %v", p.Total, p.Unlisted)
	}
	if !strings.Contains(files[0].SelectionReason, "; 1 distinct flagged categories;") {
		t.Errorf("selection reason counts what is listed: %q", files[0].SelectionReason)
	}
	_, files = page("slop", false)
	for _, f := range files {
		if strings.Contains(ids(f), "slop:RD-04") || strings.Contains(ids(f), "slop:ST-04") {
			t.Fatalf("default slop lists %s", ids(f))
		}
	}
	p, files = page("maintainability", true)
	if len(files) != 2 || p.Unlisted != nil {
		t.Fatalf("list_all: %d files, counted not listed %v", len(files), p.Unlisted)
	}
	for _, f := range files {
		if !strings.Contains(ids(f), "RD-04") || !strings.Contains(ids(f), "ST-04") {
			t.Fatalf("list_all: %s lists %s", f.Ref, ids(f))
		}
	}
	fold := NewSummaryFold(10)
	for _, r := range records {
		fold.Add(r)
		fold.AddRun([]Record{r})
	}
	slop := fold.Result()["slop"].(map[string]any)
	if u := slop["counted_not_listed"].(map[string]int); u["RD-04"] != 2 || u["ST-04"] != 2 || u["SL-FORWARD"] != 0 {
		t.Fatalf("summary: %v", slop)
	}
	if c := fold.Result()["lead_counts"].(map[string]int); c["RD-04"] != 2 || c["ST-04"] != 2 {
		t.Fatalf("summary lead counts drop them: %v", fold.Result()["lead_counts"])
	}
}

func TestADenseFilesCursorAndShapeFitTheirBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString("package dense\n\n")
	for k := 0; b.Len()+40 < 1<<20; k++ {
		fmt.Fprintf(&b, "func f%d() {}\n", k)
	}
	src := b.String()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dense.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &digestFS{OSFS: &OSFS{Root: dir}}
	cur, shape, err := PlanFile(table(t), fs, "dense.go", "dense.go", "go", ChunkBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(cur)
	record, _ := json.Marshal(JournalEvent{Record: ptr(ShapeRecord("dense.go", cur.Digest, *shape))})
	if len(state) > 64<<10 || len(record) >= journalEventBytes {
		t.Fatalf("cursor %d bytes (checkpoint %d), shape record %d bytes (event %d)", len(state), CheckpointBytes, len(record), journalEventBytes)
	}
	for k, u := range cur.Units {
		for _, c := range u.Cuts {
			if at := int(cur.Segs[k][0]) + c; at != 0 && at < len(src) && src[at-1] != '\n' {
				t.Fatalf("segment %d cut at byte %d, inside a line", k, at)
			}
		}
	}
	if len(shape.Unchecked) == 0 || shape.Unchecked[len(shape.Unchecked)-1].Why != UncheckedNames || shape.Decls != nil {
		t.Fatalf("names kept or not said: %d decls, unchecked %+v", len(shape.Decls), shape.Unchecked)
	}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, MaxSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; s.Status == "running" && i < 200; i++ {
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}, Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		if _, err := s.Step(table(t), fs, &fakeJudge{p: 0.01}, nil, 12, 400); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if state, _ := json.Marshal(s); len(state) > 64<<10 {
			t.Fatalf("step %d: the scan's state is %d bytes", i, len(state))
		}
	}
	if s.Status != "done" || s.FilesJudged != 1 {
		t.Fatalf("scan %s, %d judged", s.Status, s.FilesJudged)
	}
	t.Logf("cursor %d bytes, %d cuts in %d segments; shape record %d bytes", len(state), func() (n int) {
		for _, u := range cur.Units {
			n += len(u.Cuts)
		}
		return
	}(), len(cur.Segs), len(record))
}

func ptr[T any](v T) *T { return &v }

func TestAnEarlierShapesPassThroughIsListed(t *testing.T) {
	s := &FileShape{Funcs: []FuncShape{{Name: "Start", Line: 3, Forward: true}, {Name: "Run", Line: 9, Forward: true, Callee: "exec.Run"}}}
	var got []string
	for _, f := range DefaultThresholds.deterministic(s) {
		if f.ID == "SL-FORWARD" {
			got = append(got, f.Func+": "+f.Detail)
		}
	}
	if len(got) != 1 || got[0] != "Start: "+forwardEarlier || passThroughOthers(s) != 1 {
		t.Fatalf("SL-FORWARD %v; counted, not listed %d", got, passThroughOthers(s))
	}
}

func TestCountedNotListedIsWholeOnEachPageAndBindsTheCursor(t *testing.T) {
	a, b := reviewFixture("a.go", "RD-04", "ST-02"), reviewFixture("b.go", "ST-04", "ST-02")
	b.ID = "b"
	rep := func(lane string) ReviewReport {
		return BuildReview([]Record{a, b}, []Coverage{completeFixture(a), completeFixture(b)}, true, true, lane)
	}
	for lane, want := range map[string]map[string]int{
		"maintainability": {"RD-04": 1, "ST-04": 1}, "": {"RD-04": 1, "ST-04": 1},
		"slop": {"SL-FORWARD": 0, "RD-04": 1, "ST-04": 1}, "correctness": nil,
	} {
		p, err := PageReview(rep(lane), ReviewQuery{Shape: "review_queue", Lane: lane, Generation: "g", PageBytes: 200000, Limit: 50, ScanDone: true, Size: DefaultThresholds})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(p.Unlisted, want) {
			t.Errorf("lane %q: counted_not_listed %v, want %v", lane, p.Unlisted, want)
		}
	}
	q := ReviewQuery{Shape: "review_queue", Lane: "maintainability", Generation: "g", PageBytes: 200000, Limit: 1, ScanDone: true, Size: DefaultThresholds}
	first, err := PageReview(rep("maintainability"), q)
	if err != nil || first.Next == "" {
		t.Fatalf("first page: %v, next %q", err, first.Next)
	}
	q.Cursor = first.Next
	q.Size.ListAll = true
	if _, err := PageReview(rep("maintainability"), q); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("a cursor of the default view read under list_all: %v", err)
	}
}

func TestTheFoldCursorBindsListAll(t *testing.T) {
	_, m, _ := slopScan(t, map[string]string{"p/a.go": planted, "p/b.go": strings.Replace(planted, "package p", "package p\n\n// b.", 1)}, nil)
	q := ReviewQuery{Shape: "review_queue", Lane: "slop", Generation: "g", PageBytes: 200000, Limit: 1, ScanDone: true, Size: DefaultThresholds,
		IntentFile: &Journal{Store: m, Path: "intent"}}
	first, err := ReviewFold(&Journal{Store: m, Path: "journal"}, q)
	if err != nil || first.Next == "" {
		t.Fatalf("first page: %v, next %q, total %d, entries %.300s", err, first.Next, first.Total, first.Entries)
	}
	q.Cursor = first.Next
	if _, err := ReviewFold(&Journal{Store: m, Path: "journal"}, q); err != nil {
		t.Fatalf("the same view's next page: %v", err)
	}
	q.Size.ListAll = true
	if _, err := ReviewFold(&Journal{Store: m, Path: "journal"}, q); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("a cursor of the default view read under list_all: %v", err)
	}
}

func TestRD04AndST04DoNotRankAFile(t *testing.T) {
	x, y := reviewFixture("x.go", "RD-04", "ST-04", "RD-01"), reviewFixture("y.go", "RD-02")
	y.ID = "y"
	order := func(all bool) string {
		th := DefaultThresholds
		th.ListAll = all
		rep := BuildReview([]Record{x, y}, []Coverage{completeFixture(x), completeFixture(y)}, true, true, "maintainability")
		p, err := PageReview(rep, ReviewQuery{Shape: "review_queue", Lane: "maintainability", Generation: "g", PageBytes: 200000, Limit: 50, ScanDone: true, Size: th})
		if err != nil {
			t.Fatal(err)
		}
		var files []FileReview
		_ = json.Unmarshal(p.Entries, &files)
		var refs []string
		for _, f := range files {
			refs = append(refs, f.Ref)
		}
		return strings.Join(refs, ",")
	}
	if got := order(false); got != "y.go,x.go" {
		t.Errorf("default order %s", got)
	}
	if got := order(true); got != "x.go,y.go" {
		t.Errorf("list_all order %s", got)
	}
}

func TestPlatformTaggedReadsAsGoBuildDoes(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       bool
	}{
		{"sys_linux.go", "package p", true},
		{"sys_linux_amd64.go", "package p", true},
		{"sys_windows_test.go", "package p", true},
		{"sys_arm64.go", "package p", true},
		{"linux.go", "package p", false},
		{"parse_js_ast.go", "package p", false},
		{"handle_arm_state.go", "package p", false},
		{"plain.go", "//go:build !windows\n\npackage p", true},
		{"plain.go", "//go:build linux || android\n\npackage p", true},
		{"plain.go", "//go:build unix\n\npackage p", true},
		{"plain.go", "// +build darwin\n\npackage p", true},
		{"plain.go", "//go:build go1.21\n\npackage p", false},
		{"plain.go", "//go:build integration\n\npackage p", false},
		{"plain.go", "package p\n\n//go:build linux", false},
	} {
		if got := platformTagged(c.name, c.text); got != c.want {
			t.Errorf("%s %q: %v, want %v", c.name, c.text, got, c.want)
		}
	}
}

func TestAStubIsAMarkerWhereACommentBegins(t *testing.T) {
	tb := table(t)
	for _, c := range []struct {
		lang, text string
		want       int
	}{
		{"go", "package p\n\nvar s = \"a # TODO later\"\n", 0},
		{"go", "package p\n\nvar s = \"x // TODO\"\n", 0},
		{"go", "package p\n\n# TODO not Go\n", 0},
		{"go", "package p\n\n/* FIXME: the bound */\nvar s = 1\n", 1},
		{"go", "package p\n\n/*\n * TODO: split it\n */\nvar s = 1\n", 1},
		{"go", "package p\n\nvar s = \"#\" // TODO: name it\n", 1},
		{"go", "package p\n\nvar e = errors.New(\"not implemented\")\n", 1},
		{"python", "s = \"// TODO\"\n", 0},
		{"python", "x = 1  # TODO: tune\n", 1},
	} {
		sb := newShapeBuilder(tb, c.lang)
		sb.add(c.text, 1)
		if got := len(sb.finish(CountLines(c.text), 0, false).Stubs); got != c.want {
			t.Errorf("%s %q: %d stubs, want %d", c.lang, c.text, got, c.want)
		}
	}
}

func TestALiteralLargerThanASegmentSaysSo(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nvar table = map[int]int{\n")
	for k := 0; b.Len() < 300<<10; k++ {
		fmt.Fprintf(&b, "\t%d: %d,\n", k, k*k)
	}
	b.WriteString("}\n\nfunc F() int { return table[1] }\n")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "t.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	_, shape, err := PlanFile(table(t), &digestFS{OSFS: &OSFS{Root: dir}}, "t.go", "t.go", "go", ChunkBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(shape.Unchecked) == 0 || shape.Unchecked[0].Why != UncheckedLarge || UncheckedLarge != "a declaration larger than one segment" {
		t.Fatalf("unchecked %+v", shape.Unchecked)
	}
}

func TestAShapeRecordIsAlwaysOneEvent(t *testing.T) {
	var params []string
	for k := 0; k < 24; k++ {
		params = append(params, fmt.Sprintf("parameterNumber%05d", k))
	}
	var b strings.Builder
	b.WriteString("package p\n\n")
	for k := 0; k < 500; k++ {
		fmt.Fprintf(&b, "func F%d(%s int) {}\n", k, strings.Join(params, ", "))
	}
	src := b.String()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "params.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, shape, err := PlanFile(table(t), &digestFS{OSFS: &OSFS{Root: dir}}, "params.go", "params.go", "go", ChunkBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(JournalEvent{Record: ptr(ShapeRecord("params.go", cur.Digest, *shape))})
	names := 0
	for _, f := range shape.Funcs {
		names += len(f.Unused)
	}
	if len(record) >= journalEventBytes || names != 0 || len(shape.Funcs) != 500 || shape.Unchecked[len(shape.Unchecked)-1].Why != UncheckedNames {
		t.Fatalf("%d-byte source: record %d bytes, %d unused names kept, %d funcs, unchecked %+v", len(src), len(record), names, len(shape.Funcs), shape.Unchecked)
	}

	long := strings.Repeat("N", 300)
	b.Reset()
	b.WriteString("package p\n\n")
	for k := 0; k < 1000; k++ {
		fmt.Fprintf(&b, "func F%s%d() {}\n", long, k)
	}
	sb := newShapeBuilder(table(t), "go")
	sb.add(b.String(), 1)
	whole := sb.finish(CountLines(b.String()), 1000, false)
	record, _ = json.Marshal(JournalEvent{Record: ptr(ShapeRecord("long.go", "d", whole))})
	if len(record) >= journalEventBytes || whole.Funcs != nil || len(whole.Unchecked) != 1 || whole.Unchecked[0].Why != UncheckedShape || whole.Lines != 1002 {
		t.Fatalf("record %d bytes, %d funcs, unchecked %+v, lines %d", len(record), len(whole.Funcs), whole.Unchecked, whole.Lines)
	}
}

func TestAPageCursorBindsTheThresholds(t *testing.T) {
	_, m, _ := slopScan(t, map[string]string{"p/a.go": planted, "p/b.go": strings.Replace(planted, "package p", "package p\n\n// b.", 1)}, nil)
	q := ReviewQuery{Shape: "review_queue", Lane: "slop", Generation: "g", PageBytes: 200000, Limit: 1, ScanDone: true, Size: DefaultThresholds,
		IntentFile: &Journal{Store: m, Path: "intent"}}
	first, err := ReviewFold(&Journal{Store: m, Path: "journal"}, q)
	if err != nil || first.Next == "" {
		t.Fatalf("first page: %v, next %q", err, first.Next)
	}
	q.Cursor = first.Next
	q.Size.Nesting = DefaultThresholds.Nesting + 1
	if _, err := ReviewFold(&Journal{Store: m, Path: "journal"}, q); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("fold: a cursor read under other thresholds: %v", err)
	}
	x, y := reviewFixture("x.go", "RD-01"), reviewFixture("y.go", "RD-02")
	y.ID = "y"
	rep := BuildReview([]Record{x, y}, []Coverage{completeFixture(x), completeFixture(y)}, true, true, "maintainability")
	pq := ReviewQuery{Shape: "review_queue", Lane: "maintainability", Generation: "g", PageBytes: 200000, Limit: 1, ScanDone: true, Size: DefaultThresholds}
	p1, err := PageReview(rep, pq)
	if err != nil || p1.Next == "" {
		t.Fatalf("pager first page: %v", err)
	}
	pq.Cursor, pq.Size.Params = p1.Next, DefaultThresholds.Params+1
	if _, err := PageReview(rep, pq); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("pager: a cursor read under other thresholds: %v", err)
	}
}

type junkChooser struct{ fakeJudge }

func (j *junkChooser) Choose(state, qs map[string]any) (map[string]Choice, error) {
	out, err := j.fakeJudge.Choose(state, qs)
	for id, c := range out {
		c.Probabilities = map[string]float64{}
		for k := 0; k < 12000; k++ {
			c.Probabilities[fmt.Sprintf("not offered %06d", k)] = 1.0 / 12000
		}
		out[id] = c
	}
	return out, err
}

func TestALocationReplyKeepsOnlyWhatWasOffered(t *testing.T) {
	src := "package p\n\nimport \"os\"\n\n// W writes.\nfunc W(name string) {\n\tf, _ := os.Create(name)\n\tf.Write(nil)\n}\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "w.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	fs := &OSFS{Root: dir}
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, MaxSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := &junkChooser{fakeJudge{p: 0.99, conf: 0.9}}
	for i := 0; s.Status == "running" && i < 20; i++ {
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}, Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		if _, err := s.Step(table(t), fs, j, nil, 64, 400); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if s.Status != "done" || j.chosen == 0 || len(m.files["journal"]) > 64<<10 {
		t.Fatalf("scan %s after %d location questions; journal %d bytes", s.Status, j.chosen, len(m.files["journal"]))
	}
	var recs []Record
	_, _ = (&Journal{Store: m, Path: "journal"}).Walk(0, func(e JournalEvent) error {
		if e.Record != nil {
			recs = append(recs, *e.Record)
		}
		return nil
	})
	located := 0
	for _, r := range recs {
		for _, f := range r.Findings {
			if a := f.LocationAnswer; a != nil {
				located++
				if len(a.Probabilities) != 0 {
					t.Fatalf("%s kept %d unoffered keys", f.ID, len(a.Probabilities))
				}
			}
		}
	}
	rep := BuildReview(recs, nil, true, true, "")
	for _, f := range rep.Files {
		for _, e := range f.Findings {
			if e.LocationAnswer != nil && e.LocationAnswer.Probabilities != nil {
				t.Fatalf("a review entry carries a location distribution: %+v", e.LocationAnswer)
			}
		}
	}
	if located == 0 {
		t.Fatal("no finding was located: the test asked nothing")
	}
}

func TestANameNoRecordCanHoldIsCountedNotJudged(t *testing.T) {
	s, _, _ := slopScan(t, map[string]string{"p/a\nb.go": "package p\n\nfunc F() {}\n", "p/ok.go": "package p\n\nfunc G() {}\n"}, nil)
	if s.Status != "done" || s.Excluded[UnrecordableName] != 1 || s.FilesJudged != 1 {
		t.Fatalf("scan %s, excluded %v, judged %d", s.Status, s.Excluded, s.FilesJudged)
	}
}

func TestALargeFilesOutlineIsBoundedInBytes(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\n")
	for k := 0; k < 200; k++ {
		fmt.Fprintf(&b, "type T%s%d struct{}\n\n", strings.Repeat("N", 975), k)
	}
	for b.Len() < 1<<20-100 {
		b.WriteString("const _ = 1\n")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "o.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, _, err := PlanFile(table(t), &digestFS{OSFS: &OSFS{Root: dir}}, "o.go", "o.go", "go", ChunkBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	outline, _ := json.Marshal(cur.Outline)
	state, _ := json.Marshal(cur)
	if len(outline) > outlineMost || len(cur.Outline) == 0 || len(state) > 64<<10 {
		t.Fatalf("outline %d bytes in %d items; cursor %d bytes", len(outline), len(cur.Outline), len(state))
	}
}

func TestAUnitKeyIsValidText(t *testing.T) {
	head := "func F() string { return \"" + strings.Repeat("a", 45) + "ééééé\" }"
	for _, u := range Units(head+"\n", 1, nil) {
		if !utf8.ValidString(u.Key) {
			t.Fatalf("unit key %q is not valid text", u.Key)
		}
	}
}
