package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
)

func TestStartAndStepResultsNameTheirScan(t *testing.T) {
	cfg := mustConfig(t, values{})
	for _, s := range []*cq.Scan{
		{ID: "cq_running", Version: cq.RecordVersion, MaxFileBytes: 128 << 10, Status: "running"},
		{ID: "cq_done", Version: cq.RecordVersion, MaxFileBytes: 128 << 10, Status: "done"},
		{ID: "cq_legacy", Version: cq.RecordVersion - 1, Status: "running"},
	} {

		b, err := json.Marshal(scanView(cfg, s))
		if err != nil {
			t.Fatal(err)
		}
		var seen map[string]any
		if err := json.Unmarshal(b, &seen); err != nil {
			t.Fatal(err)
		}
		if id, _ := seen["scan_id"].(string); id != s.ID {
			t.Errorf("%s: the result's scan_id is %v, not the scan's id: %s", s.ID, seen["scan_id"], b)
		}
		if status, _ := seen["status"].(string); status == "" {
			t.Errorf("%s: the result's status is %v, not a status: %s", s.ID, seen["status"], b)
		}
	}
}

func TestRemoveResultCarriesWhatTheSchemaRequires(t *testing.T) {
	raw, err := os.ReadFile("schemas/scan_out.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) == 0 {
		t.Fatal("schemas/scan_out.json requires nothing: this test would pass on any result")
	}

	files := newLiveFiles(keptScan("a", 1000), keptScan("b", 2000), keptScan("c", 3000))
	got, err := scanRemove(files, []string{"a", "c"})
	if err != nil {
		t.Fatalf("remove a, c: %v", err)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var seen map[string]any
	if err := json.Unmarshal(b, &seen); err != nil {
		t.Fatal(err)
	}
	for _, name := range schema.Required {
		v, ok := seen[name]
		if !ok {
			t.Errorf("remove's result lacks %q, which schemas/scan_out.json requires: %s", name, b)
			continue
		}
		if want, _ := schema.Properties[name]["type"].(string); want == "string" {
			if _, ok := v.(string); !ok {
				t.Errorf("remove's result has %q as %T, and the schema says string: %s", name, v, b)
			}
		}
	}
	if seen["status"] != "removed" {
		t.Errorf("remove's status is %v, not removed: %s", seen["status"], b)
	}
	if _, has := seen["scan_id"]; has {
		t.Errorf("remove's result names one scan_id for a call that removed two: %s", b)
	}
	var ids []string
	list, _ := seen["removed"].([]any)
	for _, r := range list {
		m, _ := r.(map[string]any)
		id, _ := m["scan_id"].(string)
		ids = append(ids, id)
	}
	if !reflect.DeepEqual(ids, []string{"a", "c"}) {
		t.Errorf("removed names %v, not the two scans asked for in order: %s", ids, b)
	}
	if kept := files.ids(); !reflect.DeepEqual(kept, []string{"b"}) {
		t.Errorf("scans kept after removing a and c: %v", kept)
	}
}
