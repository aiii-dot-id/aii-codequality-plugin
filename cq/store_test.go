package cq

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type memStore struct {
	files       map[string][]byte
	reads       int
	failWrites  bool
	maxRead     int
	maxWrite    int
	failPublish bool
	writesTo    map[string]int
	failRead    map[string]int64
	stall       map[string]int64
	failList    error
	cutList     bool
	omit        map[string]bool
}

func newMemStore() *memStore { return &memStore{files: map[string][]byte{}} }

func (m *memStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	m.reads++
	m.maxRead = max(m.maxRead, n)
	if at, fails := m.failRead[p]; fails && off >= at {
		return nil, false, errors.New("FS_IO")
	}
	if at, stalls := m.stall[p]; stalls && off >= at {
		return nil, false, nil
	}
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
	m.wrote(p, b)
	m.files[p] = append([]byte(nil), b...)
	return nil
}

func (m *memStore) Append(p string, b []byte) error {
	if m.failWrites {
		return errors.New("FS_DENIED")
	}
	m.wrote(p, b)
	m.files[p] = append(m.files[p], b...)
	return nil
}

func (m *memStore) wrote(p string, b []byte) {
	m.maxWrite = max(m.maxWrite, len(b))
	if m.writesTo == nil {
		m.writesTo = map[string]int{}
	}
	m.writesTo[p]++
}

func (m *memStore) List(dir string) ([]Entry, bool, error) {
	if m.failList != nil {
		return nil, false, m.failList
	}
	var out []Entry
	for name, b := range m.files {
		if path.Dir(name) == dir && !m.omit[name] {
			out = append(out, Entry{Name: path.Base(name), Size: int64(len(b))})
		}
	}
	if len(out) == 0 && !m.cutList {
		return nil, false, fmt.Errorf("FS_NOT_FOUND: %w", ErrNotFound)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, m.cutList, nil
}

func (m *memStore) PublishStaged(p, staged, sum string) error {
	b, ok := m.files[staged]
	if !ok || m.failPublish {
		return errors.New("FS_PUBLISH_FAILED")
	}
	if got := sha256.Sum256(b); hex.EncodeToString(got[:]) != sum {
		return errors.New("FS_DIGEST_MISMATCH")
	}
	m.files[p] = b
	delete(m.files, staged)
	return nil
}

func (m *memStore) Delete(p string) error { delete(m.files, p); return nil }

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
	s, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := &fakeJudge{p: 0.1}
	if _, err := s.Step(tb, grownFS{&OSFS{Root: root}}, j, MemCache{}, 12, 400); err != nil {
		t.Fatal(err)
	}
	if s.Status != "done" || s.FilesJudged != 1 || len(s.Excluded) != 0 || s.File != nil {
		t.Fatalf("a grown file: status %s, judged %d, cursor %+v, excluded %+v", s.Status, s.FilesJudged, s.File, s.Excluded)
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
		if size := int64(len(s.files["cache/a0.jsonl"])); size > c.ShardBytes() {
			t.Fatalf("after %d entries the shard is %d bytes, past its %d", i+1, size, c.ShardBytes())
		}
	}
	if _, ok, err := c.Get(key('a', n-1)); err != nil || !ok {
		t.Fatal("the newest answer is kept")
	}
	if _, ok, err := c.Get(key('a', 0)); err != nil || ok {
		t.Fatal("the oldest answer outlived the bound")
	}
	again := &ShardCache{S: s, MaxBytes: c.MaxBytes}
	if a, ok, err := again.Get(key('a', n-1)); err != nil || !ok || string(a) != string(answer) {
		t.Fatal("the next invocation reads back what was kept")
	}
}

func TestAnOversizeShardIsReadToItsShareAndRewritten(t *testing.T) {
	s := newMemStore()
	var b []byte
	for i := range 2000 {
		b = append(b, line(key('b', i), json.RawMessage(`{"x":0.5}`))...)
	}
	s.files["cache/b0.jsonl"] = b
	c := &ShardCache{S: s, MaxBytes: CacheShards * 64 << 10}
	if _, ok, err := c.Get(key('b', 0)); err != nil || !ok {
		t.Fatal("entries inside the share are read")
	}
	if err := c.Put(key('b', 99999), json.RawMessage(`{"x":0.9}`)); err != nil {
		t.Fatal(err)
	}
	if size := int64(len(s.files["cache/b0.jsonl"])); size > c.ShardBytes() {
		t.Fatalf("the oversize shard is still %d bytes", size)
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(s.files["cache/b0.jsonl"])), "\n") {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("the rewrite left a cut line: %q", ln)
		}
	}
	if _, ok, err := (&ShardCache{S: s, MaxBytes: c.MaxBytes}).Get(key('b', 99999)); err != nil || !ok {
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

func TestTheFileCapCountsJudgedFiles(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	for f := range 20 {
		write(t, root, fmt.Sprintf("a%02d_test.go", f), goFile)
	}
	for f := range 10 {
		write(t, root, fmt.Sprintf("b%02d.go", f), strings.Replace(goFile, "Add", fmt.Sprintf("Add%d", f), 1))
	}
	s, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 5, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	j := &fakeJudge{p: 0.1}
	for i := 0; i < 100 && s.Status == "running"; i++ {
		if _, err := s.Step(tb, &OSFS{Root: root}, j, MemCache{}, 100, 400); err != nil {
			t.Fatal(err)
		}
	}
	if s.Status != StatusFileCap || s.FilesJudged != 5 || s.FilesSeen != 25 || s.Excluded["test"] != 20 {
		t.Fatalf("status %s, judged %d, seen %d, excluded %v", s.Status, s.FilesJudged, s.FilesSeen, s.Excluded)
	}
}

func TestTheExaminedBoundEndsAWalkOfSkippedFiles(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	for f := range 2*ExaminedPerFile + 5 {
		write(t, root, fmt.Sprintf("a%03d_test.go", f), goFile)
	}
	s, err := NewScan(&OSFS{Root: root}, "s", ".", "", nil, nil, false, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100 && s.Status == "running"; i++ {
		if _, err := s.Step(tb, &OSFS{Root: root}, &fakeJudge{p: 0.1}, MemCache{}, 100, 400); err != nil {
			t.Fatal(err)
		}
	}
	if s.Status != StatusExaminedCap || s.FilesSeen != s.MaxExamined() || s.FilesJudged != 0 {
		t.Fatalf("status %s, seen %d of %d, judged %d", s.Status, s.FilesSeen, s.MaxExamined(), s.FilesJudged)
	}
}

func TestTheOldCapWordReadsAsTheNewOne(t *testing.T) {
	for old, want := range map[string]string{"budget_exhausted": StatusFileCap, "running": "running", "done": "done", StatusFileCap: StatusFileCap} {
		if got := CurrentStatus(old); got != want {
			t.Errorf("%s: %s, want %s", old, got, want)
		}
	}
}

func TestALookupStreamsItsShardAndTheNewestLineWins(t *testing.T) {
	s := newMemStore()
	c := &ShardCache{S: s, MaxBytes: 4 * CacheShards * 64 << 10}
	k := key('e', 7)
	for i := range 900 {
		if err := c.Put(key('e', 1000+i), json.RawMessage(`{"x":0.5}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Put(k, json.RawMessage(`{"x":0.1}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(k, json.RawMessage(`{"x":0.2}`)); err != nil {
		t.Fatal(err)
	}
	s.files["cache/e0.jsonl"] = append(s.files["cache/e0.jsonl"], []byte(`{"a":{"x":0.9},"k":"`+k)...)
	s.maxRead = 0
	got, ok, err := (&ShardCache{S: s, MaxBytes: c.MaxBytes}).Get(k)
	if err != nil || !ok || string(got) != `{"x":0.2}` {
		t.Fatalf("the newest whole line wins: %s %v", got, ok)
	}
	if s.maxRead > cachePage {
		t.Fatalf("a lookup read %d bytes at once, over its %d-byte page", s.maxRead, cachePage)
	}
	if _, ok, err := (&ShardCache{S: s, MaxBytes: c.MaxBytes}).Get(key('f', 7)); err != nil || ok {
		t.Fatal("another shard's key is a miss")
	}
}

func TestCompactionIsStagedInHostSizedWritesAndPublished(t *testing.T) {
	s := newMemStore()
	c := &ShardCache{S: s, MaxBytes: 16 * CacheShards * 64 << 10}
	answer := json.RawMessage(`"` + strings.Repeat("a", 2000) + `"`)
	for i := range 700 {
		if err := c.Put(key('d', i), answer); err != nil {
			t.Fatal(err)
		}
	}
	if s.maxWrite > 256<<10 {
		t.Fatalf("a write carried %d bytes, over the host's 256 KiB", s.maxWrite)
	}
	if s.writesTo["cache/d0.jsonl.next"] == 0 {
		t.Fatal("the rewrite went through a staged file")
	}
	if _, staged := s.files["cache/d0.jsonl.next"]; staged {
		t.Fatal("the staged file replaced the shard")
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(s.files["cache/d0.jsonl"])), "\n") {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("a rewritten shard holds only whole lines: %.60q", ln)
		}
	}
	if _, ok, err := c.Get(key('d', 699)); err != nil || !ok {
		t.Fatal("the newest answer is kept")
	}
	before := append([]byte(nil), s.files["cache/d0.jsonl"]...)
	s.failPublish = true
	c2 := &ShardCache{S: s, MaxBytes: c.MaxBytes}
	for i := 700; i < 1400; i++ {
		if c2.Put(key('d', i), answer) != nil {
			break
		}
	}
	if got := s.files["cache/d0.jsonl"]; !bytes.HasPrefix(got, before[:len(before)/2]) {
		t.Fatal("a failed replacement must leave the old shard as it was")
	}
}

func earlierFile(digit byte, n int) []byte {
	var b []byte
	for i := range n {
		b = append(b, line(earlierKey(digit, i), json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)))...)
	}
	return b
}

func earlierKey(digit byte, i int) string { return fmt.Sprintf("%c%x%062d", digit, i%16, i) }

type noListStore struct{ m *memStore }

func (s noListStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	return s.m.Read(p, off, n)
}
func (s noListStore) Write(p string, b []byte) error  { return s.m.Write(p, b) }
func (s noListStore) Append(p string, b []byte) error { return s.m.Append(p, b) }

func TestAnAnswer0115KeptIsReadWhereItIsAndItsFileNeverChanges(t *testing.T) {
	s := newMemStore()
	old := earlierFile('a', 3000)
	s.files["cache/a.jsonl"] = append([]byte(nil), old...)
	c := &ShardCache{S: s, MaxBytes: 16 << 20}
	for _, i := range []int{0, 1, 1499, 2999} {
		if a, ok, err := c.Get(earlierKey('a', i)); err != nil || !ok || string(a) != fmt.Sprintf(`{"n":%d}`, i) {
			t.Fatalf("answer %d, kept by 0.1.15, was not found: %s %v", i, a, ok)
		}
	}
	if _, ok, err := c.Get(earlierKey('a', 999999)); err != nil || ok {
		t.Fatal("a key in neither layout is a miss")
	}
	if _, ok, err := c.Get(earlierKey('b', 0)); err != nil || ok {
		t.Fatal("a key whose earlier file is not there is a miss")
	}
	if len(s.writesTo) != 0 {
		t.Fatalf("a lookup wrote: %v", s.writesTo)
	}
	if s.maxRead > cachePage {
		t.Fatalf("a lookup read %d bytes at once, over its %d-byte page", s.maxRead, cachePage)
	}
	fresh := earlierKey('a', 777777)
	if err := c.Put(fresh, json.RawMessage(`{"n":-1}`)); err != nil {
		t.Fatal(err)
	}
	if a, ok, err := (&ShardCache{S: s, MaxBytes: c.MaxBytes}).Get(fresh); err != nil || !ok || string(a) != `{"n":-1}` {
		t.Fatalf("an answer asked since: %s %v", a, ok)
	}
	if len(s.writesTo) != 1 || s.writesTo["cache/a1.jsonl"] != 1 {
		t.Fatalf("the new answer's one write goes to its own shard: %v", s.writesTo)
	}
	if !bytes.Equal(s.files["cache/a.jsonl"], old) {
		t.Fatal("0.1.15's file was changed")
	}
}

func TestTheNewestAnswerWinsAndAnEarlierHitCostsTheWalkAMissDoes(t *testing.T) {
	s := newMemStore()
	k := earlierKey('c', 1000000)
	file := append(line(k, json.RawMessage(`{"n":"first"}`)), earlierFile('c', 3000)...)
	s.files["cache/c.jsonl"] = append(file, line(k, json.RawMessage(`{"n":"last"}`))...)
	reads := func(key, want string) int {
		t.Helper()
		s.reads = 0
		a, ok, err := (&ShardCache{S: s, MaxBytes: 16 << 20}).Get(key)
		if err != nil || ok != (want != "") || string(a) != want {
			t.Fatalf("%s: %s %v, and %q is required", key, a, ok, want)
		}
		return s.reads
	}
	hit := reads(k, `{"n":"last"}`)
	first := reads(earlierKey('c', 0), `{"n":0}`)
	miss := reads(earlierKey('c', 2000000), "")
	if hit != miss || first != miss || miss < 3 {
		t.Fatalf("reads: %d for the newest line, %d for an early line, %d for a miss; all walk the file", hit, first, miss)
	}
	if err := (&ShardCache{S: s, MaxBytes: 16 << 20}).Put(k, json.RawMessage(`{"n":"since"}`)); err != nil {
		t.Fatal(err)
	}
	if after := reads(k, `{"n":"since"}`); after >= miss {
		t.Fatalf("this layout's answer was found in %d reads, no fewer than the earlier file's %d", after, miss)
	}
}

func TestWhatTheCacheCannotListItDoesNotGuess(t *testing.T) {
	s := newMemStore()
	s.files["cache/a.jsonl"] = earlierFile('a', 10)
	have := earlierKey('a', 3)
	if _, ok, err := (&ShardCache{S: s, MaxBytes: 16 << 20}).Get(earlierKey('b', 3)); err != nil || ok || s.reads != 0 {
		t.Fatalf("listed whole, and neither file of the key is there: %v, found %v, %d reads; a miss with no read is required", err, ok, s.reads)
	}
	if a, ok, err := (&ShardCache{S: s, MaxBytes: 16 << 20}).Get(have); err != nil || !ok || string(a) != `{"n":3}` {
		t.Fatalf("listed whole: %s %v %v", a, ok, err)
	}
	unknown := map[string]func() *ShardCache{
		"a store that cannot list": func() *ShardCache { return &ShardCache{S: noListStore{s}, MaxBytes: 16 << 20} },
		"a listing that fails": func() *ShardCache {
			s.failList, s.cutList, s.omit = errors.New("FS_IO"), false, nil
			return &ShardCache{S: s, MaxBytes: 16 << 20}
		},
		"a listing cut short before the file": func() *ShardCache {
			s.failList, s.cutList, s.omit = nil, true, map[string]bool{"cache/a.jsonl": true}
			return &ShardCache{S: s, MaxBytes: 16 << 20}
		},
		"a listing cut short after the file": func() *ShardCache {
			s.failList, s.cutList, s.omit = nil, true, nil
			return &ShardCache{S: s, MaxBytes: 16 << 20}
		},
	}
	for name, cache := range unknown {
		c := cache()
		s.reads, s.writesTo = 0, nil
		if a, ok, err := c.Get(have); err == nil || ok || a != nil {
			t.Fatalf("%s: %s, found %v, error %v; an error and no answer are required", name, a, ok, err)
		}
		if err := c.Put(earlierKey('a', 777777), json.RawMessage(`{"n":-1}`)); err == nil {
			t.Fatalf("%s: an answer was kept in a folder whose contents are not known", name)
		}
		if s.reads != 0 || len(s.writesTo) != 0 {
			t.Fatalf("%s: %d reads and writes %v, and neither is allowed", name, s.reads, s.writesTo)
		}
	}
	empty := newMemStore()
	if _, ok, err := (&ShardCache{S: empty, MaxBytes: 16 << 20}).Get(have); err != nil || ok {
		t.Fatalf("a folder not made yet: %v, found %v; a miss is required", err, ok)
	}
}

func TestAFileNotReadToItsEndIsNeitherAMissNorAHit(t *testing.T) {
	k := earlierKey('c', 1000000)
	earlier := append(line(k, json.RawMessage(`{"n":"first"}`)), earlierFile('c', 3000)...)
	earlier = append(earlier, line(k, json.RawMessage(`{"n":"last"}`))...)
	if len(earlier) < 3*cachePage {
		t.Fatalf("the earlier file is %d bytes, and several pages are needed", len(earlier))
	}
	for _, tc := range []struct {
		name  string
		shard []byte
		fail  map[string]int64
		stall map[string]int64
	}{
		{"the earlier file fails at its first read", nil, map[string]int64{"cache/c.jsonl": 0}, nil},
		{"the earlier file fails after its first page, which holds an older answer", nil, map[string]int64{"cache/c.jsonl": cachePage}, nil},
		{"the earlier file returns nothing after its first page", nil, nil, map[string]int64{"cache/c.jsonl": cachePage}},
		{"the shard cannot be read, and the earlier file holds an older answer", line(k, json.RawMessage(`{"n":"since"}`)), map[string]int64{"cache/c0.jsonl": 0}, nil},
	} {
		s := newMemStore()
		s.files["cache/c.jsonl"] = earlier
		if tc.shard != nil {
			s.files["cache/c0.jsonl"] = tc.shard
		}
		s.failRead, s.stall = tc.fail, tc.stall
		a, ok, err := (&ShardCache{S: s, MaxBytes: 16 << 20}).Get(k)
		if err == nil || ok || a != nil {
			t.Fatalf("%s: answer %s, found %v, error %v; an error and no answer are required", tc.name, a, ok, err)
		}
		if len(s.writesTo) != 0 {
			t.Fatalf("%s: the failed lookup wrote: %v", tc.name, s.writesTo)
		}

		s.failRead, s.stall = nil, nil
		want := `{"n":"last"}`
		if tc.shard != nil {
			want = `{"n":"since"}`
		}
		if a, ok, err := (&ShardCache{S: s, MaxBytes: 16 << 20}).Get(k); err != nil || !ok || string(a) != want {
			t.Fatalf("%s, then read whole: %s %v %v, and %s is required", tc.name, a, ok, err, want)
		}
	}
}

type unreadableCache struct {
	err        error
	after      int
	gets, puts int
}

func (c *unreadableCache) Get(string) (json.RawMessage, bool, error) {
	c.gets++
	if c.gets <= c.after {
		return nil, false, nil
	}
	return nil, false, c.err
}
func (c *unreadableCache) Put(string, json.RawMessage) error { c.puts++; return nil }

func TestAFailedCacheReadAsksTheJudgeNothing(t *testing.T) {
	tb := table(t)
	cause := errors.New("FS_IO")
	down := &unreadableCache{err: cause}
	j := &fakeJudge{p: 0.9, where: "func Add", conf: 0.9}
	recs, err := JudgeText(tb, j, down, "x.go", "go", goFile)
	if !errors.Is(err, ErrCache) || !errors.Is(err, cause) || len(recs) != 0 || j.calls != 0 || j.chosen != 0 || down.puts != 0 || down.gets != 1 {
		t.Fatalf("error %v, %d records, %d judge calls, %d locations, %d lookups, %d kept", err, len(recs), j.calls, j.chosen, down.gets, down.puts)
	}
	s := newMemStore()
	s.files["cache/a.jsonl"] = earlierFile('a', 10)
	s.failList = cause
	recs, err = JudgeText(tb, j, &ShardCache{S: s, MaxBytes: 16 << 20}, "x.go", "go", goFile)
	if !errors.Is(err, ErrCache) || !errors.Is(err, cause) || len(recs) != 0 || j.calls != 0 || s.reads != 0 || len(s.writesTo) != 0 {
		t.Fatalf("the real cache, unlistable: error %v, %d records, %d judge calls, %d reads, writes %v", err, len(recs), j.calls, s.reads, s.writesTo)
	}
}

func TestACacheThatCannotBeReadStopsTheStepAndKeepsTheFilePending(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "a.go", goFile)
	fs := &OSFS{Root: root}
	p := &memoryProgress{}
	s, err := NewScan(fs, "cq_cache", "src", "", nil, nil, false, 100, 131072)
	if err != nil {
		t.Fatal(err)
	}
	s.Progress = p
	down := &unreadableCache{err: errors.New("FS_IO")}
	j := &fakeJudge{p: 0.1}
	recs, err := s.Step(tb, fs, j, down, 12, 50)
	if !errors.Is(err, ErrCache) || len(recs) != 0 || j.calls != 0 || down.puts != 0 || s.Status != "running" || len(s.Pending) != 1 || s.FilesSeen != 0 || s.FilesJudged != 0 || len(p.records) != 0 {
		t.Fatalf("error %v, %d records, %d judge calls, %d kept, status %s, %d pending, seen %d, judged %d, %d saved records",
			err, len(recs), j.calls, down.puts, s.Status, len(s.Pending), s.FilesSeen, s.FilesJudged, len(p.records))
	}
	for _, c := range p.coverage {
		if strings.HasSuffix(c.Ref, "a.go") {
			t.Fatalf("a coverage event was recorded for the file the step stopped at: %+v", c)
		}
	}
	recs, err = s.Step(tb, fs, j, MemCache{}, 12, 50)
	if err != nil || s.Status != "done" || s.FilesJudged != 1 || j.calls == 0 || len(recs) == 0 {
		t.Fatalf("with the cache readable: error %v, status %s, judged %d, %d judge calls, %d records", err, s.Status, s.FilesJudged, j.calls, len(recs))
	}
}

func TestACacheThatFailsAtALaterLookupLeavesWhatWasDoneAndAsksNothingTwice(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "a.go", "package x\n\nfunc Good(a, b int) int {\n\treturn a + b\n}\n\nfunc Bad(path string) string {\n\tb, _ := os.ReadFile(path)\n\treturn string(b) + \"padding padding padding\"\n}\n")
	fs := &OSFS{Root: root}
	p := &memoryProgress{}
	s, err := NewScan(fs, "cq_later", "src", "", nil, nil, false, 100, 131072)
	if err != nil {
		t.Fatal(err)
	}
	s.Progress = p
	later := &unreadableCache{err: errors.New("FS_IO"), after: 1}
	j := &fakeJudge{p: 0.9}
	recs, err := s.Step(tb, fs, j, later, 12, 50)
	if !errors.Is(err, ErrCache) || later.gets != 2 || j.calls != 1 || j.chosen != 0 || later.puts != 1 || len(recs) != 1 || len(p.records) != 1 ||
		s.Status != "running" || len(s.Pending) != 1 || s.FilesJudged != 0 {
		t.Fatalf("error %v, %d lookups, %d judge calls, %d locations asked, %d kept, %d records, %d saved, status %s, %d pending, judged %d",
			err, later.gets, j.calls, j.chosen, later.puts, len(recs), len(p.records), s.Status, len(s.Pending), s.FilesJudged)
	}
	if recs[0].Battery != "code" || len(recs[0].Findings) == 0 {
		t.Fatalf("the battery asked before the failure is not the one saved: %+v", recs[0])
	}
	recs, err = s.Step(tb, fs, j, MemCache{}, 12, 50)
	if err != nil || s.Status != "done" || s.FilesJudged != 1 || j.calls != 2 || j.chosen != 1 || len(recs) != 2 {
		t.Fatalf("the next step: error %v, status %s, judged %d, %d judge calls in all (2: the saved battery is not asked again), %d locations, %d records",
			err, s.Status, s.FilesJudged, j.calls, j.chosen, len(recs))
	}
}

func TestACompactionThatCannotReadItsShardToTheEndPublishesNothing(t *testing.T) {
	answer := json.RawMessage(`"` + strings.Repeat("a", 2000) + `"`)
	fill := func() (*memStore, *ShardCache, []byte) {
		t.Helper()
		s := newMemStore()
		c := &ShardCache{S: s, MaxBytes: 16 * CacheShards * 64 << 10}
		for i := 0; ; i++ {
			if int64(len(s.files["cache/d0.jsonl"])+len(line(key('d', i), answer))) > c.ShardBytes() {
				break
			}
			if err := c.Put(key('d', i), answer); err != nil {
				t.Fatal(err)
			}
		}
		if s.writesTo["cache/d0.jsonl.next"] != 0 {
			t.Fatal("the fixture rewrote the shard while filling it")
		}
		return s, c, append([]byte(nil), s.files["cache/d0.jsonl"]...)
	}
	s, c, before := fill()

	first := int64(len(before)) / 4
	for _, tc := range []struct {
		name  string
		stall int64
	}{
		{"its first read", first},
		{"a later read", int64(len(before)) - 2*cachePage},
	} {
		s, c, before = fill()
		s.stall = map[string]int64{"cache/d0.jsonl": tc.stall}
		err := c.Put(key('d', 99999), answer)
		if !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("%s returns nothing: the Put's error is %v, and a rewrite that failed for no progress is required", tc.name, err)
		}
		if !bytes.Equal(s.files["cache/d0.jsonl"], before) {
			t.Fatalf("%s returns nothing: the shard went from %d bytes to %d, and it must be what it was", tc.name, len(before), len(s.files["cache/d0.jsonl"]))
		}

		s.stall = nil
		if _, ok, err := (&ShardCache{S: s, MaxBytes: c.MaxBytes}).Get(key('d', 0)); err != nil || !ok {
			t.Fatalf("%s returned nothing, and afterwards the shard's oldest answer: found %v, %v", tc.name, ok, err)
		}
	}
}

func TestAReadThatReturnsNothingIsNotTheEndOfTheFile(t *testing.T) {
	s := newMemStore()
	s.files["f"] = bytes.Repeat([]byte("x"), IOPage+100)
	if b, err := ReadWhole(s, "f", 1<<20); err != nil || len(b) != IOPage+100 {
		t.Fatalf("read whole: %d bytes, %v", len(b), err)
	}
	for _, at := range []int64{0, IOPage} {
		s.stall = map[string]int64{"f": at}
		if b, complete, err := ReadPrefix(s, "f", 1<<20); !errors.Is(err, io.ErrNoProgress) || complete || b != nil {
			t.Fatalf("a read that returns nothing at %d: %d bytes, complete %v, %v; an error and no bytes are required", at, len(b), complete, err)
		}
		if _, err := ReadWhole(s, "f", 1<<20); !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("ReadWhole, a read that returns nothing at %d: %v", at, err)
		}
	}
}
