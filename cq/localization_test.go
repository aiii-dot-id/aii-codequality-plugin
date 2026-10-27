package cq

import (
	"fmt"
	"strings"
	"testing"
)

type decliningLocation struct {
	fakeJudge
	selected string
	options  int
	escaped  bool
}

func (j *decliningLocation) Choose(state, qs map[string]any) (map[string]Choice, error) {
	j.chosen++
	out := map[string]Choice{}
	for id, q := range qs {
		opts := q.(map[string]any)["criteria"].(map[string]any)
		j.options = len(opts)
		_, j.escaped = opts["cannot_localize"]
		out[id] = Choice{Choice: j.selected, Confidence: 1, Probabilities: map[string]float64{j.selected: 1}}
	}
	return out, nil
}
func locationFixture(n int) Chunk {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "func Unit%d() {\n    work()\n}\n\n", i)
	}
	return Chunk{Text: b.String(), FirstLine: 1, LastLine: 4 * n}
}
func TestLocalizationMayDecline(t *testing.T) {
	for _, selected := range []string{"cannot_localize", "unknown-key"} {
		j := &decliningLocation{selected: selected}
		fs := []Finding{{ID: "EH-01", P: .99, Severity: "high"}}
		calls, _, _, _, err := locate(j, nil, "go", "", locationFixture(2), nil, fs)
		if err != nil || calls != 1 || !j.escaped || j.options != 3 {
			t.Fatalf("missing escape: calls=%d options=%d err=%v", calls, j.options, err)
		}
		if fs[0].Where != "" || fs[0].LocationAnswer == nil || fs[0].LocationReason == "" || fs[0].ID != "EH-01" {
			t.Fatalf("invented location or lost finding: %+v", fs[0])
		}
	}
	for _, n := range []int{254, 255} {
		j := &decliningLocation{selected: "cannot_localize"}
		fs := []Finding{{ID: "EH-01", P: .99, Severity: "high"}}
		calls, _, _, _, err := locate(j, nil, "go", "", locationFixture(n), nil, fs)
		if err != nil {
			t.Fatal(err)
		}
		if n == 254 && (calls != 1 || j.options != 255) {
			t.Fatalf("254+escape rejected: %d/%d", calls, j.options)
		}
		if n == 255 && (calls != 0 || fs[0].LocationReason != "location_option_limit") {
			t.Fatalf("over-limit candidates silently changed: %d %+v", calls, fs)
		}
	}
}
func TestChoiceDistributionPreserved(t *testing.T) {
	got, err := ParseChoices([]byte(`{"answers":{"EH-01":{"choice":"cannot_localize","confidence":0.75,"probabilities":{"cannot_localize":0.9,"unit":0.1}}}}`))
	if err != nil || len(got["EH-01"].Probabilities) != 2 || got["EH-01"].Probabilities["unit"] != .1 {
		t.Fatalf("distribution lost: %+v %v", got, err)
	}
	if _, err := ParseChoices([]byte(`{"answers":{"EH-01":{"choice":"unit","confidence":1,"probabilities":{"unit":2}}}}`)); err == nil {
		t.Fatal("invalid distribution accepted")
	}
}
