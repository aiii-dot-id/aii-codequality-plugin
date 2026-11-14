package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
)

type hostContinuation struct {
	MoreWork       bool   `json:"more_work"`
	Progress       uint64 `json:"progress"`
	RetryNotBefore string `json:"retry_not_before"`
}

func decodeAsHost(t *testing.T, answer any) hostContinuation {
	t.Helper()
	b, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var c hostContinuation
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("the host cannot read the answer: %v\n%s", err, b)
	}
	return c
}

func TestProgressIsGivenOnlyWhenThisStepSavedWork(t *testing.T) {
	saved := continuing(map[string]any{"progress": "12 of about 40 files"}, 5000, 4000)
	if got := decodeAsHost(t, saved).Progress; got != 5000 {
		t.Fatalf("a step that saved work: progress %d", got)
	}
	if saved["scan_progress"] != "12 of about 40 files" {
		t.Fatalf("the words for a reader are lost: %v", saved)
	}
	idle := continuing(map[string]any{"progress": "12 of about 40 files"}, 5000, 5000)
	if _, has := idle["progress"]; has || decodeAsHost(t, idle).Progress != 0 {
		t.Fatalf("a step that saved nothing claims progress: %v", idle)
	}
	if idle["scan_progress"] != "12 of about 40 files" {
		t.Fatalf("the words for a reader are lost: %v", idle)
	}
}

func TestProgressRisesWithEachStepThatSavesWork(t *testing.T) {
	var last uint64
	step := func(name string, view map[string]any, saved, before int64) {
		t.Helper()
		got := decodeAsHost(t, continuing(view, saved, before)).Progress
		if got <= last {
			t.Fatalf("%s: progress %d after %d", name, got, last)
		}
		last = got
	}
	const journal = 7000
	step("the first step after the upgrade", map[string]any{}, journal+40, journal)
	step("the next", map[string]any{}, journal+900, journal+40)
	step("the one after", map[string]any{}, journal+901, journal+900)
}

func TestTheRetryTimeIsRFC3339AndNotEarly(t *testing.T) {
	for _, ms := range []int64{1, 999, 1000, 1001, 1794355199001, 1794355200000, 1835395199999} {
		got := decodeAsHost(t, continuing(map[string]any{"retry_not_before_ms": ms}, 0, 0)).RetryNotBefore
		at, err := time.Parse(time.RFC3339, got)
		if err != nil {
			t.Fatalf("%d ms: %q is not RFC 3339: %v", ms, got, err)
		}
		if early := at.UnixMilli() < ms; early || at.UnixMilli()-ms >= 1000 || at.Location() != time.UTC {
			t.Fatalf("%d ms became %q (%d ms)", ms, got, at.UnixMilli())
		}
	}
	if view := continuing(map[string]any{}, 0, 0); view["retry_not_before"] != nil {
		t.Fatalf("a retry time with none held: %v", view)
	}
}

func TestRFC3339AgreesWithTheStandardLibrary(t *testing.T) {
	for _, s := range []string{
		"1970-01-01T00:00:00Z", "1969-12-31T23:59:59Z", "1900-03-01T00:00:00Z", "2000-02-29T12:34:56Z",
		"2026-11-10T23:59:59Z", "2026-12-31T23:59:59Z", "2027-01-01T00:00:00Z", "2028-02-29T00:00:00Z",
		"2028-03-01T00:00:00Z", "2100-02-28T23:59:59Z", "2100-03-01T00:00:00Z", "2400-02-29T00:00:01Z",
	} {
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		if got := rfc3339(at.Unix()); got != s {
			t.Fatalf("%d s: %q, and the standard library says %q", at.Unix(), got, s)
		}
	}
	for sec := int64(-3 * 366 * 86400); sec < 140*366*86400; sec += 86400*37 + 3601 {
		if got, want := rfc3339(sec), time.Unix(sec, 0).UTC().Format(time.RFC3339); got != want {
			t.Fatalf("%d s: %q, and the standard library says %q", sec, got, want)
		}
	}
}

func TestMoreWorkIsAnUnfinishedScanAStepCanAdvance(t *testing.T) {
	scan := func(id, status string, version int) *cq.Scan {
		return &cq.Scan{ID: id, Version: version, MaxFileBytes: 128 << 10, Status: status}
	}
	for _, tc := range []struct {
		name  string
		scans []*cq.Scan
		after string
		want  bool
	}{
		{"the stepped scan goes on", []*cq.Scan{scan("a", "running", cq.RecordVersion)}, "running", true},
		{"the stepped scan waits on the judge", []*cq.Scan{scan("a", "running", cq.RecordVersion)}, "judge_unavailable", true},
		{"it finished, and nothing else is unfinished", []*cq.Scan{scan("a", "running", cq.RecordVersion), scan("b", "done", cq.RecordVersion)}, "done", false},
		{"it finished, and another is unfinished", []*cq.Scan{scan("a", "running", cq.RecordVersion), scan("b", "running", cq.RecordVersion)}, "done", true},
		{"it finished, and the other is for reports only", []*cq.Scan{scan("a", "running", cq.RecordVersion), scan("b", "running", 1)}, "done", false},
		{"it stopped at its storage cap", []*cq.Scan{scan("a", "running", cq.RecordVersion)}, "storage_cap", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			byID, ids := map[string]*cq.Scan{}, []string{}
			for _, s := range tc.scans {
				byID[s.ID] = s
				ids = append(ids, s.ID)
			}
			if !unfinished(tc.scans[0]) {
				t.Fatalf("the fixture's first scan cannot be stepped: %+v", tc.scans[0])
			}
			out, err := advanceScans(ids, func(id string) (*cq.Scan, error) { return byID[id], nil }, func(s *cq.Scan) (any, error) {
				s.Status = tc.after
				return map[string]any{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := decodeAsHost(t, out).MoreWork; got != tc.want || out.(map[string]any)["advanced"] != "a" {
				t.Fatalf("more_work %t, and %t is required: %v", got, tc.want, out)
			}
		})
	}
	none, err := advanceScans([]string{"b"}, func(string) (*cq.Scan, error) { return scan("b", "done", cq.RecordVersion), nil }, nil)
	if err != nil || decodeAsHost(t, none).MoreWork {
		t.Fatalf("nothing to step, and more work is claimed: %v %v", none, err)
	}
}
