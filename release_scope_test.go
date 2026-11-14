package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func TestNarrowReleaseFileCap(t *testing.T) {
	for _, n := range []int{1024, 1 << 20, 1 << 30} {
		if _, err := configFrom(values{"max_file_bytes": n}); err != nil {
			t.Fatalf("a retired max_file_bytes %d refused: %v", n, err)
		}
	}
}

func TestExperimentalScanIsReportOnlyWithoutMutation(t *testing.T) {
	cfg := mustConfig(t, values{})
	for _, cap := range []int64{0, -1, (192 << 10) + 1, 1 << 20} {
		s := &cq.Scan{Version: cq.RecordVersion, ID: "experimental", MaxFileBytes: cap, Status: "running", Chunks: 3, Calls: 7}
		before, _ := json.Marshal(s)
		_, err := stepScan(cfg, s, 1800000000000)
		if reasonOf(err) != "SCAN_RESTART_REQUIRED" {
			t.Fatalf("cap %d: %v", cap, err)
		}
		after, _ := json.Marshal(s)
		if string(before) != string(after) {
			t.Fatalf("refusal mutated retained scan: %s != %s", before, after)
		}
	}
}

func TestDirectJudgeByteCap(t *testing.T) {
	for _, set := range []int{0, 1024, 128 << 10} {
		cap := cq.SegmentBytes
		vals := values{"chunk_bytes": 131072}
		if set != 0 {
			vals["max_file_bytes"] = set
		}
		cfg := mustConfig(t, vals)
		for _, unit := range []string{"a", "界"} {
			exact := strings.Repeat(unit, cap/len(unit)) + strings.Repeat("a", cap%len(unit))
			for _, extra := range []string{"", "x"} {
				raw, _ := json.Marshal([]map[string]any{{"ref": "source.md", "text": exact + extra}})
				items, ok := sdk.ObjectArray(raw)
				if !ok {
					t.Fatal("invalid fixture")
				}
				budget, err := judgeBudget(cfg, items)
				if extra == "" {
					if err != nil || budget <= 0 {
						t.Fatalf("exact %d %q: %d %v", cap, unit, budget, err)
					}
				} else if reasonOf(err) != "OPERATION_ARGUMENT_INVALID" || !strings.Contains(err.Error(), "not a file skip") {
					t.Fatalf("cap+1 %d %q must fail byte guard, not call budget: %v", cap, unit, err)
				}
			}
		}
	}

	raw, _ := json.Marshal([]map[string]any{{"ref": "ok.md", "text": "ok"}, {"ref": "large.md", "text": strings.Repeat("界", 70000)}})
	items, _ := sdk.ObjectArray(raw)
	_, err := judgeBudget(mustConfig(t, values{"max_file_bytes": 192 << 10, "chunk_bytes": 131072}), items)
	if reasonOf(err) != "OPERATION_ARGUMENT_INVALID" || !strings.Contains(err.Error(), "large.md") {
		t.Fatalf("later oversized item: %v", err)
	}
}

func TestAdvanceSkipsReportOnlyWithoutMutation(t *testing.T) {
	old := &cq.Scan{ID: "old", Version: cq.RecordVersion, MaxFileBytes: 1 << 20, Status: "running", Chunks: 3}
	legacy := &cq.Scan{ID: "legacy", Version: 1, MaxFileBytes: 128 << 10, Status: "judge_unavailable"}
	current := &cq.Scan{ID: "current", Version: cq.RecordVersion, MaxFileBytes: 128 << 10, Status: "running"}
	scans := map[string]*cq.Scan{"old": old, "legacy": legacy, "current": current}
	before, _ := json.Marshal(scans)
	stepped := 0
	load := func(id string) (*cq.Scan, error) { return scans[id], nil }
	step := func(s *cq.Scan) (any, error) {
		if s != current {
			t.Fatalf("attempted report-only scan %s", s.ID)
		}
		stepped++
		return map[string]any{"status": "running"}, nil
	}
	for _, ids := range [][]string{{"old", "legacy", "current"}, {"old", "legacy"}} {
		result, err := advanceScans(ids, load, step)
		if err != nil {
			t.Fatal(err)
		}
		out := result.(map[string]any)
		want := ""
		if len(ids) == 3 {
			want = "current"
		}
		if out["advanced"] != want {
			t.Fatalf("selected %v, want %s", out, want)
		}
		skipped := out["report_only"].([]any)
		if len(skipped) != 2 {
			t.Fatalf("missing skipped scans: %v", out)
		}
		for i, id := range []string{"old", "legacy"} {
			item := skipped[i].(map[string]any)
			if item["scan_id"] != id || item["reason"] == "" {
				t.Fatalf("missing reason: %v", item)
			}
		}
	}
	after, _ := json.Marshal(scans)
	if stepped != 1 || string(before) != string(after) {
		t.Fatalf("steps %d, retained scans changed: %s", stepped, after)
	}

	sentinel := errors.New("read failure")
	result, err := advanceScans([]string{"old"}, func(string) (*cq.Scan, error) { return nil, sentinel }, step)
	out, _ := result.(map[string]any)
	if err != nil || out["advanced"] != "" || len(out["unreadable"].([]any)) != 1 ||
		out["unreadable"].([]any)[0].(map[string]any)["last_error"] != sentinel.Error() {
		t.Fatalf("swallowed read error: %v %v", out, err)
	}
}

func TestReportsSurviveInvalidAssessmentSettings(t *testing.T) {
	for _, shape := range []string{"list", "summary", "records", "review_queue", "coverage"} {
		t.Run(shape, func(t *testing.T) {
			vals := values{"chunk_bytes": 1}
			out, err := reportWithSettings(vals, func(cfg config) (any, error) {
				if cfg.maxCalls() != 0 {
					t.Fatal("invented progress configuration")
				}
				view := scanView(cfg, &cq.Scan{ID: "retained", Version: cq.RecordVersion, MaxFileBytes: 128 << 10, Status: "running", Chunks: 3})
				if _, ok := view["progress"]; ok {
					t.Fatal("settings that cannot step a scan say how it advances")
				}
				return map[string]any{"shape": shape, "scan": view, "retained": "evidence"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result := out.(map[string]any)
			if result["retained"] != "evidence" || !strings.Contains(result["assessment_configuration_error"].(string), "chunk_bytes") {
				t.Fatalf("lost evidence/warning: %v", result)
			}
			if vals["chunk_bytes"] != 1 {
				t.Fatal("settings mutated")
			}
		})
	}
	sentinel := errors.New("journal unreadable")
	_, err := reportWithSettings(values{"chunk_bytes": 1}, func(config) (any, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("lost report IO failure: %v", err)
	}
	out, err := reportWithSettings(values{}, func(config) (any, error) { return map[string]any{"ok": true}, nil })
	if err != nil || out.(map[string]any)["assessment_configuration_error"] != nil {
		t.Fatalf("valid settings: %v %v", out, err)
	}
}

func TestAnUnreadableScanIsListedNotDropped(t *testing.T) {
	good := &cq.Scan{ID: "good", Version: cq.RecordVersion, MaxFileBytes: 128 << 10, Status: "running"}
	load := func(id string) (*cq.Scan, error) {
		if id == "torn" {
			return nil, fmt.Errorf("load scan checkpoint: %w", cq.ErrCheckpointCorrupt)
		}
		return good, nil
	}
	listed := listScans(mustConfig(t, values{}), []string{"torn", "good"}, load)
	if len(listed) != 2 {
		t.Fatalf("unreadable scan dropped: %v", listed)
	}
	torn := listed[0].(map[string]any)
	if torn["scan_id"] != "torn" || torn["status"] != "unreadable" || !strings.Contains(torn["last_error"].(string), "no valid checkpoint") || torn["route"] != scanUnreadable {
		t.Fatalf("unreadable scan not described: %v", torn)
	}
	result, err := advanceScans([]string{"torn", "good"}, load, func(s *cq.Scan) (any, error) { return map[string]any{}, nil })
	if err != nil {
		t.Fatalf("one unreadable scan stopped the schedule: %v", err)
	}
	out := result.(map[string]any)
	if out["advanced"] != "good" || len(out["unreadable"].([]any)) != 1 {
		t.Fatalf("advance: %v", out)
	}
}

func TestAMissingHostClockIsATypedRefusal(t *testing.T) {
	if _, err := hostNow(sdk.Call{}); reasonOf(err) != "HOST_CLOCK_UNAVAILABLE" {
		t.Fatalf("missing clock: %v", err)
	}
	cfg := mustConfig(t, values{"backend": "local", "local_judge_url": "https://fixture.invalid", "model": "fixed"})
	j, err := newJudge(cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.bindInvocation(cfg, 0); reasonOf(err) != "HOST_CLOCK_UNAVAILABLE" {
		t.Fatalf("zero clock: %v", err)
	}
}

func TestDirectJudgeStatesItsCallLimit(t *testing.T) {
	cfg := mustConfig(t, values{"backend": "local", "local_judge_url": "https://fixture.invalid", "model": "fixed"})
	largest := cfg.maxCalls()/3*cfg.chunkBytes() - 1
	at := func(n int) []sdk.Object {
		raw, _ := json.Marshal([]any{map[string]any{"ref": "a.go", "text": strings.Repeat("x", n)}})
		items, _ := sdk.ObjectArray(raw)
		return items
	}
	if _, err := judgeBudget(cfg, at(largest)); err != nil {
		t.Fatalf("the stated largest text refused: %v", err)
	}
	_, err := judgeBudget(cfg, at(largest+1))
	if reasonOf(err) != "OPERATION_ARGUMENT_INVALID" || !strings.Contains(err.Error(), fmt.Sprintf("at most %d bytes", largest)) {
		t.Fatalf("limit not stated: %v", err)
	}
}

func TestTheReportTakesEveryLaneItsSchemaOffers(t *testing.T) {
	raw, err := os.ReadFile("schemas/report_in.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Lane struct {
				Enum []string `json:"enum"`
			} `json:"lane"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if strings.Join(schema.Properties.Lane.Enum, ",") != strings.Join(cq.ReviewLanes, ",") {
		t.Fatalf("schema lanes %v, the report's %v", schema.Properties.Lane.Enum, cq.ReviewLanes)
	}
	for _, lane := range append([]string{""}, cq.ReviewLanes...) {
		if !reviewLane(lane) {
			t.Errorf("the entry point refuses lane %q", lane)
		}
	}
	if reviewLane("everything") {
		t.Error("the entry point takes a lane the schema does not offer")
	}
}

func TestTheEstimateCoversTheFilesJudged(t *testing.T) {
	s := &cq.Scan{Version: cq.RecordVersion, Status: "done", FilesSeen: 49, FilesJudged: 2, Chunks: 8, Calls: 26, CacheHits: 2, SlopCalls: 8}
	c := scanView(mustConfig(t, values{}), s)["calls_per_judged_file"].(map[string]any)
	num := func(v any) float64 { f, _ := strconv.ParseFloat(fmt.Sprint(v), 64); return f }
	if est, actual := num(c["estimated_before_caching"]), num(c["with_cache_hits"]); est < actual {
		t.Fatalf("estimated %v a file before caching; the files cost %v", est, actual)
	}
	if strings.Contains(callsBeforeCaching, "per judged file") {
		t.Fatalf("the start leads with a one-window file's cost: %q", callsBeforeCaching)
	}
}

func TestAnArgumentAShapeDoesNotUseIsNamed(t *testing.T) {
	for args, want := range map[string]string{
		`{"scan_id":"s","shape":"summary","lane":"slop","worst":5}`:            "lane",
		`{"scan_id":"s","lane":"size"}`:                                        "lane",
		`{"scan_id":"s","shape":"review_queue","lane":"slop","worst":3}`:       "worst",
		`{"scan_id":"s","shape":"coverage","lane":"slop","path":"a"}`:          "lane",
		`{"scan_id":"s","shape":"findings","tier":"all","min_severity":"low"}`: "tier",
		`{"scan_id":"s","shape":"triage","lane":"slop","limit":4}`:             "",
		`{"shape":"summary","lane":"slop"}`:                                    "",
	} {
		got := ""
		for _, k := range ignoredArguments(sdk.Object(args)) {
			got += k.(string)
		}
		if got != want {
			t.Errorf("%s: ignored %q, want %q", args, got, want)
		}
	}
}

func TestARefusalBeforeTheSendIsMarkedNotSent(t *testing.T) {
	sent := 0
	send := func(string, *sdk.HTTPOptions) (sdk.HTTPResult, error) {
		sent++
		return sdk.HTTPResult{Status: 503}, nil
	}
	spent := &hostJudge{at: judgeAt{systemOne: "x"}, configured: "m", model: "m", left: 0, send: send}
	unbound := &hostJudge{at: judgeAt{systemOne: "x"}, configured: "m", model: "m", left: 1, send: send}
	for name, j := range map[string]*hostJudge{"spent": spent, "unbound": unbound} {
		if _, err := j.Ask(map[string]any{"code": "x"}, map[string]any{}); !errors.Is(err, cq.ErrNotSent) || sent != 0 {
			t.Errorf("%s: %v (sent %d)", name, err, sent)
		}
	}
}

func TestAListOnUnreadSettingsSaysNothingExpires(t *testing.T) {
	files := []sdk.FileEntry{{Name: "s1.state.json", Size: 10, Modified: "2026-11-01T00:00:00Z"}, {Name: "s1.v2.jsonl", Size: 10, Modified: "2026-11-01T00:00:00Z"}}
	load := func(id string) (*cq.Scan, error) {
		return &cq.Scan{ID: id, Version: cq.RecordVersion, Status: "done"}, nil
	}
	out := scanList(config{}, files, load)
	view := out["scans"].([]any)[0].(map[string]any)
	at, _ := view["expires_at"].(string)
	if _, ok := out["scan_retention_days"]; ok || strings.HasPrefix(at, "2026") || !strings.Contains(at, "settings") {
		t.Fatalf("retention %v, expires_at %q", out["scan_retention_days"], at)
	}
}
