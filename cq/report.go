// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package cq

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// ParseRecords reads a results file: one JSON record per line.
func ParseRecords(b []byte) ([]Record, error) {
	var out []Record
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// FaultsOf is Faults for every finding the records carry.
func FaultsOf(recs []Record) map[string]string {
	var ids []string
	for _, r := range recs {
		for _, f := range r.Findings {
			ids = append(ids, f.ID)
		}
	}
	return Faults(ids)
}

// Summary aggregates a scan's records. Means are weighted by lines, so a large file counts for
// more than a small one; worst lists the lowest-index chunks.
func Summary(recs []Record, worst int) map[string]any {
	bands := map[string]int{}
	type agg struct{ lines, weighted, chunks int }
	byLang := map[string]*agg{}
	findings, leads := map[string]int{}, map[string]int{}
	all := agg{}
	var judged []Record
	for _, r := range recs {
		if r.Skipped != "" || r.Band == "" {
			continue
		}
		judged = append(judged, r)
		lines := r.Lines[1] - r.Lines[0] + 1
		bands[r.Band]++
		a := byLang[r.Language]
		if a == nil {
			a = &agg{}
			byLang[r.Language] = a
		}
		for _, x := range []*agg{a, &all} {
			x.lines += lines
			x.weighted += lines * r.Index
			x.chunks++
		}
		for _, f := range r.Findings {
			if Lead(f.ID) {
				leads[f.ID]++
			} else {
				findings[f.ID]++
			}
		}
	}
	mean := func(a *agg) any {
		if a.lines == 0 {
			return nil
		}
		return a.weighted / a.lines
	}
	langs := map[string]any{}
	for k, a := range byLang {
		langs[k] = map[string]any{"chunks": a.chunks, "lines": a.lines, "index": mean(a)}
	}
	sort.SliceStable(judged, func(i, j int) bool { return judged[i].Index < judged[j].Index })
	if len(judged) > worst {
		judged = judged[:worst]
	}
	w := make([]any, 0, len(judged))
	for _, r := range judged {
		fs, ls := []any{}, []any{}
		for _, f := range r.Findings {
			item := map[string]any{"id": f.ID}
			if f.Where != "" {
				item["where"] = f.Where
			}
			if Lead(f.ID) {
				ls = append(ls, item)
			} else {
				fs = append(fs, item)
			}
		}
		w = append(w, map[string]any{"ref": r.Ref, "chunk": r.Chunk, "index": r.Index, "band": r.Band, "findings": fs, "leads": ls})
	}
	ids := make([]string, 0, len(findings)+len(leads))
	for id := range findings {
		ids = append(ids, id)
	}
	for id := range leads {
		ids = append(ids, id)
	}
	return map[string]any{"chunks_judged": all.chunks, "lines": all.lines, "index": mean(&all), "bands": bands,
		"languages": langs, "finding_counts": findings, "lead_counts": leads, "faults": Faults(ids), "worst": w}
}

var severityRank = map[string]int{"low": 0, "medium": 1, "high": 2}

// Select returns the records under glob (see Match; "" or "." is every record) in language ("" is
// any), each keeping only its findings at minSeverity or above; a record left with none is dropped.
func Select(recs []Record, glob, minSeverity, language string) []Record {
	var out []Record
	for _, r := range recs {
		if language != "" && r.Language != language || !Match(glob, r.Ref) {
			continue
		}
		kept := r.Findings[:0:0]
		for _, f := range r.Findings {
			if severityRank[f.Severity] >= severityRank[minSeverity] {
				kept = append(kept, f)
			}
		}
		if len(kept) > 0 {
			r.Findings = kept
			out = append(out, r)
		}
	}
	return out
}

// Match reports whether a file path is under a glob: path.Match within each segment, "**" for
// any number of segments, and a glob that names a folder holds every file below it — so "**",
// "internal/**" and "internal" all reach internal/plan/plan.go, and "*.go" only the top level.
func Match(glob, ref string) bool {
	glob = path.Clean(glob)
	if glob == "." {
		return true
	}
	parts := strings.Split(ref, "/")
	at := make([]bool, len(parts)+1) // at[j]: the glob so far matches parts[:j]
	at[0] = true
	for _, g := range strings.Split(glob, "/") {
		next := make([]bool, len(parts)+1)
		for j, ok := range at {
			switch {
			case !ok:
			case g == "**":
				for k := j; k <= len(parts); k++ {
					next[k] = true
				}
			case j < len(parts):
				if m, _ := path.Match(g, parts[j]); m {
					next[j+1] = true
				}
			}
		}
		at = next
	}
	for _, ok := range at {
		if ok {
			return true
		}
	}
	return false
}

// Page returns the records from offset on until pageBytes of JSON is reached, and the offset to
// continue from (-1 when there is no more).
func Page(recs []Record, offset, pageBytes int) ([]Record, int) {
	var out []Record
	size := 0
	for i := max(offset, 0); i < len(recs); i++ {
		r := recs[i]
		MarkLeads([]Record{r})
		r.Answers = nil
		b, _ := json.Marshal(r)
		if size+len(b) > pageBytes && len(out) > 0 {
			return out, i
		}
		size += len(b)
		out = append(out, r)
	}
	return out, -1
}
