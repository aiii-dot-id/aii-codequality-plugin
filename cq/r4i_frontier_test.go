package cq

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestR4ITheFrontierLeavesRoomForACursorAndTheNameLists(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; b.Len() < MaxSourceBytes-64; i++ {
		fmt.Fprintf(&b, "var v%07d = %d\n", i, i)
	}
	writeFiles(t, dir, map[string]string{"big.go": b.String()})
	fs := &OSFS{Root: dir}
	m := newMemStore()
	m.files["journal"], m.files["intent"], m.files["cp0"], m.files["cp1"] = nil, nil, nil, nil
	s, err := NewScan(fs, "s", ".", "", nil, nil, false, 100, SegmentBytes)
	if err != nil {
		t.Fatal(err)
	}

	cur, _, err := PlanFile(table(t), fs, "big.go", "big.go", "go", ChunkBytes, false)
	if err != nil || cur == nil || len(cur.Segs) < 2 {
		t.Fatalf("the large file was not planned as one of several segments: %v", err)
	}
	cur.Lang, cur.Langs = "go", []string{"go"}
	s.File = cur
	cursor, _ := json.Marshal(s.File)

	name := strings.Repeat("n", 246)
	pending := []Entry{{Name: "big.go", Size: int64(b.Len())}}
	for i := 0; ; i++ {
		e := Entry{Name: fmt.Sprintf("%04d%s", i, name), Size: 123456}
		if !frontierFits(FrontierBytes, "", nil, nil, append(pending, e)) {
			break
		}
		pending = append(pending, e)
	}
	if !frontierFits(FrontierBytes, "", nil, nil, pending) || len(pending) < 100 {
		t.Fatalf("the widest frontier admitted is %d entries", len(pending))
	}
	s.Pending = pending
	for i := 0; i < 400; i++ {
		n := fmt.Sprintf("d/%03d%s", i, name)
		s.Refused = keepName(s.Refused, n, NotFinishedListed)
		s.NotFinished = keepName(s.NotFinished, n, NotFinishedListed)
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
	t.Logf("state %d bytes of %d: frontier %d (%d entries), cursor %d (%d segments, %d outline items), names %d + %d, room left %d",
		len(state), CheckpointBytes, len(frontier), len(pending), len(cursor), len(s.File.Segs), len(s.File.Outline),
		len(strings.Join(s.Refused, "")), len(strings.Join(s.NotFinished, "")), CheckpointBytes-len(state))
	if err != nil {
		t.Fatalf("a state at every bound at once does not fit the checkpoint: %v", err)
	}
	if len(frontier) > FrontierBytes+2 {
		t.Errorf("the frontier admitted encodes to %d bytes, past FrontierBytes %d", len(frontier), FrontierBytes)
	}
}
