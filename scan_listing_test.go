package main

import (
	"errors"
	"reflect"
	"testing"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func TestAListingThatFailedOrWasCutIsNotAnEmptyOne(t *testing.T) {
	files := []sdk.FileEntry{{Name: "cq_2.state.json"}, {Name: "cq_2.v2.jsonl"}, {Name: "named.state.json"}, {Name: "cq_1.state.json"}, {Name: "cq_1.checkpoint.0"}}
	es, err := listing(files, false, nil)
	if err != nil || !reflect.DeepEqual(scanIDsOf(es), []string{"cq_1", "cq_2", "named"}) {
		t.Fatalf("a whole listing: %v, %v", es, err)
	}
	es, err = listing(nil, false, sdk.Fail("FS_NOT_FOUND", "no such folder"))
	if err != nil || len(es) != 0 {
		t.Fatalf("a folder not made yet: %v, %v", es, err)
	}
	failed := sdk.Fail("FS_IO", "the host could not read the folder")
	es, err = listing(nil, false, failed)
	if !errors.Is(err, error(failed)) || es != nil {
		t.Fatalf("a failed listing was read as %v, %v", es, err)
	}
	other := errors.New("no reply from the host")
	if es, err = listing(nil, false, other); !errors.Is(err, other) || es != nil {
		t.Fatalf("a failed call was read as %v, %v", es, err)
	}
	es, err = listing(files, true, nil)
	if reasonOf(err) != "SCANS_UNLISTABLE" || es != nil {
		t.Fatalf("a listing cut short was read as %v, %v", es, err)
	}
}

func TestHeldScansCountOnlyTheirOwnFiles(t *testing.T) {
	scan := func(id string, state, journal int64, at string) []sdk.FileEntry {
		return []sdk.FileEntry{{Name: id + ".state.json", Size: state, Modified: at}, {Name: id + ".v2.jsonl", Size: journal, Modified: at},
			{Name: id + ".checkpoint.0", Size: 10, Modified: at}, {Name: id + ".checkpoint.1", Size: 10, Modified: at}}
	}
	var folder []sdk.FileEntry
	folder = append(folder, scan("cq_300", 100, 3000, "2026-11-10T00:00:00Z")...)
	folder = append(folder, scan("cq_100", 100, 1000, "2026-10-01T00:00:00Z")...)
	folder = append(folder, scan("a-named-scan", 100, 500, "2026-09-01T00:00:00Z")...)
	folder = append(folder, scan("cq_200", 100, 2000, "2026-09-20T00:00:00Z")...)
	folder = append(folder, sdk.FileEntry{Name: "cq_050.v2.jsonl", Size: 7, Modified: "2020-01-01T00:00:00Z"})
	folder = append(folder, sdk.FileEntry{Name: "cq_100.kept-aside", Size: 5, Modified: "2020-01-01T00:00:00Z"})
	held := heldScans(folder, 30)
	want := map[string]int64{"cq_100": 1120, "cq_200": 2120, "cq_300": 3120, "a-named-scan": 620}
	if len(held) != 4 {
		t.Fatalf("held %+v", held)
	}
	for _, h := range held {
		if h.Bytes != want[h.ID] || h.Expires != h.LastWrite+30*86400 {
			t.Fatalf("%s: %d bytes, last write %s, expires %s", h.ID, h.Bytes, rfc3339(h.LastWrite), rfc3339(h.Expires))
		}
	}
	now, _ := parseRFC3339("2026-10-25T00:00:00Z")
	var ids []string
	for _, h := range expiredOf(held, now, "") {
		ids = append(ids, h.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a-named-scan", "cq_200"}) {
		t.Fatalf("expired at 2026-10-25, oldest first: %v", ids)
	}
	if got := expiredOf(held, now, "a-named-scan"); len(got) != 1 || got[0].ID != "cq_200" {
		t.Fatalf("the scan being started is never expired: %+v", got)
	}

	stepped := scan("cq_400", 100, 100, "2026-09-01T00:00:00Z")
	stepped[3].Modified = "2026-10-20T00:00:00Z"
	if h := heldScans(stepped, 30); len(h) != 1 || rfc3339(h[0].LastWrite) != "2026-10-20T00:00:00Z" || len(expiredOf(h, now, "")) != 0 {
		t.Fatalf("a scan stepped on 2026-10-20: %+v", h)
	}
	if h := heldScans(nil, 30); len(h) != 0 {
		t.Fatalf("an empty folder holds %+v", h)
	}
}
