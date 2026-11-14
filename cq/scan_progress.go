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

var chosenSkips = map[string]bool{"global_dir": true, "language_dir": true, "gitignore": true, "caller_exclude": true,
	"nested_repository": true, "symlink": true, IgnoredDir: true}

func (s *Scan) boundary(ref, reason string) error {
	disposition := "unknown"
	if chosenSkips[reason] {
		disposition = "excluded"
	}
	return s.cover(Coverage{Ref: ref, Kind: "subtree", Disposition: disposition, Reasons: []string{reason, "children_not_enumerated"}})
}

func recordWindows(rs []Record) int {
	seen := map[int]bool{}
	for _, r := range rs {
		seen[r.Chunk] = true
	}
	return len(seen)
}
