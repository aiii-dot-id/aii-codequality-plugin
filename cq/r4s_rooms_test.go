package cq

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestR4SAStateAtEveryBoundFitsWhateverTheScanWasStartedWith(t *testing.T) {
	amp := func(n int) string { return strings.Repeat("&", n) }
	heavy := make([]string, 64)
	for i := range heavy {
		heavy[i] = amp(256)
	}
	languages := make([]string, 64)
	for i := range languages {
		languages[i] = fmt.Sprintf("%02d%s", i, amp(30))
	}
	small := `{"pin":{"backend":"jev","endpoint":"https://api.example.test/v1/chat","requested":"jev"},"retry":{"attempts":0,"not_before_ms":0}}`
	long := `{"pin":{"backend":"local","endpoint":"http://h/` + strings.Repeat(`\u0026`, 4087) + `","requested":"m","model":"m"},"retry":{"attempts":0,"not_before_ms":0}}`
	cases := []struct {
		name      string
		root      string
		exclude   []string
		languages []string
		runtime   string
		whole     bool
	}{
		{"an ordinary start", "internal", nil, nil, small, true},
		{"the heaviest patterns and the longest address", "internal", heavy, nil, long, false},
		{"every start argument at its schema bound", "/" + amp(511), heavy, languages, long, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"a.go": goodFile("A")})
			m := newMemStore()
			m.files["cp0"], m.files["cp1"] = nil, nil
			s, err := NewScan(&OSFS{Root: dir}, strings.Repeat("i", 64), c.root, "", c.exclude, c.languages, false, 100, SegmentBytes)
			if err != nil {
				t.Fatal(err)
			}
			s.Runtime = json.RawMessage(c.runtime)
			fr, cr := s.rooms()
			if c.whole != (fr == FrontierBytes && cr == CursorBytes) {
				t.Fatalf("rooms %d and %d of %d and %d; whole rooms wanted: %v", fr, cr, FrontierBytes, CursorBytes, c.whole)
			}
			if fr < 16<<10 || cr < 8<<10 {
				t.Fatalf("rooms %d and %d: a start its schema allows leaves the walk next to nothing", fr, cr)
			}

			langs := c.languages
			if langs == nil {
				langs = []string{"go"}
			}
			cur := &FileCursor{Ref: "d/big.go", Size: MaxSourceBytes, Digest: strings.Repeat("a", 64), Windows: 4000, Segs: make([][5]int64, 8)}
			for i := 0; ; i++ {
				cur.Outline = append(cur.Outline, OutlineItem{Kind: "func", Name: fmt.Sprintf("F%05d", i), Sig: strings.Repeat("<", 20), Lines: 1})
				if !cursorFits(cr, cur, "go", langs) {
					cur.Outline = cur.Outline[:len(cur.Outline)-1]
					break
				}
			}
			if len(cur.Outline) == 0 || !cursorFits(cr, cur, "go", langs) {
				t.Fatalf("no cursor fits a room of %d", cr)
			}
			cur.Lang, cur.Langs = "go", langs
			cur.Next, cur.Done, cur.Partial, cur.Calls, cur.Lines, cur.Top = 7, 4000, 4000, 99999, 70000, 70000
			cursor, _ := json.Marshal(cur)
			s.File = cur

			s.Dir = "d/" + amp(250)
			name := strings.Repeat("n", 246)
			var pending []Entry
			for i := 0; ; i++ {
				e := Entry{Name: fmt.Sprintf("%04d%s", i, name), Size: 123456}
				if !frontierFits(fr, s.Dir, nil, nil, append(pending, e)) {
					break
				}
				pending = append(pending, e)
			}
			if len(pending) < 20 {
				t.Fatalf("the widest frontier admitted in a room of %d is %d entries", fr, len(pending))
			}
			s.Pending = pending
			frontier, _ := json.Marshal(s.Pending)

			for i := 0; i < 400; i++ {
				n := fmt.Sprintf("%03d%s", i, amp(200))
				s.Refused = keepName(s.Refused, n, NotFinishedListed)
				s.NotFinished = keepName(s.NotFinished, n, NotFinishedListed)
			}
			for i := 0; i < 2*TooWideListed; i++ {
				list := &s.TooWide
				if i%2 == 1 {
					list = &s.Truncated
				}
				s.keepFolder(list, fmt.Sprintf("%02d%s", i, amp(40)))
			}
			for i := 0; i < 2*PlanTooLargeListed; i++ {
				s.namePlanTooLarge(fmt.Sprintf("%02d%s", i, amp(39)))
			}
			if len(s.Refused) < 10 || len(s.TooWide)+len(s.Truncated) < 10 || len(s.PlanTooLarge) < 4 {
				t.Fatalf("precondition: the lists hold %d, %d + %d and %d names; they were to be near their bounds",
					len(s.Refused), len(s.TooWide), len(s.Truncated), len(s.PlanTooLarge))
			}
			s.LastError = amp(1<<10) + " … (99999 bytes in all)"
			s.ProgressOffset, s.IntentOffset, s.JournalCap, s.MaxFileBytes = math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64
			s.MaxFiles, s.ChunkBytes, s.DirsListed, s.FilesListed = math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt
			s.FilesSeen, s.FilesJudged, s.Chunks, s.Calls = math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt
			s.SlopCalls, s.IntentCalls, s.CacheHits = math.MaxInt, math.MaxInt, math.MaxInt
			s.IncludeTests, s.IncludeGenerated, s.Status = true, true, StatusExaminedCap
			for i := 0; i < 32; i++ {
				s.Excluded[fmt.Sprintf("reason_%02d_%s", i, strings.Repeat("r", 22))] = math.MaxInt
			}
			for i := 0; i < 64; i++ {
				s.LangCounts[fmt.Sprintf("%02d%s", i, strings.Repeat("l", 30))] = math.MaxInt
			}

			grown := c.runtime[:len(c.runtime)-1] + `,"g":"` + strings.Repeat("g", RuntimeGrowth-7) + `"}`
			if len(grown)-len(c.runtime) != RuntimeGrowth {
				t.Fatalf("precondition: the request state grew by %d, not %d", len(grown)-len(c.runtime), RuntimeGrowth)
			}
			s.Runtime = json.RawMessage(grown)

			state, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			pair := CheckpointPair{Store: m, Paths: [2]string{"cp0", "cp1"}}
			err = pair.Save(state)
			t.Logf("state %d bytes of %d: rooms %d and %d, frontier %d (%d entries), cursor %d (%d outline items), what the scan cannot shed %d, room left %d",
				len(state), CheckpointBytes, fr, cr, len(frontier), len(pending), len(cursor), len(cur.Outline), s.metadataBytes(), CheckpointBytes-len(state))
			if err != nil {
				t.Fatalf("a state at every bound at once does not fit the checkpoint: %v", err)
			}
		})
	}
}
