// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package cq

import (
	"fmt"
	"strings"
)

// MaxUnits bounds the units a chunk is split into: they are the options of one Choice.
const MaxUnits = 48

// Unit is one top-level piece of a chunk — a function, a type, a declaration block — named by
// its line range and first line, so a location answers with lines.
type Unit struct {
	Key         string
	First, Last int
	Text        string
}

// Units splits a chunk of blanked code into top-level units by indentation, for any language: a
// unit starts at each line at the chunk's shallowest indentation that is not a closing bracket,
// and runs to the line before the next. When one unit holds most of the chunk (a class around
// its methods), its body is split one level deeper instead. Short runs of one-line units are
// merged, and the count is capped at MaxUnits by merging neighbours. firstLine is the chunk's
// first line in the file.
func Units(text string, firstLine int) []Unit {
	lines := strings.Split(text, "\n")
	spans := split(lines, 0, len(lines))
	if len(spans) == 1 {
		s := spans[0]
		if inner := split(lines, s[0]+1, s[1]); len(inner) > 1 {
			spans = append([][2]int{{s[0], inner[0][0]}}, inner...)
			spans[len(spans)-1][1] = s[1]
		}
	}
	spans = mergeShort(lines, spans)
	for len(spans) > MaxUnits {
		merged := make([][2]int, 0, (len(spans)+1)/2)
		for i := 0; i < len(spans); i += 2 {
			end := spans[i][1]
			if i+1 < len(spans) {
				end = spans[i+1][1]
			}
			merged = append(merged, [2]int{spans[i][0], end})
		}
		spans = merged
	}
	out := make([]Unit, 0, len(spans))
	for _, s := range spans {
		head := ""
		for _, l := range lines[s[0]:s[1]] {
			if t := strings.TrimSpace(l); t != "" {
				head = t
				break
			}
		}
		if len(head) > 72 {
			head = head[:72]
		}
		first, last := firstLine+s[0], firstLine+lastNonBlank(lines, s[0], s[1])
		out = append(out, Unit{Key: fmt.Sprintf("lines %d-%d: %s", first, last, head), First: first, Last: last,
			Text: strings.Join(lines[s[0]:s[1]], "\n")})
	}
	return out
}

// split returns [start, end) spans of lines[from:to] beginning at each line at the shallowest
// indentation there that opens something rather than closing it. Lines before the first such
// line join the first span.
func split(lines []string, from, to int) [][2]int {
	min := -1
	for _, l := range lines[from:to] {
		if d, ok := depth(l); ok && !closer(l) && (min < 0 || d < min) {
			min = d
		}
	}
	var starts []int
	for i := from; i < to; i++ {
		if d, ok := depth(lines[i]); ok && d == min && !closer(lines[i]) {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return [][2]int{{from, to}}
	}
	starts[0] = from
	spans := make([][2]int, len(starts))
	for k, s := range starts {
		end := to
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		spans[k] = [2]int{s, end}
	}
	return spans
}

// mergeShort folds a run of one-line units (imports, one-line declarations) into one unit.
func mergeShort(lines []string, spans [][2]int) [][2]int {
	var out [][2]int
	run := false // the last unit is a run of one-line units
	for _, s := range spans {
		one := codeLines(lines, s) == 1
		if n := len(out); n > 0 && one && run {
			out[n-1][1] = s[1]
			continue
		}
		out = append(out, s)
		run = one
	}
	return out
}

func codeLines(lines []string, s [2]int) int {
	n := 0
	for _, l := range lines[s[0]:s[1]] {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func lastNonBlank(lines []string, from, to int) int {
	for i := to - 1; i > from; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return from
}

// depth is a line's leading whitespace, a tab counting as one; false for a blank line.
func depth(l string) (int, bool) {
	t := strings.TrimLeft(l, " \t")
	if t == "" {
		return 0, false
	}
	return len(l) - len(t), true
}

func closer(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "}") || strings.HasPrefix(t, ")") || strings.HasPrefix(t, "]") || t == "end"
}
