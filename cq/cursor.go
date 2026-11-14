package cq

import (
	"bytes"
	"encoding/json"
)

const PlanTooLarge = "plan_too_large"

const CursorBytes = 72 << 10

const cursorGrowth = 256

const (
	PlanTooLargeListed = 8
	PlanTooLargeBytes  = 2 << 10
)

func (s *Scan) cursorFits(c *FileCursor, lang string, langs []string) bool {
	_, room := s.rooms()
	return cursorFits(room, c, lang, langs)
}

func cursorFits(room int, c *FileCursor, lang string, langs []string) bool {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	n := cursorGrowth
	fits := func(v any) bool {
		buf.Reset()
		if err := enc.Encode(v); err != nil {
			return false
		}
		n += buf.Len()
		return n <= room
	}
	rest := *c
	rest.Lang, rest.Langs = lang, langs
	rest.Outline, rest.Units = nil, nil
	if !fits(&rest) {
		return false
	}
	for i := range c.Outline {
		if !fits(&c.Outline[i]) {
			return false
		}
	}
	for i := range c.Units {
		if !fits(&c.Units[i]) {
			return false
		}
	}
	return true
}

func (s *Scan) namePlanTooLarge(rel string) {
	if len(s.PlanTooLarge) >= PlanTooLargeListed {
		return
	}
	if b, err := json.Marshal(append(s.PlanTooLarge[:len(s.PlanTooLarge):len(s.PlanTooLarge)], rel)); err == nil && len(b) <= PlanTooLargeBytes {
		s.PlanTooLarge = append(s.PlanTooLarge, rel)
	}
}
