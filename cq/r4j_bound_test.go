package cq

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestR4JAStateAtEveryBoundWithACursorAtItsCeilingFitsTheCheckpoint(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": goodFile("A")})
	fs := &OSFS{Root: dir}
	m := newMemStore()
	m.files["cp0"], m.files["cp1"] = nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}

	langs := []string{"go"}
	cur := &FileCursor{Ref: "d/big.go", Size: MaxSourceBytes, Digest: strings.Repeat("a", 64), Windows: 4000, Segs: make([][5]int64, 8)}
	for i := 0; ; i++ {
		cur.Outline = append(cur.Outline, OutlineItem{Kind: "func", Name: fmt.Sprintf("F%05d", i), Sig: strings.Repeat("<", 150), Lines: 1})
		if !cursorFits(CursorBytes, cur, "go", langs) {
			cur.Outline = cur.Outline[:len(cur.Outline)-1]
			break
		}
	}
	if !cursorFits(CursorBytes, cur, "go", langs) {
		t.Fatal("the cursor built to the ceiling does not fit it")
	}
	cur.Lang, cur.Langs = "go", langs
	cur.Next, cur.Done, cur.Partial, cur.Calls, cur.Lines, cur.Top = 7, 4000, 4000, 99999, 70000, 70000
	cursor, _ := json.Marshal(cur)
	if len(cursor) > CursorBytes || len(cursor) < CursorBytes-2048 {
		t.Fatalf("a cursor cursorFits admits encodes to %d bytes with its counters at their largest; want it at the ceiling of %d and not past it", len(cursor), CursorBytes)
	}
	s.File = cur

	name := strings.Repeat("n", 246)
	pending := []Entry{{Name: "big.go", Size: MaxSourceBytes}}
	for i := 0; ; i++ {
		e := Entry{Name: fmt.Sprintf("%04d%s", i, name), Size: 123456}
		if !frontierFits(FrontierBytes, "", nil, nil, append(pending, e)) {
			break
		}
		pending = append(pending, e)
	}
	if len(pending) < 100 {
		t.Fatalf("the widest frontier admitted is %d entries", len(pending))
	}
	s.Pending = pending
	for i := 0; i < 400; i++ {
		n := fmt.Sprintf("d/%03d%s", i, name)
		s.Refused = keepName(s.Refused, n, NotFinishedListed)
		s.NotFinished = keepName(s.NotFinished, n, NotFinishedListed)
		s.namePlanTooLarge(n[:244])
	}
	if b, _ := json.Marshal(s.PlanTooLarge); len(s.PlanTooLarge) == 0 || len(s.PlanTooLarge) > PlanTooLargeListed || len(b) > PlanTooLargeBytes {
		t.Fatalf("the files refused for their plan are %d names in %d bytes, outside the list's bounds", len(s.PlanTooLarge), len(b))
	}
	for i := 0; i < TooWideListed; i++ {
		s.TooWide = append(s.TooWide, fmt.Sprintf("%02d%s", i, name[:238]))
	}
	if tw, _ := json.Marshal(s.TooWide); len(tw) > TooWideBytes {
		t.Fatalf("the test's list of folders is %d bytes, past the bound it stands for", len(tw))
	}
	s.LastError = strings.Repeat("e", 1<<10)
	s.Runtime = json.RawMessage(`{"pad":"` + strings.Repeat("r", 2<<10) + `"}`)
	state, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	frontier, _ := json.Marshal(s.Pending)
	pair := CheckpointPair{Store: m, Paths: [2]string{"cp0", "cp1"}}
	err = pair.Save(state)
	t.Logf("state %d bytes of %d: frontier %d (%d entries), cursor %d (%d outline items), files refused for their plan %d named, room left %d",
		len(state), CheckpointBytes, len(frontier), len(pending), len(cursor), len(cur.Outline), len(s.PlanTooLarge), CheckpointBytes-len(state))
	if err != nil {
		t.Fatalf("a state at every bound at once does not fit the checkpoint: %v", err)
	}
}
