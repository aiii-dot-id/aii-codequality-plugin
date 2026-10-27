package cq

import "testing"

func TestOversizeIsUnknownNotIntentionalExclusion(t *testing.T) {
	s := &Scan{MaxFileBytes: 10, Excluded: map[string]int{}}
	_, _, err := s.file(tableForCoverage(t), nil, nil, nil, Entry{Name: "large.go", Size: 11}, "large.go", nil, nil, nil)
	if err != nil || len(s.Observed) != 1 || s.Observed[0].Disposition != "unknown" || s.Observed[0].Reasons[0] != "oversize" {
		t.Fatalf("%+v %v", s.Observed, err)
	}
}
func tableForCoverage(t *testing.T) *Table {
	t.Helper()
	v, e := LoadTable()
	if e != nil {
		t.Fatal(e)
	}
	return v
}
