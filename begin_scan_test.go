package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

type recordedFiles struct {
	scans, cache []sdk.FileEntry
	cut          map[string]bool
	fail         map[string]error
	calls        []string
}

func (r *recordedFiles) List(dir string) ([]sdk.FileEntry, bool, error) {
	r.calls = append(r.calls, "list "+dir)
	if err := r.fail[dir]; err != nil {
		return nil, false, err
	}
	if dir == "scans" {
		return r.scans, r.cut[dir], nil
	}
	return r.cache, r.cut[dir], nil
}
func (r *recordedFiles) Write(p string, b []byte) error {
	r.calls = append(r.calls, "write "+p)
	return nil
}
func (r *recordedFiles) Delete(p string) error {
	r.calls = append(r.calls, "delete "+p)
	return nil
}

func (r *recordedFiles) changes() []string {
	var out []string
	for _, c := range r.calls {
		if !strings.HasPrefix(c, "list ") {
			out = append(out, c)
		}
	}
	return out
}

func keptScan(id string, journal int64) []sdk.FileEntry {
	return keptScanAt(id, journal, recent)
}

func keptScanAt(id string, journal int64, modified string) []sdk.FileEntry {
	return []sdk.FileEntry{{Name: id + ".state.json", Size: 100, Modified: modified}, {Name: id + ".v2.jsonl", Size: journal, Modified: modified},
		{Name: id + ".checkpoint.0", Size: 10, Modified: modified}, {Name: id + ".checkpoint.1", Size: 10, Modified: modified}}
}

const (
	recent = "2026-11-12T18:00:00Z"
	stale  = "2026-09-01T12:00:00Z"
)

var testNow, _ = parseRFC3339("2026-11-12T19:00:00Z")

func begin(f scanFiles, s *cq.Scan) ([]any, error) {
	return beginScan(f, s, testNow, defaultRetentionDays, 16<<20, 64<<10)
}

func newScanNamed(id string) *cq.Scan {
	return &cq.Scan{ID: id, Version: cq.RecordVersion, Status: "running"}
}

func TestARefusedStartHasRemovedAndWrittenNothing(t *testing.T) {
	two := append(keptScanAt("cq_100", 1000, stale), keptScan("cq_200", 2000)...)
	wide := make([]string, 4000)
	for i := range wide {
		wide[i] = strings.Repeat("x", 100)
	}
	for _, tc := range []struct {
		name   string
		files  *recordedFiles
		scan   *cq.Scan
		reason string
	}{
		{"the scans folder cannot be listed", &recordedFiles{scans: two, fail: map[string]error{"scans": sdk.Fail("FS_IO", "the host could not read the folder")}}, nil, "FS_IO"},
		{"the scans folder's listing is cut short", &recordedFiles{scans: two, cut: map[string]bool{"scans": true}}, nil, "SCANS_UNLISTABLE"},
		{"the cache folder cannot be listed", &recordedFiles{scans: two, fail: map[string]error{"cache": sdk.Fail("FS_IO", "the host could not read the folder")}}, nil, "FS_IO"},
		{"the cache folder's listing is cut short", &recordedFiles{scans: two, cut: map[string]bool{"cache": true}}, nil, "SCANS_UNLISTABLE"},
		{"too little room, even with the expired scan removed", &recordedFiles{scans: append(keptScanAt("cq_100", 1000, stale), keptScan("cq_200", 60<<20)...)}, nil, "SCAN_STORAGE_FULL"},
		{"a first state too wide for a checkpoint", &recordedFiles{scans: two}, &cq.Scan{ID: "cq_300", Version: cq.RecordVersion, Status: "running", Exclude: wide}, "SCAN_TOO_WIDE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.scan
			if s == nil {
				s = newScanNamed("cq_300")
			}
			_, err := begin(tc.files, s)
			if reasonOf(err) != tc.reason {
				t.Fatalf("refused as %q (%v), and %s is required", reasonOf(err), err, tc.reason)
			}
			if changed := tc.files.changes(); len(changed) != 0 {
				t.Fatalf("a refused start changed files: %v", changed)
			}
		})
	}
}

func TestAStartThatGoesAheadChangesFilesOnlyAfterItsChecks(t *testing.T) {
	files := &recordedFiles{scans: append(keptScanAt("cq_100", 1000, stale), keptScan("cq_200", 2000)...)}
	s := newScanNamed("cq_300")
	removed, err := begin(files, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"list scans", "list cache",
		"delete scans/cq_100.jsonl", "delete scans/cq_100.v2.jsonl", "delete scans/cq_100.intent.jsonl", "delete scans/cq_100.checkpoint.0", "delete scans/cq_100.checkpoint.1", "delete scans/cq_100.attempt.json", "delete scans/cq_100.state.json",
		"delete scans/cq_300.jsonl", "delete scans/cq_300.v2.jsonl", "delete scans/cq_300.intent.jsonl", "delete scans/cq_300.checkpoint.0", "delete scans/cq_300.checkpoint.1", "delete scans/cq_300.attempt.json",
		"write scans/cq_300.v2.jsonl", "write scans/cq_300.intent.jsonl", "write scans/cq_300.checkpoint.0", "write scans/cq_300.checkpoint.1", "write scans/cq_300.state.json",
	}
	if !reflect.DeepEqual(files.calls, want) {
		t.Fatalf("the start's calls, in order:\n%s\nand these are required:\n%s", strings.Join(files.calls, "\n"), strings.Join(want, "\n"))
	}
	if len(removed) != 1 || removed[0].(map[string]any)["scan_id"] != "cq_100" || removed[0].(map[string]any)["expires_at"] != "2026-10-01T12:00:00Z" {
		t.Fatalf("expired_removed: %v", removed)
	}
	if s.JournalCap < minJournalRoom {
		t.Fatalf("the scan's journal bound was not set from the room found: %d", s.JournalCap)
	}
	if _, err := begin(&recordedFiles{fail: map[string]error{"scans": sdk.Fail("FS_NOT_FOUND", "no such folder"), "cache": sdk.Fail("FS_NOT_FOUND", "no such folder")}},
		newScanNamed("cq_300")); err != nil {
		t.Fatalf("a first scan, with neither folder made yet: %v", err)
	}
}

func wordsOf(err error) string {
	var op *sdk.OperationError
	if errors.As(err, &op) {
		return op.Reason
	}
	return ""
}

type liveFiles struct {
	files map[string]sdk.FileEntry
	now   string
}

func newLiveFiles(scans ...[]sdk.FileEntry) *liveFiles {
	l := &liveFiles{files: map[string]sdk.FileEntry{}, now: recent}
	for _, sc := range scans {
		for _, e := range sc {
			l.files[e.Name] = e
		}
	}
	return l
}
func (l *liveFiles) List(dir string) ([]sdk.FileEntry, bool, error) {
	if dir != "scans" {
		return nil, false, nil
	}
	var out []sdk.FileEntry
	for _, e := range l.files {
		out = append(out, e)
	}
	return out, false, nil
}
func (l *liveFiles) Write(p string, b []byte) error {
	name := strings.TrimPrefix(p, "scans/")
	l.files[name] = sdk.FileEntry{Name: name, Size: int64(len(b)), Modified: l.now}
	return nil
}
func (l *liveFiles) Delete(p string) error {
	delete(l.files, strings.TrimPrefix(p, "scans/"))
	return nil
}
func (l *liveFiles) ids() []string {
	var es []sdk.FileEntry
	for _, e := range l.files {
		es = append(es, e)
	}
	return scanIDsOf(es)
}

func TestTwelveStartsKeepTwelveScans(t *testing.T) {
	files := newLiveFiles()
	for i := 12; i >= 1; i-- {
		if _, err := begin(files, newScanNamed(fmt.Sprintf("scan-%02d", i))); err != nil {
			t.Fatalf("start %d: %v", 13-i, err)
		}
	}
	if ids := files.ids(); len(ids) != 12 {
		t.Fatalf("%d scans kept after twelve starts: %v", len(ids), ids)
	}
}

func TestAnExpiredScanIsRemovedByItsTimeNotItsName(t *testing.T) {
	files := newLiveFiles(keptScanAt("aaa-first-by-name", 1000, recent), keptScanAt("zzz-expired", 1000, stale))
	removed, err := begin(files, newScanNamed("new"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := files.ids(); !reflect.DeepEqual(ids, []string{"aaa-first-by-name", "new"}) {
		t.Fatalf("scans kept: %v", ids)
	}
	if len(removed) != 1 || removed[0].(map[string]any)["scan_id"] != "zzz-expired" {
		t.Fatalf("expired_removed: %v", removed)
	}
}

func TestARetiredKeepScansRemovesNothing(t *testing.T) {
	var five [][]sdk.FileEntry
	for i := 0; i < 5; i++ {
		five = append(five, keptScan(fmt.Sprintf("cq_%d", i), 1000))
	}
	files := newLiveFiles(five...)
	if _, err := begin(files, newScanNamed("cq_9")); err != nil {
		t.Fatal(err)
	}
	if ids := files.ids(); len(ids) != 6 {
		t.Fatalf("%d scans kept: %v", len(ids), ids)
	}
	scans, _, _ := files.List("scans")
	load := func(id string) (*cq.Scan, error) { return newScanNamed(id), nil }
	out := scanList(mustConfig(t, values{"keep_scans": 2}), scans, load)
	notice, _ := out["keep_scans_notice"].(string)
	if len(out["scans"].([]any)) != 6 || !strings.Contains(notice, "keep_scans is set to 2") || !strings.Contains(notice, "ignores it") || out["scan_retention_days"] != 30 {
		t.Fatalf("scan list: %v", out)
	}
	for _, v := range out["scans"].([]any) {
		if at := v.(map[string]any)["expires_at"]; at != "2026-12-12T18:00:00Z" {
			t.Fatalf("expires_at %v, want 30 days after the last write", at)
		}
	}
	if _, ok := scanList(mustConfig(t, values{}), scans, load)["keep_scans_notice"]; ok {
		t.Fatal("a notice with keep_scans not set")
	}
	if _, err := sdk.ResultSize(out); err != nil {
		t.Fatalf("the scan list is no result: %v", err)
	}
}

func TestARefusalForRoomSaysWhatIsKeptAndRemovesNothing(t *testing.T) {
	large := append(keptScan("cq_100", 1000), keptScan("cq_200", 60<<20)...)
	for _, tc := range []struct {
		name          string
		files         *recordedFiles
		says, saysNot []string
	}{
		{"two unexpired scans", &recordedFiles{scans: large},
			[]string{"no scan was started and nothing was removed", "KiB more is needed", "scans, 2 (61441 KiB); answers from before this version, 0 KiB; newer answers, 0 KiB; against the 64 MiB",
				"cq_200 (61440 KiB, expires 2026-12-12T18:00:00Z)", "scan action remove", "cache_max_bytes holds back 16384 KiB"},
			[]string{"No setting removes", "keep_scans"}},
		{"the answers from before this version take the room", &recordedFiles{scans: keptScan("cq_100", 1000),
			cache: []sdk.FileEntry{{Name: "a.jsonl", Size: 46 << 20}, {Name: "ab.jsonl", Size: 1 << 20}}},
			[]string{"scans, 1 (1 KiB); answers from before this version, 47104 KiB; newer answers, 1024 KiB", "cache_max_bytes holds back 15360 KiB",
				"No setting removes the answers from before this version"},
			nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := begin(tc.files, newScanNamed("cq_300"))
			if reasonOf(err) != "SCAN_STORAGE_FULL" {
				t.Fatalf("refused as %q (%v), and SCAN_STORAGE_FULL is required", reasonOf(err), err)
			}
			if changed := tc.files.changes(); len(changed) != 0 {
				t.Fatalf("a refused start changed files: %v", changed)
			}
			words := wordsOf(err)
			for _, s := range tc.says {
				if !strings.Contains(words, s) {
					t.Fatalf("the refusal does not say %q:\n%s", s, words)
				}
			}
			for _, s := range tc.saysNot {
				if strings.Contains(words, s) {
					t.Fatalf("the refusal says %q, which is not so here:\n%s", s, words)
				}
			}
		})
	}
}

func TestTheRoomPlanHoldsEarlierAnswersAsKeptAndLetsOnlyNewerOnesGrow(t *testing.T) {
	room := func(cache ...sdk.FileEntry) int64 {
		t.Helper()
		s := newScanNamed("cq_300")
		if _, err := begin(&recordedFiles{cache: cache}, s); err != nil {
			t.Fatal(err)
		}
		return s.JournalCap
	}

	empty := int64(privateQuota - 16<<20 - journalReserve - 64<<10)
	if got := room(); got != journalCapOf(empty) {
		t.Fatalf("nothing kept: a journal bound of %d, and of the quota less the cache's bound, the reserve and a share, %d, is required", got, journalCapOf(empty))
	}
	earlier, newer := sdk.FileEntry{Name: "a.jsonl", Size: 8 << 20}, sdk.FileEntry{Name: "ab.jsonl", Size: 8 << 20}
	if got := room(earlier); got != journalCapOf(empty-8<<20) {
		t.Fatalf("8 MiB of earlier answers: %d bytes of room, and %d is required", got, journalCapOf(empty-8<<20))
	}
	if got := room(newer); got != journalCapOf(empty) {
		t.Fatalf("8 MiB of newer answers, inside the bound: %d bytes of room, and %d is required", got, journalCapOf(empty))
	}
	if got := room(earlier, newer); got != journalCapOf(empty-8<<20) {
		t.Fatalf("8 MiB of each: %d bytes of room, and %d is required", got, journalCapOf(empty-8<<20))
	}
	if got := room(sdk.FileEntry{Name: "ab.jsonl.next", Size: 1 << 20}); got != journalCapOf(empty-1<<20) {
		t.Fatalf("a file of neither layout: %d bytes of room, and %d is required", got, journalCapOf(empty-1<<20))
	}
}

func TestAStartThatWouldPassOneListingIsRefusedAndRemovesNothing(t *testing.T) {

	var folder []sdk.FileEntry
	for i := 0; i < 254; i++ {
		folder = append(folder, keptScan(fmt.Sprintf("cq_%04d", i), 10)...)
	}
	for i := 0; i < 2; i++ {
		folder = append(folder, sdk.FileEntry{Name: fmt.Sprintf("leftover.%d", i), Size: 1})
	}
	if len(folder)+newScanFiles != listingMost {
		t.Fatalf("the test's folder: %d files", len(folder))
	}
	start := func(scans []sdk.FileEntry) (*recordedFiles, error) {
		files := &recordedFiles{scans: scans}
		_, err := begin(files, newScanNamed("cq_9000"))
		return files, err
	}
	if _, err := start(folder); err != nil {
		t.Fatalf("a start that fills one listing exactly: %v", err)
	}
	over := append(folder[:len(folder):len(folder)], sdk.FileEntry{Name: "cq_0000.kept-aside", Size: 1})
	files, err := start(over)
	if reasonOf(err) != "SCAN_STORAGE_FULL" {
		t.Fatalf("refused as %q (%v), and SCAN_STORAGE_FULL is required", reasonOf(err), err)
	}
	if changed := files.changes(); len(changed) != 0 {
		t.Fatalf("a refused start changed files: %v", changed)
	}
	for _, s := range []string{"more files than the 1024 one listing returns", "no scan was started and nothing was removed", "scans, 254 (", "and 214 more."} {
		if !strings.Contains(wordsOf(err), s) {
			t.Fatalf("the refusal does not say %q:\n%s", s, wordsOf(err))
		}
	}

	expired := append(over[:len(over):len(over)], nil...)
	for i := range expired {
		if strings.HasPrefix(expired[i].Name, "cq_0253.") {
			expired[i].Modified = stale
		}
	}
	files, err = start(expired)
	if err != nil {
		t.Fatalf("the same start with one scan expired: %v", err)
	}
	for _, c := range files.changes() {
		if strings.HasPrefix(c, "delete ") && !strings.Contains(c, "cq_0253.") && !strings.Contains(c, "cq_9000.") {
			t.Fatalf("an unexpired scan was removed: %s", c)
		}
	}
}

func TestRemoveTakesNamedScansOrNone(t *testing.T) {
	files := newLiveFiles(keptScan("a", 1000), keptScan("b", 2000))
	for _, ids := range [][]string{nil, {"a", "nope"}, {"a", "a"}, {"../x"}} {
		if _, err := removeScans(files, ids); err == nil || len(files.ids()) != 2 {
			t.Fatalf("remove %v: %v, scans %v", ids, err, files.ids())
		}
	}
	removed, err := removeScans(files, []string{"b"})
	if err != nil || len(removed) != 1 || removed[0].(map[string]any)["scan_id"] != "b" || !reflect.DeepEqual(files.ids(), []string{"a"}) {
		t.Fatalf("remove b: %v %v, scans %v", removed, err, files.ids())
	}
}

func TestTimesReadBackAsWritten(t *testing.T) {
	for _, sec := range []int64{0, 951782400, 1709208000, 1793644800, 4102444799, 1762970400} {
		if got, ok := parseRFC3339(rfc3339(sec)); !ok || got != sec {
			t.Errorf("%d -> %s -> %d %v", sec, rfc3339(sec), got, ok)
		}
	}
	for _, bad := range []string{"", "2026-11-12 18:00:00Z", "2026-13-01T00:00:00Z", "2026-11-12T18:00:00+01:00", "x026-11-12T18:00:00Z"} {
		if _, ok := parseRFC3339(bad); ok {
			t.Errorf("%q read as a time", bad)
		}
	}

	if h := heldScans(keptScanAt("odd", 10, "yesterday"), 30); len(h) != 1 || h[0].Expires != 0 || len(expiredOf(h, 1<<40, "")) != 0 {
		t.Fatalf("held %+v", h)
	}
}

func TestAStartReservesTheAttemptRecordAStepMayLeave(t *testing.T) {
	files := &recordedFiles{}
	if _, err := begin(files, newScanNamed("cq_9000")); err != nil {
		t.Fatal(err)
	}
	wrote := 0
	for _, change := range files.changes() {
		if strings.HasPrefix(fmt.Sprint(change), "write ") {
			wrote++
		}
	}
	if wrote != 5 || wrote+1 != newScanFiles {
		t.Fatalf("a start writes %d files (%v) and a step may leave the scan's attempt record: %d are reserved", wrote, files.changes(), newScanFiles)
	}
}
