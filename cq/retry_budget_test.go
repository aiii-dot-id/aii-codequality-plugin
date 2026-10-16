// Copyright 2026 AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package cq

import (
	"fmt"
	"strings"
	"testing"
)

// Match the host judge: exhausting a step leaves the current file pending;
// successful answers remain in the cache for the next step.
type stepBudgetJudge struct {
	fakeJudge
	left int
}

func (j *stepBudgetJudge) Ask(state, questions map[string]any) (map[string]float64, error) {
	if j.left == 0 {
		return nil, ErrJudge
	}
	j.left--
	return j.fakeJudge.Ask(state, questions)
}

func (j *stepBudgetJudge) Choose(state, questions map[string]any) (map[string]Choice, error) {
	if j.left == 0 {
		return nil, ErrJudge
	}
	j.left--
	return j.fakeJudge.Choose(state, questions)
}

func TestLargePendingFileSurvivesSmallerRetryBudget(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	var source strings.Builder
	source.WriteString("package fixture\n")
	for i := 0; i < 2400; i++ {
		fmt.Fprintf(&source, "func Value%d() int { return %d + 1234567890 }\n", i, i)
	}
	if n := int64(source.Len()); n <= ChunkBytes || n > 131072 || EstimatedCalls(n) > 12 {
		t.Fatalf("fixture must fit the normal budget but need multiple chunks: %d bytes", n)
	}
	write(t, root, "large.go", source.String())
	fs := &OSFS{Root: root}
	cache := MemCache{}
	scan, err := NewScan(fs, "retry-budget", "fixture", "", nil, nil, false, 100, 131072)
	if err != nil {
		t.Fatal(err)
	}
	failed := &failJudge{}
	if records, err := scan.Step(tb, fs, failed, cache, 12, 400); err != nil || len(records) != 0 || scan.Status != "judge_unavailable" || len(scan.Pending) != 1 || failed.calls != 1 {
		t.Fatalf("initial failure: records=%d err=%v scan=%+v calls=%d", len(records), err, scan, failed.calls)
	}
	var records []Record
	for step := 0; step < 4; step++ {
		judge := &stepBudgetJudge{fakeJudge: fakeJudge{p: 0.9, conf: 0.9}, left: 3}
		got, err := scan.Step(tb, fs, judge, cache, 3, 400)
		if err != nil {
			t.Fatal(err)
		}
		if judge.calls+judge.chosen > 3 {
			t.Fatal("retry exceeded its call budget")
		}
		if scan.Excluded["oversize"] != 0 {
			t.Fatal("retry budget discarded the pending file as oversize")
		}
		records = append(records, got...)
		if step == 0 && (scan.Status != "judge_unavailable" || len(scan.Pending) != 1 || scan.FilesSeen != 0 || len(cache) == 0) {
			t.Fatalf("partial retry must keep the file and cached progress: %+v cache=%d", scan, len(cache))
		}
		if scan.Status == "done" {
			break
		}
	}
	if scan.Status != "done" || len(scan.Pending) != 0 || scan.FilesJudged != 1 || scan.FilesSeen != 1 || len(records) < 2 || scan.CacheHits == 0 || scan.LastError != "" {
		t.Fatalf("retry did not finish exactly one file using its cache: records=%d scan=%+v", len(records), scan)
	}
}

func TestRetryBudgetStillHonorsFileSizeLimit(t *testing.T) {
	tb := table(t)
	root := t.TempDir()
	write(t, root, "large.go", "package fixture\n"+strings.Repeat("var value = 1234567890\n", 6000))
	fs := &OSFS{Root: root}
	scan, err := NewScan(fs, "size-limit", "fixture", "", nil, nil, false, 100, 131072)
	if err != nil {
		t.Fatal(err)
	}
	judge := &failJudge{}
	if records, err := scan.Step(tb, fs, judge, MemCache{}, 3, 400); err != nil || len(records) != 0 || scan.Status != "done" || scan.Excluded["oversize"] != 1 || judge.calls != 0 {
		t.Fatalf("file size limit changed: records=%d err=%v scan=%+v calls=%d", len(records), err, scan, judge.calls)
	}
}
