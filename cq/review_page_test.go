package cq

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestQueuePolicyAndCursor(t *testing.T) {
	a, b := reviewFixture("a.go", "EH-01"), reviewFixture("b.go", "EH-02")
	report := BuildReview([]Record{b, a}, nil, false, true, "")
	generation := ReviewGeneration([]byte("checkpoint-1"), []byte("records-1"))
	first, err := PageReview(report, generation, "review_queue", "", "", 4096, 1)
	if err != nil {
		t.Fatal(err)
	}
	var entries []FileReview
	if err = json.Unmarshal(first.Entries, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Ref != "a.go" || first.Next == "" || first.GapCount != 2 {
		t.Fatalf("first page: %+v", first)
	}
	second, err := PageReview(report, generation, "review_queue", "", first.Next, 4096, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(second.Entries, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Ref != "b.go" || second.Next != "" {
		t.Fatalf("second page: %+v", second)
	}
	for _, tc := range []struct{ generation, shape, lane, cursor string }{
		{ReviewGeneration([]byte("checkpoint-2"), []byte("records-1")), "review_queue", "", first.Next},
		{generation, "coverage", "", first.Next}, {generation, "review_queue", "correctness", first.Next}, {generation, "review_queue", "", "garbage"},
	} {
		if _, err := PageReview(report, tc.generation, tc.shape, tc.lane, tc.cursor, 4096, 1); !errors.Is(err, ErrStaleCursor) {
			t.Fatalf("accepted stale/query cursor: %v", err)
		}
	}
	if _, err := PageReview(report, generation, "coverage", "", "", 4096, 100); err != nil {
		t.Fatal(err)
	}
}

func TestReviewEntryCannotSilentlyExceedPage(t *testing.T) {
	r := reviewFixture("a.go", "EH-01")
	r.Findings[0].Where = strings.Repeat("x", 4096)
	report := BuildReview([]Record{r}, nil, false, true, "")
	if _, err := PageReview(report, "generation", "review_queue", "", "", 4096, 1); !errors.Is(err, ErrReviewEntryTooLarge) {
		t.Fatalf("oversized result: %v", err)
	}
}

func TestCoverageCannotInventMissingRecord(t *testing.T) {
	r := reviewFixture("a.go", "EH-01")
	c := completeFixture(r)
	c.ExpectedChunks = 2
	c.CompletedChunks = 2
	report := BuildReview([]Record{r}, []Coverage{c}, true, true, "")
	if report.AssessmentComplete || report.Files[0].Status == "assessed" {
		t.Fatal("checkpoint counters substituted for missing evidence")
	}
}
