package cq

import "testing"

func TestReviewSupersedesPendingFileCoverage(t *testing.T) {
	pending := Coverage{Ref: "./a.go", Kind: "file", Disposition: "pending", Reasons: []string{"step_budget"}}
	finished := Coverage{Ref: "a.go", SourceDigest: "digest-a", Kind: "file", Disposition: "unknown", Reasons: []string{"context_limited"}}
	other := Coverage{Ref: "b.go", Kind: "file", Disposition: "pending", Reasons: []string{"step_budget"}}
	for _, complete := range []bool{false, true} {
		report := BuildReview(nil, []Coverage{pending, other, finished}, complete, complete, "")
		if len(report.Gaps) != 2 {
			t.Fatalf("stale pending coverage retained: %+v", report.Gaps)
		}
		for _, c := range report.Gaps {
			if c.Ref == "a.go" && (c.SourceDigest != "digest-a" || contains(c.Reasons, "step_budget")) {
				t.Fatalf("wrong current coverage: %+v", c)
			}
		}
		if report.AssessmentComplete {
			t.Fatal("context-limited/pending evidence claimed complete")
		}
	}

	report := BuildReview(nil, []Coverage{pending}, false, false, "")
	if len(report.Gaps) != 1 || !contains(report.Gaps[0].Reasons, "step_budget") {
		t.Fatalf("prefix lost pause: %+v", report)
	}

	report = BuildReview(nil, []Coverage{finished, pending}, false, true, "")
	if len(report.Gaps) != 2 {
		t.Fatalf("later pending evidence lost: %+v", report.Gaps)
	}
}

func TestReviewSupersessionPreservesIndependentEvidence(t *testing.T) {
	cs := []Coverage{
		{Ref: "a.go", Kind: "file", Disposition: "unknown", Reasons: []string{"read_error"}},
		{Ref: "a.go", SourceDigest: "old", Kind: "file", Disposition: "unknown", Reasons: []string{"source_changed"}},
		{Ref: "a.go", SourceDigest: "new", Kind: "file", Disposition: "unknown", Reasons: []string{"context_limited"}},
		{Ref: "sub", Kind: "subtree", Disposition: "unknown", Reasons: []string{"step_budget", "children_not_enumerated"}},
		{Ref: "sub/child.go", SourceDigest: "child", Kind: "file", Disposition: "unknown", Reasons: []string{"context_limited"}},
	}
	report := BuildReview(nil, cs, true, true, "")
	if len(report.Gaps) != 5 {
		t.Fatalf("independent digest/ref/boundary evidence lost: %+v", report.Gaps)
	}
}
