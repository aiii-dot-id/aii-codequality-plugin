package cq

import (
	"bytes"
	"encoding/json"
	"path"
)

const FolderTooWide = "listing_too_wide"

const FrontierBytes = 122 << 10

const ListingTruncated = "dir_listing_truncated"

const (
	TooWideListed = 16
	TooWideBytes  = 4 << 10
)

func (s *Scan) frontierFits(dir string, subdirs []string, pending []Entry) bool {
	room, _ := s.rooms()
	return frontierFits(room, dir, s.Stack[:len(s.Stack)-1], subdirs, pending)
}

func frontierFits(room int, dir string, stack, subdirs []string, pending []Entry) bool {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	n := 0
	fits := func(v any) bool {
		buf.Reset()
		if err := enc.Encode(v); err != nil {
			return false
		}
		n += buf.Len()
		return n <= room
	}
	if !fits(dir) {
		return false
	}
	for _, d := range stack {
		if !fits(d) {
			return false
		}
	}
	for _, d := range subdirs {
		if !fits(d) {
			return false
		}
	}
	for i := range pending {
		if !fits(&pending[i]) {
			return false
		}
	}
	return true
}

func (s *Scan) tooWide(dir string) error {
	ref := path.Clean(s.rel(dir))
	if err := s.boundary(ref, FolderTooWide); err != nil {
		return err
	}
	if s.Excluded == nil {
		s.Excluded = map[string]int{}
	}
	s.Excluded[FolderTooWide]++
	s.keepFolder(&s.TooWide, ref)
	s.commitProgress()
	return nil
}

func (s *Scan) keepFolder(list *[]string, ref string) {
	if len(s.TooWide)+len(s.Truncated) >= TooWideListed {
		return
	}
	b, err := json.Marshal(append(append(s.TooWide[:len(s.TooWide):len(s.TooWide)], s.Truncated...), ref))
	if err != nil || len(b) > TooWideBytes {
		return
	}
	*list = append(*list, ref)
}

func (s *Scan) listingTruncated(ref string) {
	for _, r := range s.Truncated {
		if r == ref {
			return
		}
	}
	if s.Excluded == nil {
		s.Excluded = map[string]int{}
	}
	s.Excluded[ListingTruncated]++
	s.keepFolder(&s.Truncated, ref)
}
