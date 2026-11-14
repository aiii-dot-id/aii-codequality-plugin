package cq

import "encoding/json"

const stateReserve = 52 << 10

const RuntimeGrowth = 3 << 10

func (s *Scan) metadataBytes() int {
	if s.metadata > 0 {
		return s.metadata
	}
	n := 0
	for _, v := range []any{s.ID, s.Root, s.Base, s.Ignore, s.Attrs, s.Exclude, s.Languages, s.Runtime} {
		b, err := json.Marshal(v)
		if err != nil {
			return CheckpointBytes
		}
		n += len(b)
	}
	s.metadata = n
	return n
}

func (s *Scan) rooms() (frontier, cursor int) {
	left := CheckpointBytes - stateReserve - RuntimeGrowth - s.metadataBytes()
	if left >= FrontierBytes+CursorBytes {
		return FrontierBytes, CursorBytes
	}
	if left < 0 {
		left = 0
	}
	frontier = int(int64(left) * FrontierBytes / (FrontierBytes + CursorBytes))
	return frontier, left - frontier
}

func (s *Scan) FrontierRoom() int {
	frontier, _ := s.rooms()
	return frontier
}

func (s *Scan) CursorRoom() int {
	_, cursor := s.rooms()
	return cursor
}
