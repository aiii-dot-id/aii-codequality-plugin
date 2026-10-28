package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memStore struct {
	files      map[string][]byte
	reads      int
	failWrites bool
}

func newMemStore() *memStore { return &memStore{files: map[string][]byte{}} }

func (m *memStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	m.reads++
	b, ok := m.files[p]
	if !ok {
		return nil, false, errors.New("FS_NOT_FOUND")
	}
	if off >= int64(len(b)) {
		return nil, true, nil
	}
	end := min(off+int64(n), int64(len(b)))
	return append([]byte(nil), b[off:end]...), end == int64(len(b)), nil
}

func (m *memStore) Write(p string, b []byte) error {
	if m.failWrites {
		return errors.New("FS_DENIED")
	}
	m.files[p] = append([]byte(nil), b...)
	return nil
}

func (m *memStore) Append(p string, b []byte) error {
	if m.failWrites {
		return errors.New("FS_DENIED")
	}
	m.files[p] = append(m.files[p], b...)
	return nil
}

func TestAReadLimitIsNotTheEndOfTheFile(t *testing.T) {
	s := newMemStore()
	for _, size := range []int{0, 10, IOPage, IOPage + 1, 2*IOPage + 7} {
		name := fmt.Sprint("f", size)
		s.files[name] = bytes.Repeat([]byte("x"), size)
		if b, err := ReadWhole(s, name, int64(size)); err != nil || len(b) != size {
			t.Fatalf("a file of exactly the limit (%d) is whole: %d %v", size, len(b), err)
		}
		if size > 0 {
			if _, err := ReadWhole(s, name, int64(size-1)); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("a file one byte past the limit (%d) is too large, not its prefix: %v", size, err)
			}
			b, complete, err := ReadPrefix(s, name, int64(size-1))
			if err != nil || complete || len(b) != size-1 {
				t.Fatalf("a prefix says it is one: %d %v %v", len(b), complete, err)
			}
		}
	}
	if _, err := ReadWhole(s, "absent", 10); err == nil || errors.Is(err, ErrTooLarge) {
		t.Fatalf("an absent file is the store's error: %v", err)
	}
}

func TestOSFSRefusesAFileLargerThanItsLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), bytes.Repeat([]byte("x"), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &OSFS{Root: root}
	if b, err := f.Read("a.go", 100); err != nil || len(b) != 100 {
		t.Fatalf("exactly the limit: %d %v", len(b), err)
	}
	if _, err := f.Read("a.go", 99); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("past the limit: %v", err)
	}
}

type grownFS struct{ *OSFS }

func (g grownFS) Read(file string, max int64) ([]byte, error) {
	if strings.HasSuffix(file, "grew.go") {
		return nil, ErrTooLarge
	}
	return g.OSFS.Read(file, max)
}

func TestAFileThatGrewIsNotJudgedInPart(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "grew.go", goFile)
	s, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	j := &fakeJudge{p: 0.1}
	if _, err := s.Step(tb, grownFS{&OSFS{Root: root}}, j, MemCache{}, 12, 400); err != nil {
		t.Fatal(err)
	}
	if s.Excluded["oversize"] != 1 || j.calls != 0 || s.FilesJudged != 0 {
		t.Fatalf("a grown file was judged: %+v, %d calls", s.Excluded, j.calls)
	}
}

func TestARulesFileTooLargeStopsTheStart(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", strings.Repeat("vendor/\n", (maxRulesBytes/8)+1))
	if _, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 100, 1<<20); err == nil || !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("started with half its rules: %v", err)
	}
	write(t, root, ".gitignore", "vendor/\n")
	if _, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 100, 1<<20); err != nil {
		t.Fatal(err)
	}
}

func key(shard byte, i int) string { return fmt.Sprintf("%c%063d", shard, i) }

func TestTheCacheStaysWithinItsBound(t *testing.T) {
	s := newMemStore()
	c := &ShardCache{S: s, MaxBytes: CacheShards * 64 << 10}
	answer := json.RawMessage(`{"EH-01":0.1,"EH-02":0.2,"ST-01":0.3}`)
	const n = 3000
	for i := range n {
		if err := c.Put(key('a', i), answer); err != nil {
			t.Fatal(err)
		}
		if size := int64(len(s.files["cache/a.jsonl"])); size > c.ShardBytes() {
			t.Fatalf("after %d entries the shard is %d bytes, past its %d", i+1, size, c.ShardBytes())
		}
	}
	if _, ok := c.Get(key('a', n-1)); !ok {
		t.Fatal("the newest answer is kept")
	}
	if _, ok := c.Get(key('a', 0)); ok {
		t.Fatal("the oldest answer outlived the bound")
	}
	again := &ShardCache{S: s, MaxBytes: c.MaxBytes}
	if a, ok := again.Get(key('a', n-1)); !ok || string(a) != string(answer) {
		t.Fatal("the next invocation reads back what was kept")
	}
}

func TestAnOversizeShardIsReadToItsShareAndRewritten(t *testing.T) {
	s := newMemStore()
	var b []byte
	for i := range 2000 {
		b = append(b, line(key('b', i), json.RawMessage(`{"x":0.5}`))...)
	}
	s.files["cache/b.jsonl"] = b
	c := &ShardCache{S: s, MaxBytes: CacheShards * 64 << 10}
	if _, ok := c.Get(key('b', 0)); !ok {
		t.Fatal("entries inside the share are read")
	}
	if err := c.Put(key('b', 99999), json.RawMessage(`{"x":0.9}`)); err != nil {
		t.Fatal(err)
	}
	if size := int64(len(s.files["cache/b.jsonl"])); size > c.ShardBytes() {
		t.Fatalf("the oversize shard is still %d bytes", size)
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(s.files["cache/b.jsonl"])), "\n") {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("the rewrite left a cut line: %q", ln)
		}
	}
	if _, ok := (&ShardCache{S: s, MaxBytes: c.MaxBytes}).Get(key('b', 99999)); !ok {
		t.Fatal("the new entry survives the rewrite")
	}
}

type failCache struct{ MemCache }

func (failCache) Put(string, json.RawMessage) error { return errors.New("FS_DENIED") }

func TestAnAnswerTheCacheCannotKeepIsStillUsed(t *testing.T) {
	tb := table(t)
	recs, err := JudgeText(tb, &fakeJudge{p: 0.9, where: "func Add", conf: 0.9}, failCache{MemCache{}}, "x.go", "go",
		"package x\n\n// Adds.\nfunc Add(a, b int) int {\n\treturn a + b + 1000000 + len(\"some text that makes it long enough\")\n}\n\nfunc Other() int {\n\treturn 2\n}\n")
	if err != nil || len(recs) != 2 || len(recs[0].Findings) == 0 || recs[0].Uncached != 2 || recs[1].Uncached != 1 {
		t.Fatalf("%v %+v", err, recs)
	}
	s := newMemStore()
	s.failWrites = true
	c := &ShardCache{S: s, MaxBytes: 1 << 20}
	if recs, err := JudgeText(tb, &fakeJudge{p: 0.1}, c, "x.go", "go", goFile); err != nil || recs[0].Uncached == 0 {
		t.Fatalf("a denied cache write stopped the judgment: %v %+v", err, recs)
	}
	if err := (&ShardCache{S: newMemStore(), MaxBytes: CacheShards * 64 << 10}).Put(key('c', 1), json.RawMessage(`"`+strings.Repeat("x", 40<<10)+`"`)); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("an answer larger than half a shard: %v", err)
	}
}

func TestAnUnreadableEntryIsAMiss(t *testing.T) {
	tb := table(t)
	j := &fakeJudge{p: 0.1}
	c := MemCache{}
	if _, err := JudgeText(tb, j, c, "x.go", "go", goFile); err != nil {
		t.Fatal(err)
	}
	for k := range c {
		c[k] = json.RawMessage(`{not json`)
	}
	if _, err := JudgeText(tb, j, c, "x.go", "go", goFile); err != nil || j.calls != 4 {
		t.Fatalf("%v, %d calls", err, j.calls)
	}
}

func TestReplyModel(t *testing.T) {
	for body, want := range map[string]string{
		`{"model":"jev-1.13.0","answers":{}}`: "jev-1.13.0",
		`{"answers":{}}`:                      "",
		`{"model":" spaced ","answers":{}}`:   "",
		`not json`:                            "",
	} {
		if got := ReplyModel([]byte(body)); got != want {
			t.Errorf("%s: %q", body, got)
		}
	}
	if KeyModel("jev", "m") != "m" || KeyModel("", "m") != "m" || KeyModel("local judge.lan:8080", "m") == "m" {
		t.Fatal("hosted keys are the model alone; a local judge's carry its address")
	}
}

func TestTheChunkSizeIsASettingAScanKeeps(t *testing.T) {
	var b strings.Builder
	b.WriteString("package x\n\n")
	for i := 0; b.Len() < 20000; i++ {
		fmt.Fprintf(&b, "func F%d() int {\n\treturn %d + 1234567890\n}\n\n", i, i)
	}
	src := b.String()
	if n := len(ChunksOf(src, 4096)); n < 5 {
		t.Fatalf("a 20 KB text in 4 KB chunks is %d chunks", n)
	}
	if len(ChunksOf(src, 0)) != len(Chunks(src)) {
		t.Fatal("no size is the default size")
	}
	if EstimatedCallsAt(20000, 4096) <= EstimatedCalls(20000) {
		t.Fatal("smaller chunks need more calls")
	}
	tb := table(t)
	root := t.TempDir()
	write(t, root, "big.go", src)
	s, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s.ChunkBytes = 4096
	raw, _ := json.Marshal(s)
	var back Scan
	if json.Unmarshal(raw, &back) != nil || back.ChunkBytes != 4096 {
		t.Fatal("the chunk size is kept with the scan")
	}
	recs, err := back.Step(tb, &OSFS{Root: root}, &fakeJudge{p: 0.1}, MemCache{}, 64, 400)
	if err != nil || len(recs) < 5 {
		t.Fatalf("%v: %d records", err, len(recs))
	}
}

func TestStepsLeftFollowTheWalk(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	for d := range 3 {
		for f := range 10 {
			write(t, root, fmt.Sprintf("d%d/f%d.go", d, f), strings.Replace(goFile, "Add", fmt.Sprintf("Add%d%d", d, f), 1))
		}
	}
	s, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 1000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first := s.StepsLeft(4, 400)
	if first < 1 {
		t.Fatalf("a new scan needs at least a step: %d", first)
	}
	j := &fakeJudge{p: 0.1}
	if _, err := s.Step(tb, &OSFS{Root: root}, j, MemCache{}, 4, 400); err != nil {
		t.Fatal(err)
	}
	mid := s.StepsLeft(4, 400)
	if mid < 1 || s.DirsListed == 0 || s.FilesListed == 0 {
		t.Fatalf("mid-walk: %d steps, %d dirs, %d files", mid, s.DirsListed, s.FilesListed)
	}
	for i := 0; i < 100 && s.Status == "running"; i++ {
		if _, err := s.Step(tb, &OSFS{Root: root}, j, MemCache{}, 4, 400); err != nil {
			t.Fatal(err)
		}
	}
	if s.Status != "done" || s.StepsLeft(4, 400) != 0 {
		t.Fatalf("ended %s with %d steps left", s.Status, s.StepsLeft(4, 400))
	}
	legacy := &Scan{Status: "running", Stack: []string{"x"}, MaxFiles: 10}
	if legacy.StepsLeft(12, 400) < 1 {
		t.Fatal("a scan saved before the counts still estimates")
	}
}
