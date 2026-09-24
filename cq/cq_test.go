// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

//go:build !wasm_unknown

package cq

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	b, c, lost, _ := BlankComments(src, tb.Langs["go"])
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
	b, c, _, _ = BlankComments(py, tb.Langs["python"])
	if strings.Contains(b, "Docstring") || strings.Contains(b, "# yes") || !strings.Contains(b, "'# no'") {
		t.Fatalf("python blanking:\n%s", b)
	}
	if !strings.Contains(c, "Docstring") || !strings.Contains(c, "# yes") {
		t.Fatalf("python comments %q", c)
	}
	rs := "fn f<'a>(c: char) -> bool { c == '\"' } // q\n"
	b, _, lost, _ = BlankComments(rs, tb.Langs["rust"])
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

// fakeJudge answers every Noul with p, or fails; it answers every Choice with the first option
// holding where (or the first option) at confidence conf, counting those calls in chosen.
type fakeJudge struct {
	p      float64
	fail   bool
	calls  int
	seen   []map[string]any
	where  string
	conf   float64
	chosen int
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

func (f *fakeJudge) Choose(state, qs map[string]any) (map[string]Choice, error) {
	f.chosen++
	out := map[string]Choice{}
	for id, q := range qs {
		var keys []string
		for k := range q.(map[string]any)["criteria"].(map[string]any) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pick := keys[0]
		for _, k := range keys {
			if strings.Contains(k, f.where) {
				pick = k
				break
			}
		}
		out[id] = Choice{Choice: pick, Confidence: f.conf}
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
		recs, err := s.Step(tb, fs, j, MemCache{}, 3, 50) // three calls (code, location, comments): one file per step
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
	if err != nil || len(recs) != 0 || s.Status != "judge_unavailable" || len(s.Pending) != 2 || s.FilesSeen != 0 || !strings.HasSuffix(s.LastError, "the next step retries this file") {
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

// unansweredLocate answers every battery and never a location, as Jev did through the host for
// the location calls of a repository's two largest files (2026-09-23).
type unansweredLocate struct{ fakeJudge }

func (f *unansweredLocate) Choose(state, qs map[string]any) (map[string]Choice, error) {
	return nil, fmt.Errorf("%w: no answer within the 2000 ms this call allowed", ErrJudge)
}

// A file whose location call goes unanswered stays pending with its batteries kept, and the retry
// step, planning only three calls, finishes it from the cache with the one call left to make; the
// failure it recovered from is no longer reported (a resident stopped a scan that had recovered,
// reading the old last_error, 2026-09-23).
func TestAnUnansweredLocationIsRetriedFromTheCache(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "a.go", "package x\n\nfunc Good(a, b int) int {\n\treturn a + b\n}\n\nfunc Bad(path string) string {\n\tb, _ := os.ReadFile(path)\n\treturn string(b) + \"padding padding padding\"\n}\n")
	fs := &OSFS{Root: root}
	c := MemCache{}
	s, _ := NewScan(fs, "cq_3", "src", "", nil, nil, false, 100, 131072)
	slow := &unansweredLocate{fakeJudge{p: 0.9}}
	if recs, err := s.Step(tb, fs, slow, c, 12, 50); err != nil || len(recs) != 0 || s.Status != "judge_unavailable" || len(s.Pending) != 1 {
		t.Fatalf("err %v recs %d status %s pending %d", err, len(recs), s.Status, len(s.Pending))
	}
	retry := &fakeJudge{p: 0.9, where: "func Bad", conf: 0.9}
	recs, err := s.Step(tb, fs, retry, c, 3, 50)
	if err != nil || len(recs) != 1 || s.Status != "done" || s.LastError != "" || retry.calls != 0 || retry.chosen != 1 {
		t.Fatalf("err %v recs %d status %s last_error %q: batteries asked again %d, locations %d", err, len(recs), s.Status, s.LastError, retry.calls, retry.chosen)
	}
	if !strings.Contains(recs[0].Findings[0].Where, "func Bad") {
		t.Fatalf("located %+v", recs[0].Findings[0])
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
	page, next := Page(Select(recs, ".", "medium", ""), 0, 1<<20)
	if len(page) != 1 || len(page[0].Findings) != 1 || next != -1 {
		t.Fatalf("%+v %d", page, next)
	}
}

// The globs a resident reached for on a real scan (2026-09-23): "**" and "internal/**" came back
// with the top level only and with nothing, and a report counted a file it did not return.
func TestSelectReachesNestedFilesAndCountsWhatItReturns(t *testing.T) {
	refs := []string{"main.go", "internal/plan/plan.go", "internal/plan/plan_test.go", "internal/transcript/round_test.go"}
	for glob, want := range map[string]string{
		"":                   "1111",
		".":                  "1111",
		"**":                 "1111",
		"*":                  "1111",
		"*.go":               "1000",
		"**/*.go":            "1111",
		"**/*_test.go":       "0011",
		"internal/**":        "0111",
		"internal":           "0111",
		"./internal/plan/":   "0110",
		"internal/*/plan.go": "0100",
		"internal/**/*.go":   "0111",
		"plan":               "0000",
		"internal/plan.go":   "0000",
		"[":                  "0000",
	} {
		got := ""
		for _, r := range refs {
			got += map[bool]string{true: "1", false: "0"}[Match(glob, r)]
		}
		if got != want {
			t.Errorf("Match(%q) = %s, want %s", glob, got, want)
		}
	}
	recs := []Record{
		{Ref: "internal/plan/plan.go", Language: "go", Findings: []Finding{{ID: "EH-01", Severity: "high"}}},
		{Ref: "internal/transcript/round_test.go", Language: "go", Findings: []Finding{{ID: "RD-03", Severity: "low"}}},
		{Ref: "main.go", Language: "go", Findings: []Finding{{ID: "EH-01", Severity: "high"}}},
	}
	if got := Select(recs, "internal/**", "medium", "go"); len(got) != 1 || got[0].Ref != "internal/plan/plan.go" {
		t.Fatalf("a record with no finding at the severity asked for is not selected, and so not counted: %+v", got)
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

// Jev's status decides what a failed call means: unavailable is retried, a text Jev declines is
// recorded and passed, a refused key stops the scan with what to do — and the status is kept in
// the error, so a stopped scan says what Jev answered.
func TestJudgeReplyReadsTheStatus(t *testing.T) {
	answers := []byte(`{"answers":{"EH-01":{"type":"noul","noul":0.83}}}`)
	if _, err := JudgeReply(200, answers); err != nil {
		t.Fatalf("200 carries answers: %v", err)
	}
	for _, c := range []struct {
		status int
		body   string
		want   error
	}{
		{0, "", ErrJudge}, {429, "{}", ErrJudge}, {500, "", ErrJudge}, {503, "<html>", ErrJudge},
		{400, `{"error":"state too large"}`, ErrRefused}, {413, "", ErrRefused}, {422, "{}", ErrRefused},
		{403, "<html>Attention Required</html>", ErrRefused},
		{401, `{"error":"bad key"}`, ErrKey}, {403, `{"error":"forbidden"}`, ErrKey},
	} {
		_, err := JudgeReply(c.status, []byte(c.body))
		switch {
		case err == nil:
			t.Fatalf("%d must fail", c.status)
		case c.want != nil && !errors.Is(err, c.want):
			t.Fatalf("%d %q: got %v, want %v", c.status, c.body, err, c.want)
		case c.want == ErrKey && (errors.Is(err, ErrJudge) || errors.Is(err, ErrRefused)):
			t.Fatalf("%d %q is the key being refused, neither retried nor passed over: %v", c.status, c.body, err)
		case !strings.Contains(err.Error(), strconv.Itoa(c.status)):
			t.Fatalf("the status is kept in the error: %v", err)
		}
	}
}

// A language is named in any case and answered in the table's spelling; a name the table does
// not know is refused with the names it does, never a filter that silently matches nothing.
func TestLanguagesAreMatchedInAnyCaseAndUnknownOnesRefused(t *testing.T) {
	tb := table(t)
	got, err := tb.Languages([]string{"Go", " MARKDOWN ", "go"})
	if err != nil || strings.Join(got, ",") != "go,markdown,go" {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := tb.Languages([]string{"golang"}); err == nil || !strings.Contains(err.Error(), `"golang"`) || !strings.Contains(err.Error(), "go, ") {
		t.Fatalf("an unknown name is refused, listing the known: %v", err)
	}
	if got, err := tb.Languages(nil); err != nil || got != nil {
		t.Fatal("no names is no filter")
	}
}

// A report says what a finding means: every item the catalogue asks, in any language, is named
// with its statement; an id the catalogue does not hold is left out; the summary carries the
// statements of the findings it counts.
func TestFaultsNameWhatAFindingMeans(t *testing.T) {
	all := append(append([]Item(nil), general...), docs...)
	for _, items := range addenda {
		all = append(all, items...)
	}
	for _, it := range all {
		if got := Faults([]string{it.ID})[it.ID]; got != it.Fault || got == "" {
			t.Fatalf("%s is named %q, want %q", it.ID, got, it.Fault)
		}
	}
	if f := Faults([]string{"NO-SUCH"}); len(f) != 0 {
		t.Fatalf("an unknown id is left out: %v", f)
	}
	recs := []Record{{Ref: "a.go", Language: "go", Lines: [2]int{1, 10}, Band: "minor", Index: 80,
		Findings: []Finding{{ID: "EH-01"}, {ID: "ID-GO-01"}}}}
	sum := Summary(recs, 5)
	faults, _ := sum["faults"].(map[string]string)
	if len(faults) != 2 || faults["ID-GO-01"] != "an error is returned without context" || faults["EH-01"] == "" {
		t.Fatalf("the summary names the faults it counts: %v", sum["faults"])
	}
	if f := FaultsOf(recs); len(f) != 2 {
		t.Fatalf("FaultsOf names the records' findings: %v", f)
	}
}

// A chunk splits into its top-level units, each named by its lines and first line: Go functions
// and types, a Java class split at its methods, Python definitions; lines count from the chunk's
// first line; short declarations merge; the count never passes MaxUnits.
// A multi-line raw string is text, not code: its lines at column 0 do not start units, so a
// command whose usage text is a raw string stays one unit (three of aii-plugin-sdk's commands were
// cut into pieces headed by the string's closing line, 2026-09-23). The lexer marks exactly the
// lines that begin inside such a string, and not the line after an unterminated one-line string.
func TestARawStringDoesNotSplitAUnit(t *testing.T) {
	tb := table(t)
	goSrc := "package p\n\nfunc cmdSign(args []string) int {\n\tusage := `Usage: sign\n\nSigns the staged tree.\n  1. reads it\n`\n\treturn len(usage)\n}\n\nfunc other() int {\n\treturn 2\n}\n"
	b, _, _, in := BlankComments(goSrc, tb.Langs["go"])
	want := []bool{false, false, false, false, true, true, true, true, false, false, false, false, false, false, false}
	if fmt.Sprint(in) != fmt.Sprint(want) {
		t.Fatalf("lines inside the string:\n got %v\nwant %v", in, want)
	}
	var heads []string
	for _, u := range Units(b, 1, in) {
		heads = append(heads, u.Key)
	}
	if len(heads) != 3 || !strings.Contains(heads[1], "lines 3-10: func cmdSign") || !strings.Contains(heads[2], "func other") {
		t.Fatalf("the command is one unit: %q", heads)
	}
	js := "function a() {\n  const t = `\nline one\nline two\n`;\n  return t;\n}\n\nfunction b() {\n  return 1;\n}\n"
	b, _, _, in = BlankComments(js, tb.Langs["javascript"])
	if u := Units(b, 1, in); len(u) != 2 || !strings.HasPrefix(u[0].Key, "lines 1-7: function a()") {
		t.Fatalf("a template literal is text: %+v", u)
	}
	_, _, lost, in := BlankComments("x := \"open\ny := 1\n", tb.Langs["go"])
	if !lost || in[1] {
		t.Fatalf("an unterminated one-line string ends at its line: lost %v, marks %v", lost, in)
	}
}

// A location answer is kept for the units it was asked about: split differently, the same text is
// asked again rather than answered with a choice among units no longer offered.
func TestALocationIsAskedAgainWhenTheUnitsChange(t *testing.T) {
	src := "package x\n\nfunc Good(a, b int) int {\n\treturn a + b\n}\n\nfunc Bad(path string) string {\n\tb, _ := os.ReadFile(path)\n\treturn string(b) + \"padding padding padding\"\n}\n"
	j := &fakeJudge{p: 0.9, where: "func Bad", conf: 0.9}
	c := MemCache{}
	ch := Chunks(src)[0]
	fs := []Finding{{ID: "EH-01"}}
	if calls, _, err := locate(j, c, "go", "fake", ch, nil, fs); err != nil || calls != 1 {
		t.Fatalf("first ask: %d %v", calls, err)
	}
	if calls, hits, _ := locate(j, c, "go", "fake", ch, nil, fs); calls != 0 || hits != 1 {
		t.Fatalf("the same units are answered from the cache: calls %d hits %d", calls, hits)
	}
	in := make([]bool, 11)
	in[6] = true // line 7, "func Bad", now read as the inside of a string
	if calls, _, _ := locate(j, c, "go", "fake", ch, in, fs); calls != 1 || j.chosen != 2 {
		t.Fatalf("other units are asked again: calls %d, chosen %d", calls, j.chosen)
	}
}

func TestUnitsSplitAChunkAtItsTopLevel(t *testing.T) {
	goSrc := "package p\n\nimport \"fmt\"\n\ntype T struct {\n\tn int\n}\n\nfunc (t T) A() int {\n\treturn t.n\n}\n\nfunc B() {\n\tfmt.Println(1)\n}\n"
	u := Units(goSrc, 1, nil)
	var keys []string
	for _, x := range u {
		keys = append(keys, x.Key)
	}
	want := []string{"lines 1-3: package p", "lines 5-7: type T struct {", "lines 9-11: func (t T) A() int {", "lines 13-15: func B() {"}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("go units %q", keys)
	}
	if !strings.Contains(u[2].Text, "return t.n") || u[2].First != 9 || u[2].Last != 11 {
		t.Fatalf("a unit carries its text and lines: %+v", u[2])
	}
	java := "public class C {\n    private int n;\n\n    int a() {\n        return n;\n    }\n\n    void b() {\n        n++;\n    }\n}\n"
	keys = nil
	for _, x := range Units(java, 100, nil) {
		keys = append(keys, x.Key)
	}
	if len(keys) != 3 || !strings.HasPrefix(keys[1], "lines 103-105: int a()") || !strings.HasPrefix(keys[2], "lines 107-110: void b()") {
		t.Fatalf("a class splits at its methods, lines from the chunk's first: %q", keys)
	}
	py := "import os\nimport sys\n\ndef f(x):\n    return x\n\nclass K:\n    def g(self):\n        pass\n"
	keys = nil
	for _, x := range Units(py, 1, nil) {
		keys = append(keys, x.Key)
	}
	if len(keys) != 3 || !strings.HasPrefix(keys[0], "lines 1-2: import os") || !strings.HasPrefix(keys[1], "lines 4-5: def f(x):") {
		t.Fatalf("python units %q", keys)
	}
	var many strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&many, "func f%d() {\n\tx := %d\n\t_ = x\n}\n", i, i)
	}
	if n := len(Units(many.String(), 1, nil)); n > MaxUnits || n < MaxUnits/2 {
		t.Fatalf("%d units for 200 functions", n)
	}
	if u := Units("x = 1", 7, nil); len(u) != 1 || u[0].Key != "lines 7-7: x = 1" {
		t.Fatalf("one line is one unit: %+v", u)
	}
}

// A finding is located to the unit Jev names when Jev is sure (WhereMin), in the same call for
// every code finding of the chunk, from the cache on an unchanged text; unsure, it is left
// unlocated; a chunk of one unit is not asked. Leads are marked by today's measurement, and the
// summary counts findings and leads apart.
func TestAFindingIsLocatedWhenJevIsSure(t *testing.T) {
	tb := table(t)
	src := `package x

func Good(a, b int) int {
	return a + b
}

func Bad(path string) string {
	b, _ := os.ReadFile(path)
	return string(b) + "padding padding padding"
}
`
	j := &fakeJudge{p: 0.9, where: "func Bad", conf: 0.9}
	c := MemCache{}
	recs, err := JudgeText(tb, j, c, "x.go", "go", src)
	if err != nil || len(recs) != 1 {
		t.Fatalf("%v %+v", err, recs)
	}
	r := recs[0]
	if j.chosen != 1 || r.Calls != 2 {
		t.Fatalf("one location call for the chunk: chosen %d, calls %d", j.chosen, r.Calls)
	}
	for _, f := range r.Findings {
		if !strings.Contains(f.Where, "func Bad") || f.WhereConfidence != 0.9 {
			t.Fatalf("finding %s located at %q (%v)", f.ID, f.Where, f.WhereConfidence)
		}
	}
	again, _ := JudgeText(tb, j, c, "x.go", "go", src)
	if j.chosen != 1 || again[0].Calls != 0 || again[0].Cached != 2 || again[0].Findings[0].Where == "" {
		t.Fatalf("an unchanged text is located from the cache: chosen %d, %+v", j.chosen, again[0])
	}
	unsure := &fakeJudge{p: 0.9, where: "func Bad", conf: 0.5}
	recs, _ = JudgeText(tb, unsure, MemCache{}, "x.go", "go", src)
	for _, f := range recs[0].Findings {
		if f.Where != "" {
			t.Fatalf("below WhereMin nothing is located: %+v", f)
		}
	}
	one := &fakeJudge{p: 0.9, conf: 0.9}
	JudgeText(tb, one, MemCache{}, "y.go", "go", "func Only(a, b int) int {\n\treturn a + b + len(\"text long enough to be judged at all\")\n}\n")
	if one.chosen != 0 {
		t.Fatal("a chunk of one unit is not asked where")
	}
	MarkLeads(recs)
	lead := map[string]bool{}
	for _, f := range recs[0].Findings {
		lead[f.ID] = f.Lead
	}
	if lead["EH-01"] || !lead["DF-01"] || !lead["ST-03"] {
		t.Fatalf("EH-01 is reported as a finding, DF-01 and unmeasured ST-03 as leads: %v", lead)
	}
	for id, p := range precision {
		if p[1] <= 0 || p[0] < 0 || p[0] > p[1] || Lead(id) != (100*p[0] < ReportMin*p[1]) {
			t.Fatalf("%s: %d of %d confirmed, lead %v", id, p[0], p[1], Lead(id))
		}
	}
	if !Lead("EH-02") || Lead("ST-01") {
		t.Fatal("at 25 of 45 EH-02 is a lead, at 15 of 20 ST-01 a finding (the 60% line, 122 files)")
	}
	sum := Summary(recs, 5)
	fc, lc := sum["finding_counts"].(map[string]int), sum["lead_counts"].(map[string]int)
	if fc["EH-01"] != 1 || fc["DF-01"] != 0 || lc["DF-01"] != 1 {
		t.Fatalf("the summary counts findings and leads apart: %v %v", fc, lc)
	}
}

// A Choice answer is read as its chosen option and confidence, by the same status rules as a
// Noul battery; an answer without both is a judge failure, never a location.
func TestChoiceReplyReadsTheChoiceAndItsConfidence(t *testing.T) {
	body := []byte(`{"model":"jev-1.13.0","answers":{"EH-01":{"type":"choice","choice":"lines 9-11: func Bad() {","confidence":0.87,"probabilities":{"lines 9-11: func Bad() {":0.9,"lines 1-3: package x":0.1}}}}`)
	got, err := ChoiceReply(200, body)
	if err != nil || got["EH-01"].Choice != "lines 9-11: func Bad() {" || got["EH-01"].Confidence != 0.87 {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := ChoiceReply(200, []byte(`{"answers":{"EH-01":{"type":"choice","choice":"x"}}}`)); !errors.Is(err, ErrJudge) {
		t.Fatalf("an answer without confidence is a judge failure: %v", err)
	}
	if _, err := ChoiceReply(503, nil); !errors.Is(err, ErrJudge) {
		t.Fatalf("503 is Jev unavailable: %v", err)
	}
	if _, err := ChoiceReply(401, []byte(`{"error":"key"}`)); !errors.Is(err, ErrKey) {
		t.Fatalf("401 is the key refused: %v", err)
	}
}
