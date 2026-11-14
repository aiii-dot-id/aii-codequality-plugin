package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

var headed = map[string]string{
	"licence": "// Copyright 2026 Example\n// SPDX-License-Identifier: Apache-2.0\n\npackage p\n",
	"tag":     "//go:build linux\n\npackage p\n",
	"doc":     "// Package p does one thing.\npackage p\n",
}

const headedBody = `
func Used() { deep(1); many(1, 2, 3, 4, 5, 6, 7) }

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
`

func TestAHeadedFileIsReadWhole(t *testing.T) {
	for name, head := range headed {
		src := head + headedBody
		b := newShapeBuilder(table(t), "go")
		b.add(src, 1)
		shape := b.finish(CountLines(src), 3, false)
		f := FileReview{Ref: name + ".go", shape: &shape}
		DefaultThresholds.markWith(&f, nil)
		ids := map[string]bool{}
		for _, s := range f.Slop {
			ids[s.ID] = true
		}
		if len(shape.Funcs) != 3 || len(shape.Unchecked) != 0 || len(f.NotChecked) != 0 || !ids["SL-NEST"] || !ids["SL-PARAMS"] || !OutlineAsked(&shape) {
			t.Errorf("%s: funcs %d, unchecked %v, not checked %v, findings %v", name, len(shape.Funcs), shape.Unchecked, f.NotChecked, f.Slop)
		}
	}
}

func TestThePluginsOwnFilesAreReadWhole(t *testing.T) {
	var files []string
	for _, dir := range []string{".", ".."} {
		names, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if !strings.HasSuffix(n, "_test.go") {
				files = append(files, n)
			}
		}
	}
	if len(files) < 20 {
		t.Fatalf("found %d production files", len(files))
	}
	for _, n := range files {
		raw, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		b := newShapeBuilder(table(t), "go")
		b.add(string(raw), 1)
		shape := b.finish(CountLines(string(raw)), 0, false)
		if len(shape.Unchecked) != 0 || len(shape.Funcs) == 0 && strings.Contains(string(raw), "\nfunc ") {
			t.Errorf("%s: unchecked %v, %d funcs", n, shape.Unchecked, len(shape.Funcs))
		}
	}
}

func TestAnUnreadRangeKeepsPackageRulesOff(t *testing.T) {
	broken := "// Copyright\n\npackage p\n\nfunc Caller() { orphanish( }\n"
	_, m, _ := slopScan(t, map[string]string{"p/planted.go": planted, "p/control.go": control, "p/broken.go": broken}, nil)
	found, page := slopPage(t, m, DefaultThresholds)
	for _, id := range found["p/planted.go"] {
		if id == "SL-UNUSEDFUNC" || id == "SL-UNUSEDPARAM" {
			t.Fatalf("%s beside an unread file: %v", id, found)
		}
	}
	if len(page.PackageNotes) != 1 || !strings.Contains(page.PackageNotes[0].Note, "broken.go") {
		t.Fatalf("notes: %+v", page.PackageNotes)
	}
	b := newShapeBuilder(table(t), "go")
	b.add("func Big() {\n\tx := 1\n", 101, true)
	if s := b.finish(102, 0, false); len(s.Unchecked) != 1 || s.Unchecked[0].Why != UncheckedLarge {
		t.Fatalf("a cut segment: %+v", s.Unchecked)
	}
	b = newShapeBuilder(table(t), "go")
	b.add(broken, 1)
	if s := b.finish(5, 0, false); len(s.Unchecked) != 1 || s.Unchecked[0].Why != UncheckedNoParse {
		t.Fatalf("a whole file that does not parse: %+v", s.Unchecked)
	}
}

func TestANewlineFreeSegmentIsCutInsideIt(t *testing.T) {
	l := table(t).Langs["go"]
	for _, shift := range []int{0, 1, 2} {
		buf := strings.Repeat("a", shift) + strings.Repeat("界", (SegmentBytes-shift)/3+1)
		buf = buf[:SegmentBytes]
		n, _, limited := cutSegment(buf, l, 0, false)
		if n <= 0 || n > len(buf) || !limited || !utf8.ValidString(buf[:n]) {
			t.Fatalf("shift %d: cut %d of %d, limited %v, valid %v", shift, n, len(buf), limited, utf8.ValidString(buf[:min(n, len(buf))]))
		}
	}
	dir := t.TempDir()
	src := "package p\n\nvar s = `" + strings.Repeat("界", 140000) + "`\n\nfunc F() string { return s }\n"
	if err := os.WriteFile(filepath.Join(dir, "raw.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, _, err := PlanFile(table(t), &OSFS{Root: dir}, "raw.go", "raw.go", "go", ChunkBytes, false)
	if err != nil || cur.Size != int64(len(src)) || len(cur.Segs) < 3 {
		t.Fatalf("plan: %v, %+v", err, cur)
	}
	for k := range cur.Segs {
		seg, err := cur.segmentAt(&OSFS{Root: dir}, "raw.go", k)
		if err != nil || !utf8.ValidString(seg) {
			t.Fatalf("segment %d: %v, valid %v", k, err, utf8.ValidString(seg))
		}
	}
}

func TestTheSummaryCountsSizeFromShapes(t *testing.T) {
	lines := func(n int) FileShape { return FileShape{Lines: n} }
	fold := NewSummaryFold(10)
	fold.AddRun([]Record{ShapeRecord("b.go", "d1", lines(1001))})
	fold.AddRun([]Record{ShapeRecord("c.go", "d2", lines(10)), ShapeRecord("c.go", "d2", lines(2001))})
	fold.AddRun([]Record{ShapeRecord("t_test.go", "d3", FileShape{Lines: 5000, Test: true})})
	got := fold.Result()["size_findings"].(map[string]int)
	if got["large_file"] != 1 || got["very_large_file"] != 1 {
		t.Fatalf("size_findings %v", got)
	}
}

func TestALargeFileEditedBetweenStepsIsNotFinishedAsPlanned(t *testing.T) {
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
			final := src
			edited := false
			hook := func(step int, s *Scan) *Scan {
				if s.File != nil && s.File.Next >= 2 && !edited {
					edited, final = true, edit(src)
					if err := os.WriteFile(file, []byte(final), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				return s
			}
			j := &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12}
			s, m := scanOne(t, dir, j, 12, hook)
			if !edited || s.Status != "done" {
				t.Fatalf("edited %v, status %s", edited, s.Status)
			}
			sum := sha256.Sum256([]byte(final))
			want := hex.EncodeToString(sum[:])
			var c struct {
				Entries []Coverage `json:"entries"`
			}
			_ = json.Unmarshal([]byte(reviewJSON(t, m, ReviewQuery{Shape: "coverage"})), &c)
			finished := 0
			for _, e := range c.Entries {
				if contains(e.Reasons, "source_changed") {
					if e.SourceDigest == want {
						t.Fatalf("the current digest is called changed: %+v", e)
					}
					continue
				}
				finished++
				if e.SourceDigest != want || e.Size != int64(len(final)) {
					t.Fatalf("finished under %s (%d bytes); the file reads %s (%d bytes)", e.SourceDigest, e.Size, want, len(final))
				}
			}
			if finished != 1 {
				t.Fatalf("%d coverages not stale: %+v", finished, c.Entries)
			}
		})
	}
}

var _ RangeFS = FS(nil)

func TestAFileOverOneSegmentIsReadByRange(t *testing.T) {
	src, _ := goOfSize(3 * SegmentBytes)
	f := &scopedRulesFS{dirs: map[string][]Entry{"repo": {{Name: "big.go", Size: int64(len(src))}}}, files: map[string]string{"repo/big.go": src}}
	s, err := NewScan(f, "s", "", "repo", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := &fakeJudge{p: 0.1}
	for i := 0; i < 200 && s.Status == "running"; i++ {
		if _, err := s.Step(table(t), f, j, MemCache{}, 64, 400); err != nil {
			t.Fatal(err)
		}
	}
	if s.Status != "done" || s.FilesJudged != 1 || len(s.Excluded) != 0 {
		t.Fatalf("status %s, judged %d, excluded %v", s.Status, s.FilesJudged, s.Excluded)
	}
}

type outlineStopJudge struct {
	*fakeJudge
	err     error
	stopped bool
}

func (o *outlineStopJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	if _, outline := state["outline"]; outline && !o.stopped {
		o.stopped = true
		return nil, o.err
	}
	return o.fakeJudge.Ask(state, qs)
}

func TestALargeFilesOutlineIsAskedWhateverStopsIt(t *testing.T) {
	for name, stop := range map[string]error{
		"judge":      fmt.Errorf("judge unavailable: 503 (%w)", ErrJudge),
		"step calls": fmt.Errorf("%w (%w)", ErrStepCalls, ErrJudge),
		"key":        ErrKey,
		"cache":      fmt.Errorf("%w: a listing cut short", ErrCache),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src, _ := goOfSize(2 * SegmentBytes)
			if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			j := &outlineStopJudge{fakeJudge: &fakeJudge{p: 0.1}, err: stop}
			m := newMemStore()
			m.files["journal"], m.files["intent"] = nil, nil
			s, err := NewScan(&OSFS{Root: dir}, "s", ".", "", nil, nil, false, 100, SegmentBytes)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 200 && (s.Status == "running" || s.Status == "judge_unavailable"); i++ {
				s.AskIntent = true
				s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"},
					Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
				_, _ = s.Step(table(t), &OSFS{Root: dir}, j, nil, 400, 400)
			}
			if !j.stopped || s.Status != "done" {
				t.Fatalf("stopped %v, status %s", j.stopped, s.Status)
			}
			var outlines []Record
			var planned *FileShape
			if _, err := (&Journal{Store: m, Path: "intent"}).Walk(0, func(e JournalEvent) error {
				if e.Record != nil && e.Record.Battery == OutlineBattery {
					outlines = append(outlines, *e.Record)
				}
				if e.Record != nil && e.Record.Battery == ShapeBattery {
					planned = e.Record.Shape
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(outlines) != 1 || !batteryAnswered(outlines[0]) || contains(outlines[0].ReasonCodes, IntentFailed) {
				t.Fatalf("outline records %+v", outlines)
			}

			asked := ""
			for _, st := range j.seen {
				if o, ok := st["outline"].(string); ok {
					asked = o
				}
			}
			if planned == nil || asked == "" || asked != OutlineText(planned.Outline) || outlines[0].Lines[1] != planned.Lines {
				t.Fatalf("the outline asked is not the planned shape's (%d bytes asked, lines %v)", len(asked), outlines[0].Lines)
			}
		})
	}
}

func TestAWindowWithNoCodeIsCountedApart(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\n// Add returns a plus b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "// Note %d: the table below is read once at start and never written again.\n", i)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s, m := scanOne(t, dir, &fakeJudge{p: 0.05}, 64, nil)
	if s.Status != "done" {
		t.Fatal(s.Status)
	}
	fold := NewSummaryFold(10)
	var files []FileReview
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(run *FileRun) error {
		records := DeduplicateRecords(run.Records)
		for _, r := range records {
			fold.Add(r)
		}
		fold.AddRun(records, run.Coverage...)
		files = append(files, BuildReview(records, run.Coverage, true, true, "slop").Files...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files %+v", files)
	}
	f := files[0]

	if f.Coverage == nil || f.NoCode != 1 || f.Coverage.CompletedChunks+f.NoCode != f.Coverage.ExpectedChunks ||
		strings.Join(f.Reasons, ",") != "context_limited" {
		t.Fatalf("no code %d, coverage %+v, reasons %v", f.NoCode, f.Coverage, f.Reasons)
	}
	if got := fold.Result()["no_code_windows"]; got != f.NoCode {
		t.Fatalf("summary no_code_windows %v, the file's %d", got, f.NoCode)
	}
}

func TestASummaryCountsEachWindowOnce(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\n// Add returns a plus b.\nfunc Add(a, b int) int {\n\tif a > b {\n\t\treturn a - b\n\t}\n\treturn a + b\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, m := scanOne(t, dir, &fakeJudge{p: 0.05}, 64, nil)
	var recs []Record
	fold := NewSummaryFold(10)
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(run *FileRun) error {
		records := DeduplicateRecords(run.Records)
		for _, r := range records {
			fold.Add(r)
		}
		fold.AddRun(records, run.Coverage...)
		recs = append(recs, records...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, sum := range map[string]map[string]any{"fold": fold.Result(), "whole": Summary(recs, 10)} {
		lang := sum["languages"].(map[string]any)["go"].(map[string]any)
		if sum["chunks_judged"] != s.Chunks || sum["lines"] != 9 || lang["chunks"] != s.Chunks || lang["lines"] != 9 {
			t.Fatalf("%s: chunks %v lines %v, go %v; the scan judged %d window of 9 lines", name, sum["chunks_judged"], sum["lines"], lang, s.Chunks)
		}
	}
}

func TestSlopQuestionsCarryTheirOwnNames(t *testing.T) {
	_, m, _ := slopScan(t, map[string]string{"p/planted.go": planted, "p/control.go": control}, nil)
	intent := &Journal{Store: m, Path: "intent"}
	sum := NewSummaryFold(10)
	cursor := NewIntentCursor(intent)
	if _, err := FoldJournal(&Journal{Store: m, Path: "journal"}, func(run *FileRun) error {
		records := DeduplicateRecords(run.Records)
		for _, r := range records {
			sum.Add(r)
		}
		extra, _, _ := cursor.Take(run.Ref)
		sum.AddRun(append(records, extra...), run.Coverage...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sum.Result()["slop"])
	var slop struct {
		Questions map[string]int `json:"questions"`
	}
	_ = json.Unmarshal(b, &slop)
	if strings.Contains(string(b), "intent") || slop.Questions["asked"] == 0 || slop.Questions["asked"] != slop.Questions["answered"]+slop.Questions["failed"] {
		t.Fatalf("summary slop: %s", b)
	}
	_, page := slopPage(t, m, DefaultThresholds)
	if strings.Contains(string(page.Entries), "intent_asked") || !strings.Contains(string(page.Entries), `"slop_questions":{"asked":`) {
		t.Fatalf("slop lane entries: %s", page.Entries)
	}
}

func TestACallTheStepRefusedIsNotCounted(t *testing.T) {
	dir := t.TempDir()
	src, _ := goOfSize(2 * SegmentBytes)
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12}
	s, _ := scanOne(t, dir, j, 12, nil)
	if s.Status != "done" || j.used == 0 {
		t.Fatalf("status %s", s.Status)
	}
	if made := j.plantJudge.calls; s.Calls != made {
		t.Fatalf("the scan counts %d calls; %d were sent", s.Calls, made)
	}
}

type firstAnswersJudge struct {
	fakeJudge
	asked int
}

func (f *firstAnswersJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	if f.asked++; f.asked > 1 {
		return nil, fmt.Errorf("judge unavailable: 503 (%w)", ErrJudge)
	}
	return f.fakeJudge.Ask(state, qs)
}

func TestAWindowStoppedBeforeItsDocsIsNotComplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(progressSource), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(&OSFS{Root: dir}, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := &firstAnswersJudge{fakeJudge: fakeJudge{p: 0.99}}
	s.AskIntent = true
	s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"}}
	if _, err := s.Step(table(t), &OSFS{Root: dir}, j, nil, 64, 400); err != nil {
		t.Fatal(err)
	}
	var last *Coverage
	var docs int
	if _, err := (&Journal{Store: m, Path: "journal"}).Walk(0, func(e JournalEvent) error {
		if e.Coverage != nil && e.Coverage.Ref == "a.go" {
			last = e.Coverage
		}
		if e.Record != nil && e.Record.Battery == "docs" {
			docs++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if s.Status != "judge_unavailable" || j.asked < 2 || docs != 0 || last == nil {
		t.Fatalf("status %s, asked %d, docs records %d, coverage %+v", s.Status, j.asked, docs, last)
	}
	if last.CompletedChunks != 0 {
		t.Fatalf("a window whose docs were never asked is counted complete: %+v", *last)
	}
}
