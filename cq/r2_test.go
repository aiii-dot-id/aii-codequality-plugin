package cq

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type plantJudge struct {
	seen  map[string]bool
	calls int
}

func (p *plantJudge) Model() string { return "fake" }
func (p *plantJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	p.calls++
	code, _ := state["code"].(string)
	for _, line := range strings.Split(code, "\n") {
		if strings.HasPrefix(line, "func ") {
			p.seen[strings.Fields(line)[1]] = true
		}
	}
	out := map[string]float64{}
	for id := range qs {
		out[id] = 0.05
		if id == "EH-01" && strings.Contains(code, "Planted") {
			out[id] = 0.95
		}
	}
	return out, nil
}
func (p *plantJudge) Choose(state, qs map[string]any) (map[string]Choice, error) {
	p.calls++
	out := map[string]Choice{}
	for id, q := range qs {
		pick := "cannot_localize"
		for k := range q.(map[string]any)["criteria"].(map[string]any) {
			if strings.Contains(k, "func Planted") {
				pick = k
			}
		}
		out[id] = Choice{Choice: pick, Confidence: 0.9}
	}
	return out, nil
}

func goOfSize(n int) (string, int) {
	var b strings.Builder
	b.WriteString("package big\n\nimport \"os\"\n\n")
	last := "// Planted closes f.\nfunc Planted(f *os.File) {\n\t_ = f.Close()\n}\n"
	k := 0
	for b.Len()+len(last)+400 < n {
		fmt.Fprintf(&b, "// F%d returns x plus %d.\nfunc F%d(x int) int {\n\tif x < 0 {\n\t\treturn -x + %d\n\t}\n\treturn x + %d\n}\n\n", k, k, k, k, k)
		k++
	}
	b.WriteString(last)
	pad := n - b.Len() - 4
	b.WriteString("// " + strings.Repeat("x", pad) + "\n")
	return b.String(), k
}

func scanOne(t *testing.T, dir string, j Judge, calls int, before func(step int, s *Scan) *Scan) (*Scan, *memStore) {
	t.Helper()
	m := newMemStore()
	m.files["journal"], m.files["intent"] = nil, nil
	s, err := NewScan(&OSFS{Root: dir}, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000 && (s.Status == "running" || s.Status == "judge_unavailable"); i++ {
		if before != nil {
			s = before(i, s)
		}
		if c, ok := j.(interface{ newStep() }); ok {
			c.newStep()
		}
		s.AskIntent = true
		s.Progress = &JournalProgress{Journal: &Journal{Store: m, Path: "journal"}, Intent: &Journal{Store: m, Path: "intent"},
			Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		if _, err := s.Step(table(t), &OSFS{Root: dir}, j, nil, calls, 400); err != nil {
			t.Fatal(err)
		}
	}
	return s, m
}

func reviewJSON(t *testing.T, m *memStore, q ReviewQuery) string {
	t.Helper()
	q.Generation, q.PageBytes, q.Limit, q.ScanDone = "g", 200000, 100, true
	q.IntentFile = &Journal{Store: m, Path: "intent"}
	page, err := ReviewFold(&Journal{Store: m, Path: "journal"}, q)
	if err != nil {
		t.Fatal(err)
	}
	b, err := page.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAOneMiBGoFileIsJudgedWhole(t *testing.T) {
	dir := t.TempDir()
	src, n := goOfSize(MaxSourceBytes)
	if len(src) != MaxSourceBytes {
		t.Fatalf("fixture %d bytes", len(src))
	}
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &plantJudge{seen: map[string]bool{}}
	s, m := scanOne(t, dir, j, 64, nil)
	if s.Status != "done" || s.FilesJudged != 1 || len(s.Excluded) != 0 || s.File != nil {
		t.Fatalf("scan %s, judged %d, excluded %v", s.Status, s.FilesJudged, s.Excluded)
	}
	for k := 0; k < n; k++ {
		if !j.seen[fmt.Sprintf("F%d(x", k)] {
			t.Fatalf("function F%d never reached the judge", k)
		}
	}
	if !j.seen["Planted(f"] {
		t.Fatal("the last function never reached the judge")
	}
	queue := reviewJSON(t, m, ReviewQuery{Shape: "review_queue", Lane: "correctness"})
	var page struct {
		Entries []FileReview `json:"entries"`
	}
	_ = json.Unmarshal([]byte(queue), &page)
	if len(page.Entries) != 1 || page.Entries[0].Tier != TierPinned || !strings.Contains(page.Entries[0].Findings[0].Where, "func Planted") {
		t.Fatalf("the planted defect: %s", queue)
	}

	cov := reviewJSON(t, m, ReviewQuery{Shape: "coverage"})
	var c struct {
		Entries []Coverage `json:"entries"`
	}
	_ = json.Unmarshal([]byte(cov), &c)
	if len(c.Entries) != 1 || c.Entries[0].ExpectedChunks == 0 || c.Entries[0].ExpectedChunks != c.Entries[0].CompletedChunks ||
		strings.Join(c.Entries[0].Reasons, ",") != "context_limited" {
		t.Fatalf("coverage: %s", cov)
	}
	t.Logf("1 MiB fixture: %d functions, %d segments judged, %d judge calls", n+1, s.Chunks, j.calls)
}

func TestOneByteOverTheCeilingIsOutOfScope(t *testing.T) {
	dir := t.TempDir()
	src, _ := goOfSize(MaxSourceBytes + 1)
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &plantJudge{seen: map[string]bool{}}
	s, m := scanOne(t, dir, j, 64, nil)
	if s.Status != "done" || s.FilesJudged != 0 || s.Excluded[LargerThanSource] != 1 || j.calls != 0 {
		t.Fatalf("scan %s, judged %d, excluded %v, %d calls", s.Status, s.FilesJudged, s.Excluded, j.calls)
	}
	triage := reviewJSON(t, m, ReviewQuery{Shape: "triage"})
	if !strings.Contains(triage, `"reason":"larger_than_1_mib"`) || !strings.Contains(triage, "not treated as a source file") ||
		!strings.Contains(triage, fmt.Sprintf(`"size":%d`, MaxSourceBytes+1)) || strings.Contains(triage, "oversize") {
		t.Fatalf("triage: %s", triage)
	}
}

func TestAKillOrADroppedCursorBetweenSegmentsCountsNothingTwice(t *testing.T) {
	dir := t.TempDir()
	src, _ := goOfSize(MaxSourceBytes)
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	capped := func() Judge { return &cappedJudge{plantJudge: &plantJudge{seen: map[string]bool{}}, cap: 12} }
	whole, wm := scanOne(t, dir, capped(), 12, nil)
	want := reviewJSON(t, wm, ReviewQuery{Shape: "review_queue", Tier: TierAll})
	if whole.Status != "done" {
		t.Fatal(whole.Status)
	}
	var killed, dropped bool
	var saved []byte
	kill := func(step int, s *Scan) *Scan {
		if s.File != nil && s.File.Next >= 1 && !killed {
			if saved == nil {
				saved, _ = json.Marshal(s)
				return s
			}
			killed = true
			var back Scan
			if err := json.Unmarshal(saved, &back); err != nil {
				t.Fatal(err)
			}
			return &back
		}
		return s
	}
	ks, km := scanOne(t, dir, capped(), 12, kill)
	drop := func(step int, s *Scan) *Scan {
		if s.File != nil && s.File.Next >= 2 && !dropped {
			dropped = true
			s.File = nil
		}
		return s
	}
	ds, dm := scanOne(t, dir, capped(), 12, drop)
	if !killed || !dropped || ks.Status != "done" || ds.Status != "done" {
		t.Fatalf("killed %v %s, dropped %v %s", killed, ks.Status, dropped, ds.Status)
	}
	for name, got := range map[string]string{"killed": reviewJSON(t, km, ReviewQuery{Shape: "review_queue", Tier: TierAll}),
		"dropped": reviewJSON(t, dm, ReviewQuery{Shape: "review_queue", Tier: TierAll})} {
		if got != want {
			t.Errorf("%s: the review differs from one run straight through:\n%s\n%s", name, got, want)
		}
	}

	for name, m := range map[string]*memStore{"straight": wm, "killed": km, "dropped": dm} {
		var c struct {
			Entries []Coverage `json:"entries"`
		}
		_ = json.Unmarshal([]byte(reviewJSON(t, m, ReviewQuery{Shape: "coverage"})), &c)
		if len(c.Entries) != 1 || c.Entries[0].ExpectedChunks == 0 || c.Entries[0].ExpectedChunks != c.Entries[0].CompletedChunks ||
			strings.Join(c.Entries[0].Reasons, ",") != "context_limited" {
			t.Errorf("%s: coverage %+v", name, c.Entries)
		}
	}
}

func TestFileSizeIsAFindingWithNoCall(t *testing.T) {
	dir := t.TempDir()
	lines := func(n int) string {
		var b strings.Builder
		b.WriteString("package p\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "var v%d = %d\n", i, i)
		}
		return b.String()
	}
	for name, n := range map[string]int{"a.go": 1000, "b.go": 1001, "c.go": 2001} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(lines(n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	j := &fakeJudge{p: 0.05}
	s, m := scanOne(t, dir, j, 64, nil)
	if s.Status != "done" || s.FilesJudged != 3 {
		t.Fatalf("scan %s judged %d", s.Status, s.FilesJudged)
	}
	if want := s.Calls - s.IntentCalls; j.calls != want {
		t.Fatalf("%d judge calls, the batteries account for %d", j.calls, want)
	}
	page := reviewJSON(t, m, ReviewQuery{Shape: "triage", Lane: "size"})
	var p struct {
		Entries []TriageRow `json:"entries"`
	}
	_ = json.Unmarshal([]byte(page), &p)
	if len(p.Entries) != 2 || p.Entries[0].Ref != "c.go" || p.Entries[0].Size.Finding != "very_large_file" || p.Entries[0].Size.Severity != "high" ||
		p.Entries[1].Ref != "b.go" || p.Entries[1].Size.Finding != "large_file" || p.Entries[1].Size.Lines != 1001 ||
		p.Entries[0].Tier != "" || p.Entries[0].Size.TopLevel == nil {
		t.Fatalf("size lane: %s", page)
	}
	pinned := reviewJSON(t, m, ReviewQuery{Shape: "triage"})
	if strings.Contains(pinned, `"ref":"c.go"`) {
		t.Fatalf("a clean large file reached the correctness tier: %s", pinned)
	}
	other := reviewJSON(t, m, ReviewQuery{Shape: "triage", Lane: "size", Size: SizeThresholds{Large: 5000, VeryLarge: 9000}})
	if strings.Contains(other, `"ref":"c.go"`) {
		t.Fatalf("a changed threshold did not apply without a rescan: %s", other)
	}
}

func TestScopeIsTheFilesKind(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"nul.go":       "package p\x00\n",
		"bad.go":       "package p\n// \xff\xfe\n",
		"data.unknown": "some words\n",
		"gen.go":       "// Code generated by tool. DO NOT EDIT.\n\npackage p\n\nvar X = 1\n",
		"min.js":       strings.Repeat("var a=1;", 40) + "\n",
		"ok.go":        "package p\n\n// F is f.\nfunc F() int { return 1 }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	j := &fakeJudge{p: 0.05}
	s, m := scanOne(t, dir, j, 64, nil)
	if s.FilesJudged != 1 || s.Excluded["not_text"] != 2 || s.Excluded["language_not_recognised"] != 1 || s.Excluded["generated"] != 1 || s.Excluded["minified"] != 1 {
		t.Fatalf("judged %d, excluded %v", s.FilesJudged, s.Excluded)
	}
	triage := reviewJSON(t, m, ReviewQuery{Shape: "triage"})
	for _, want := range []string{`"reason":"not_text"`, `"reason":"language_not_recognised"`, `"reason":"generated"`, "mean line length over 110 bytes"} {
		if !strings.Contains(triage, want) {
			t.Fatalf("triage lacks %s: %s", want, triage)
		}
	}
	if !strings.Contains(triage, `"inventory_complete":true`) {
		t.Fatalf("an out-of-scope file left the inventory incomplete: %s", triage)
	}

	m2 := newMemStore()
	m2.files["journal"], m2.files["intent"] = nil, nil
	g, _ := NewScan(&OSFS{Root: dir}, "g", ".", "", nil, nil, false, 100, SegmentBytes)
	g.IncludeGenerated = true
	for i := 0; i < 50 && g.Status == "running"; i++ {
		g.Progress = &JournalProgress{Journal: &Journal{Store: m2, Path: "journal"}, Offset: g.ProgressOffset}
		if _, err := g.Step(table(t), &OSFS{Root: dir}, j, nil, 64, 400); err != nil {
			t.Fatal(err)
		}
	}
	if g.FilesJudged != 3 || g.Excluded["generated"] != 0 || g.Excluded["minified"] != 0 {
		t.Fatalf("include_generated: judged %d, excluded %v", g.FilesJudged, g.Excluded)
	}
}

func TestAFilesLanguagesAreAHint(t *testing.T) {
	tb := table(t)
	if got := tb.FileLanguages("html", []byte("<html><script>var a=1</script><style>p{}</style></html>")); strings.Join(got, ",") != "html,css,javascript" {
		t.Fatalf("html: %v", got)
	}
	if got := tb.FileLanguages("markdown", []byte("# x\n```go\nfunc F(){}\n```\n```py\nx=1\n```\n")); strings.Join(got, ",") != "markdown,go,python" {
		t.Fatalf("markdown: %v", got)
	}
	if got := tb.FileLanguages("go", []byte("package p\n")); strings.Join(got, ",") != "go" {
		t.Fatalf("go: %v", got)
	}
	var states []map[string]any
	j := &recordingJudge{states: &states}
	recs, err := JudgeTextProgress(tb, j, nil, "a.html", "html", "<html><body><p>x</p><script>var a = 1;</script></body></html>\n", ChunkBytes, nil, nil, false, "html", "javascript")
	if err != nil || len(recs) == 0 {
		t.Fatal(err)
	}
	if recs[0].Language != "html+javascript" || states[0]["language"] != "html" || fmt.Sprint(states[0]["languages"]) != "[html javascript]" {
		t.Fatalf("label %q, state %v", recs[0].Language, states[0])
	}
}

type recordingJudge struct{ states *[]map[string]any }

func (r *recordingJudge) Model() string { return "fake" }
func (r *recordingJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	*r.states = append(*r.states, state)
	out := map[string]float64{}
	for id := range qs {
		out[id] = 0.05
	}
	return out, nil
}
func (r *recordingJudge) Choose(state, qs map[string]any) (map[string]Choice, error) {
	return map[string]Choice{}, nil
}

type cappedJudge struct {
	*plantJudge
	cap, used int
}

func (c *cappedJudge) newStep() { c.used = 0 }
func (c *cappedJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	if c.used++; c.used > c.cap {
		return nil, fmt.Errorf("%w (%w, %w)", ErrStepCalls, ErrJudge, ErrNotSent)
	}
	return c.plantJudge.Ask(state, qs)
}
func (c *cappedJudge) Choose(state, qs map[string]any) (map[string]Choice, error) {
	if c.used++; c.used > c.cap {
		return nil, fmt.Errorf("%w (%w, %w)", ErrStepCalls, ErrJudge, ErrNotSent)
	}
	return c.plantJudge.Choose(state, qs)
}
