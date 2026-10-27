package cq

type ScanProgress interface {
	Load(ref string) ([]Record, error)
	Save(Record) error
	Cover(Coverage) error
	End() int64
}

func (s *Scan) cover(c Coverage) error {
	if s.Progress != nil {
		return s.Progress.Cover(c)
	}
	s.Observed = append(s.Observed, c)
	return nil
}
func (s *Scan) boundary(ref, reason string) error {
	return s.cover(Coverage{Ref: ref, Kind: "subtree", Disposition: "unknown", Reasons: []string{reason, "children_not_enumerated"}})
}

func recordWindows(rs []Record) int {
	seen := map[int]bool{}
	for _, r := range rs {
		seen[r.Chunk] = true
	}
	return len(seen)
}
