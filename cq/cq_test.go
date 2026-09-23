//go:build !wasm_unknown

package cq

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func table(t *testing.T) *Table {
	t.Helper()
	tb, err := LoadTable()
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestDetect(t *testing.T) {
	tb := table(t)
	for name, k := range specialFilenames {
		if tb.Langs[k] == nil {
			t.Errorf("special filename %s names %q, which the table does not have", name, k)
		}
	}
	for rel, want := range map[string]string{
		"a/b/main.go": "go", "x.py": "python", "Dockerfile": "dockerfile", "src/app.ts": "typescript",
		"Foo.java": "java", "lib.rs": "rust", "k.c": "c", "notes.xyz": "",
	} {
		if got := tb.Detect(rel, nil); got != want {
			t.Errorf("Detect(%q) = %q, want %q", rel, got, want)
		}
	}
	if got := tb.Detect("x.h", []byte("@interface Foo\n")); got != "objc" {
		t.Errorf(".h with @interface = %q", got)
	}
	if got := tb.Detect("tool", []byte("#!/usr/bin/env python3\nprint(1)\n")); got != "python" {
		t.Errorf("python shebang = %q", got)
	}
}

func TestBlankCommentsKeepsLinesAndStrings(t *testing.T) {
	tb := table(t)
	src := "package x\n\n// Doc says hi.\nfunc F() string { /* inline\nblock */ return \"a // not a comment\" + `raw /* x */` }\n"
	b, c, lost := BlankComments(src, tb.Langs["go"])
	if strings.Count(b, "\n") != strings.Count(src, "\n") {
		t.Fatalf("lines changed:\n%s", b)
	}
	if strings.Contains(b, "Doc says") || strings.Contains(b, "inline") {
		t.Fatalf("comment left in code:\n%s", b)
	}
	if !strings.Contains(b, `"a // not a comment"`) || !strings.Contains(b, "`raw /* x */`") {
		t.Fatalf("string damaged:\n%s", b)
	}
	if !strings.Contains(c, "Doc says hi.") || !strings.Contains(c, "inline\nblock") || lost {
		t.Fatalf("comments %q lost %v", c, lost)
	}
	py := "def f(x):\n    \"\"\"Docstring.\"\"\"\n    s = '# no'  # yes\n    return s\n"
	b, c, _ = BlankComments(py, tb.Langs["python"])
	if strings.Contains(b, "Docstring") || strings.Contains(b, "# yes") || !strings.Contains(b, "'# no'") {
		t.Fatalf("python blanking:\n%s", b)
	}
	if !strings.Contains(c, "Docstring") || !strings.Contains(c, "# yes") {
		t.Fatalf("python comments %q", c)
	}
	rs := "fn f<'a>(c: char) -> bool { c == '\"' } // q\n"
	b, _, lost = BlankComments(rs, tb.Langs["rust"])
	if strings.Contains(b, "// q") || lost {
		t.Fatalf("rust char literal / lifetime: %q lost=%v", b, lost)
	}
}

func TestRedact(t *testing.T) {
	// The fixtures are assembled from pieces so that no secret scanner mistakes this file for a leak.
	pem := "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIx\n-----END RSA " + "PRIVATE KEY-----\n"
	in := "key = \"AKIA" + "ABCDEFGHIJKLMNOP\"\napi_key: 'abcdefghijklmnop123'\n" + pem + "ok := 1\n"
	out, n := Redact(in)
	if n != 3 || strings.Contains(out, "AKIA") || strings.Contains(out, "MIIx") || !strings.Contains(out, "ok := 1") {
		t.Fatalf("n=%d\n%s", n, out)
	}
}

func TestScoreBands(t *testing.T) {
	none := map[string]float64{}
	for _, it := range CodeItems("go") {
		none[it.ID] = 0.05
	}
	_, _, _, idx, _ := Score("go", none, false)
	if idx != 100 || Band(idx) != "clean" {
		t.Fatalf("all absent: %d", idx)
	}
	all := map[string]float64{}
	for _, it := range append(CodeItems("go"), DocItems()...) {
		all[it.ID] = 0.99
	}
	f, _, dims, idx, _ := Score("go", all, true)
	if Band(idx) != "severe" || len(f) != 25 || dims["error_handling"] != 0 {
		t.Fatalf("all present: idx %d findings %d dims %v", idx, len(f), dims)
	}
	half := map[string]float64{}
	for _, it := range CodeItems("go") {
		half[it.ID] = 0.5
	}
	_, ab, _, _, unc := Score("go", half, false)
	if len(ab) != 20 || !unc {
		t.Fatalf("abstentions %d uncertain %v", len(ab), unc)
	}
	for _, want := range []string{"clean", "minor", "moderate", "severe"} {
		found := false
		for idx := 0; idx <= 100; idx++ {
			if Band(idx) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("band %s unreachable", want)
		}
	}
}

func TestChunks(t *testing.T) {
	if c := Chunks("a\nb\n"); len(c) != 1 || c[0].LastLine != 3 {
		t.Fatalf("%+v", c)
	}
	var b strings.Builder
	for b.Len() < 3*ChunkBytes {
		b.WriteString("x := compute(value)\n\n")
	}
	cs := Chunks(b.String())
	if len(cs) < 3 {
		t.Fatalf("%d chunks", len(cs))
	}
	next := 1
	joined := ""
	for _, c := range cs {
		if len(c.Text) > ChunkBytes || c.FirstLine != next {
			t.Fatalf("chunk %+v (len %d) want first line %d", c.FirstLine, len(c.Text), next)
		}
		next = c.LastLine
		joined += c.Text
	}
	if joined != b.String() {
		t.Fatal("chunks do not reassemble the text")
	}
}

// fakeJudge answers every question with p, or fails.
type fakeJudge struct {
	p     float64
	fail  bool
	calls int
	seen  []map[string]any
}

func (f *fakeJudge) Model() string { return "fake" }
func (f *fakeJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	f.calls++
	f.seen = append(f.seen, state)
	if f.fail {
		return nil, errors.New("judge unavailable: 503")
	}
	out := map[string]float64{}
	for id := range qs {
		out[id] = f.p
	}
	return out, nil
}

type failJudge struct{ fakeJudge }

func (f *failJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	f.calls++
	return nil, ErrJudge
}

func TestJudgeTextCallsCacheAndSkips(t *testing.T) {
	tb := table(t)
	j := &fakeJudge{p: 0.1}
	c := MemCache{}
	src := "package x\n\n// Adds.\nfunc Add(a, b int) int {\n\treturn a + b + 1000000 + len(\"some text that makes it long enough\")\n}\n"
	recs, err := JudgeText(tb, j, c, "x.go", "go", src)
	if err != nil || len(recs) != 1 || recs[0].Calls != 2 || recs[0].Band != "clean" {
		t.Fatalf("%v %+v", err, recs)
	}
	if st := j.seen[0]; st["language"] != "go" || strings.Contains(st["code"].(string), "Adds.") {
		t.Fatalf("code state carries the comment: %v", st)
	}
	recs, _ = JudgeText(tb, j, c, "x.go", "go", src)
	if recs[0].Calls != 0 || recs[0].Cached != 2 || j.calls != 2 {
		t.Fatalf("cache: calls %d cached %d judge %d", recs[0].Calls, recs[0].Cached, j.calls)
	}
	recs, _ = JudgeText(tb, j, c, "t.go", "go", "package x\n")
	if recs[0].Skipped != "too_small" || j.calls != 2 {
		t.Fatalf("tiny file judged: %+v", recs[0])
	}
	recs, _ = JudgeText(tb, j, c, "README.md", "markdown", "# Title\n\nSome prose.\n")
	if recs[0].Calls != 1 || recs[0].Dimensions["documentation"] != 100 {
		t.Fatalf("markdown: %+v", recs[0])
	}
}

func TestParseAnswers(t *testing.T) {
	a, err := ParseAnswers([]byte(`{"answers":{"EH-01":{"type":"noul","noul":0.83}},"usage":{"input_tokens":10}}`))
	if err != nil || a["EH-01"] != 0.83 {
		t.Fatal(a, err)
	}
	for _, bad := range []string{`not json`, `{"answers":{}}`, `{"answers":{"X":{"type":"noul"}}}`, `{"answers":{"X":{"noul":1.5}}}`} {
		if _, err := ParseAnswers([]byte(bad)); !errors.Is(err, ErrJudge) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goFile = "package x\n\nfunc Add(a, b int) int {\n\treturn a + b + len(\"padding so the chunk is not trivially small\")\n}\n"

func TestScanWalksAndExcludes(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "repo/.gitignore", "build/\n*.log\n")
	write(t, root, "repo/.gitattributes", "third/** linguist-vendored\n")
	write(t, root, "repo/a.go", goFile)
	write(t, root, "repo/go.mod", "module example.com/x\n\ngo 1.25\n")
	write(t, root, "repo/pkg/b.go", goFile+"\n")
	write(t, root, "repo/pkg/b_test.go", goFile)
	write(t, root, "repo/pkg/gen.go", "// Code generated by x. DO NOT EDIT.\n"+goFile)
	write(t, root, "repo/build/c.go", goFile)
	write(t, root, "repo/third/d.go", goFile)
	write(t, root, "repo/node_modules/e.js", "var x = 1;\n")
	write(t, root, "repo/.env", "FIXTURE=not-a-secret\n")
	write(t, root, "repo/debug.log", "x\n")
	write(t, root, "repo/blob.go", "package x\x00\n")
	write(t, root, "repo/skip/f.go", goFile)
	write(t, root, "repo/.git/HEAD", "ref: refs/heads/main\n")
	write(t, root, "repo/wt/other/.git", "gitdir: /elsewhere\n")
	write(t, root, "repo/wt/other/g.go", goFile)
	if err := os.Symlink(filepath.Join(root, "repo/a.go"), filepath.Join(root, "repo/link.go")); err != nil {
		t.Fatal(err)
	}
	fs := &OSFS{Root: root}
	s, err := NewScan(fs, "cq_1", "src", "repo", []string{"skip/"}, nil, false, 100, 131072)
	if err != nil {
		t.Fatal(err)
	}
	j := &fakeJudge{p: 0.1}
	var all []Record
	for i := 0; i < 20 && s.Status == "running"; i++ {
		recs, err := s.Step(tb, fs, j, MemCache{}, 2, 50) // two calls: one file per step
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) > 1 {
			t.Fatalf("step judged %d files with a two-call budget", len(recs))
		}
		all = append(all, recs...)
	}
	if s.Status != "done" {
		t.Fatalf("status %s", s.Status)
	}
	var refs []string
	for _, r := range all {
		refs = append(refs, r.Ref)
	}
	if strings.Join(refs, ",") != "a.go,pkg/b.go" {
		t.Fatalf("judged %v; excluded %v", refs, s.Excluded)
	}
	for _, why := range []string{"gitignore", "gitattributes", "global_dir", "secret_named", "nul_bytes", "symlink", "test", "generated", "caller_exclude", "nested_repository"} {
		if s.Excluded[why] == 0 {
			t.Errorf("no %s exclusion: %v", why, s.Excluded)
		}
	}
}

func TestScanJudgeFailureKeepsCursor(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "a.go", goFile)
	write(t, root, "b.go", goFile+"\n")
	fs := &OSFS{Root: root}
	s, _ := NewScan(fs, "cq_2", "src", "", nil, nil, false, 100, 131072)
	bad := &failJudge{}
	recs, err := s.Step(tb, fs, bad, MemCache{}, 6, 50)
	if err != nil || len(recs) != 0 || s.Status != "judge_unavailable" || len(s.Pending) != 2 || s.FilesSeen != 0 {
		t.Fatalf("err %v recs %d status %s pending %d seen %d", err, len(recs), s.Status, len(s.Pending), s.FilesSeen)
	}
	recs, err = s.Step(tb, fs, &fakeJudge{p: 0.9}, MemCache{}, 6, 50)
	if err != nil || len(recs) != 2 || s.Status != "done" || s.FilesJudged != 2 {
		t.Fatalf("retry: err %v recs %d status %s", err, len(recs), s.Status)
	}
	if recs[0].Band != "severe" {
		t.Fatalf("band %s", recs[0].Band)
	}
}

type refuseJudge struct{ fakeJudge }

func (f *refuseJudge) Ask(state, qs map[string]any) (map[string]float64, error) {
	if strings.Contains(state["code"].(string), "python -c") {
		return nil, ErrRefused
	}
	return f.fakeJudge.Ask(state, qs)
}

func TestScanSkipsTextsTheJudgeRefuses(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "a.go", goFile)
	write(t, root, "b.go", strings.Replace(goFile, "padding", "python -c", 1))
	write(t, root, "c.go", goFile+"\n")
	fs := &OSFS{Root: root}
	s, _ := NewScan(fs, "cq_3", "src", "", nil, nil, false, 100, 131072)
	recs, err := s.Step(tb, fs, &refuseJudge{fakeJudge{p: 0.1}}, MemCache{}, 6, 50)
	if err != nil || s.Status != "done" || len(recs) != 2 || s.Excluded["refused_by_judge"] != 1 || len(s.Refused) != 1 || s.Refused[0] != "b.go" {
		t.Fatalf("err %v status %s recs %d excluded %v refused %v", err, s.Status, len(recs), s.Excluded, s.Refused)
	}
}

func TestSummaryAndPage(t *testing.T) {
	recs := []Record{
		{Ref: "a", Language: "go", Lines: [2]int{1, 100}, Index: 90, Band: "clean"},
		{Ref: "b", Language: "go", Lines: [2]int{1, 300}, Index: 50, Band: "severe",
			Findings: []Finding{{ID: "EH-01", Severity: "high"}, {ID: "RD-01", Severity: "low"}}},
		{Ref: "c", Language: "python", Lines: [2]int{1, 10}, Skipped: "too_small"},
	}
	s := Summary(recs, 1)
	if s["index"] != 60 || s["chunks_judged"] != 2 {
		t.Fatalf("%v", s)
	}
	page, next := Page(recs, 0, 1<<20, "medium", "")
	if len(page) != 1 || len(page[0].Findings) != 1 || next != -1 {
		t.Fatalf("%+v %d", page, next)
	}
}

func TestModelChoicesReadJevsList(t *testing.T) {
	got, err := ModelChoices([]byte(`{"models":[{"name":"jev-latest","description":"The latest"},{"name":"jev-preview"},{"name":"jev-latest"},{"name":""}]}`))
	if err != nil || len(got) != 2 || got[0]["value"] != "jev-latest" || got[1]["value"] != "jev-preview" {
		t.Fatalf("each named model once, in Jev's order: %v %v", got, err)
	}
	for _, bad := range []string{`not json`, `{"models":[]}`, `{"models":[{"name":""}]}`} {
		if _, err := ModelChoices([]byte(bad)); err == nil {
			t.Errorf("%s: refused", bad)
		}
	}
}
