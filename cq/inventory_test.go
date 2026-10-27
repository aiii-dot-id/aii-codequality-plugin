package cq

import "testing"

func TestInventoryCompletionCannotHideUnknownSubtrees(t *testing.T) {
	c := []Coverage{{Ref: "nested", Kind: "subtree", Disposition: "unknown", Reasons: []string{"children_not_enumerated"}}}
	r := BuildReview(nil, c, true, true, "")
	if r.InventoryComplete || r.AssessmentComplete || len(r.Gaps) != 1 {
		t.Fatalf("unknown subtree lost: %+v", r)
	}
	r = BuildReview(nil, nil, true, false, "")
	if r.InventoryComplete {
		t.Fatal("unread journal suffix treated as complete inventory")
	}
}
