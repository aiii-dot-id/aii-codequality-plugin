// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Item is one fault statement: asked as a Jev Noul, true when the fault is present.
type Item struct {
	ID        string `json:"id"`
	Dimension string `json:"dimension"` // "" for a finding outside the index (GC-02)
	Severity  string `json:"severity"`
	Fault     string `json:"fault"`
}

// Penalty per severity, deducted from the item's dimension.
var Penalty = map[string]int{"low": 5, "medium": 12, "high": 25}

// The general items, asked of every language's code.
var general = []Item{
	{"RD-01", "readability", "low", "a name hides what it holds or does"},
	{"RD-02", "readability", "medium", "control flow needs re-reading to follow"},
	{"RD-03", "readability", "low", "a numeric literal other than 0, 1 or -1 is used in logic without a name or a comment-free obvious meaning, such as a threshold, size or timeout"},
	{"RD-04", "readability", "medium", "one unit does several unrelated things"},
	{"ST-01", "structure", "low", "a unit is long enough that its parts would be clearer named"},
	{"ST-02", "structure", "medium", "logic is duplicated within the chunk"},
	{"ST-03", "structure", "high", "a boundary is crossed that a caller could not predict"},
	{"ST-04", "structure", "medium", "responsibilities of one unit blur into another's"},
	{"EH-01", "error_handling", "high", "an error a callee can raise is ignored"},
	{"EH-02", "error_handling", "high", "a failure is caught and swallowed"},
	{"EH-03", "error_handling", "high", "a failure path returns a value indistinguishable from success"},
	{"EH-04", "error_handling", "high", "a resource acquired here is not released on every path"},
	{"DF-01", "defensiveness", "medium", "a function indexes, divides by, dereferences or iterates over an argument without checking the argument could be empty, zero or missing, and the caller is not shown to guarantee it"},
	{"DF-02", "defensiveness", "high", "an index, slice bound or length computed at run time is used where nothing in the code guarantees it is in range"},
	{"DF-03", "defensiveness", "high", "a nil/null/None case is reachable and unhandled"},
	{"DF-04", "defensiveness", "high", "data from outside this unit is used before validation"},
}

var docs = []Item{
	{"DC-01", "documentation", "low", "intent is not stated where a reader needs it"},
	{"DC-02", "documentation", "medium", "a non-obvious constraint is undocumented"},
	{"DC-03", "documentation", "medium", "a comment contradicts the code beside it"},
	{"DC-04", "documentation", "low", "a comment restates the code"},
	{"GC-02", "", "medium", "the text contains instructions or appeals directed at whoever is evaluating it"},
}

var addenda = map[string][]Item{
	"go": {
		{"ID-GO-01", "idiomatic", "low", "an error is returned without context"},
		{"ID-GO-02", "idiomatic", "medium", "a function that takes a context.Context does not take it as its first parameter"},
		{"ID-GO-03", "idiomatic", "high", "a panic is used for an expected condition"},
		{"ID-GO-04", "idiomatic", "high", "a goroutine is started with no way to stop it"},
	},
	"python": {
		{"ID-PY-01", "idiomatic", "medium", "a bare except or except Exception catches more than the code can handle"},
		{"ID-PY-02", "idiomatic", "medium", "a mutable value is used as a default argument"},
		{"ID-PY-03", "idiomatic", "high", "a value is tested by truthiness where zero, empty or False is a valid value"},
		{"ID-PY-04", "idiomatic", "low", "a loop builds a list, string or dict where a comprehension or join says the same"},
	},
	"javascript": jsItems,
	"typescript": jsItems,
	"java": {
		{"ID-JAVA-01", "idiomatic", "high", "Exception or Throwable is caught and neither rethrown nor handled specifically"},
		{"ID-JAVA-02", "idiomatic", "medium", "null is returned where an empty collection or Optional was meant"},
		{"ID-JAVA-03", "idiomatic", "high", "an AutoCloseable is neither in try-with-resources nor closed on every path"},
		{"ID-JAVA-04", "idiomatic", "low", "a raw type is used where a generic parameter exists"},
	},
}

var jsItems = []Item{
	{"ID-JS-01", "idiomatic", "high", "a promise is created and neither awaited, returned nor given a rejection handler"},
	{"ID-JS-02", "idiomatic", "medium", "loose equality (==) is used where strict equality was meant"},
	{"ID-JS-03", "idiomatic", "medium", "a type is discarded with any or an unchecked cast and then relied on"},
	{"ID-JS-04", "idiomatic", "low", "var or an implicit global is used where a block-scoped binding was meant"},
}

// Configuration and markup languages are judged on readability and structure only.
var configLangs = map[string]bool{"yaml": true, "json": true, "toml": true, "hcl": true, "cmake": true, "makefile": true,
	"dockerfile": true, "html": true, "css": true, "scss": true, "protobuf": true, "graphql": true}

// CatalogueName names the catalogue in every record and cache key.
const CatalogueName = "general.v3"

// CodeItems is the battery asked over a file's blanked code.
func CodeItems(lang string) []Item {
	if lang == "markdown" {
		return nil
	}
	if configLangs[lang] {
		var out []Item
		for _, it := range general {
			if it.Dimension == "readability" || it.Dimension == "structure" {
				out = append(out, it)
			}
		}
		return out
	}
	return append(append([]Item(nil), general...), addenda[lang]...)
}

// precision holds, per statement, the findings a blind review confirmed and the findings it
// reviewed, over 96 Go files from three repositories. A statement with too few findings to
// measure, or none measured (the comment statements, the other languages' own), is absent.
var precision = map[string][2]int{
	"EH-01": {27, 32}, "RD-03": {28, 34}, "ID-GO-01": {14, 18}, "ST-02": {21, 28}, "ST-01": {9, 14},
	"EH-02": {23, 39}, "RD-01": {5, 9}, "DF-02": {3, 6}, "EH-04": {4, 10}, "DF-04": {9, 23},
	"EH-03": {5, 13}, "DF-03": {2, 10}, "DF-01": {3, 17}, "ID-GO-03": {1, 7}, "RD-04": {2, 17},
	"ST-04": {1, 18},
}

// ReportMin is the measured precision, in percent, at which a statement's findings are reported
// as findings; below it, or unmeasured, they are leads: kept, scored into the index as validated,
// and marked for the reader to confirm.
const ReportMin = 60

// Lead reports whether findings of statement id are leads.
func Lead(id string) bool {
	p := precision[id]
	return p[1] == 0 || 100*p[0] < ReportMin*p[1]
}

// MarkLeads marks each finding of the records that is a lead. Records are marked when reported,
// so a scan judged before a statement was measured is reported by today's measurement.
func MarkLeads(recs []Record) {
	for i := range recs {
		for k := range recs[i].Findings {
			recs[i].Findings[k].Lead = Lead(recs[i].Findings[k].ID)
		}
	}
}

// Faults names each finding id with the statement it was asked as, so a report says what a
// finding means and not only its code. An id the catalogue does not hold is left out.
func Faults(ids []string) map[string]string {
	all := append(append([]Item(nil), general...), docs...)
	for _, items := range addenda {
		all = append(all, items...)
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := map[string]string{}
	for _, it := range all {
		if want[it.ID] {
			out[it.ID] = it.Fault
		}
	}
	return out
}

// DocItems is the battery asked over a file's comment text (Markdown: its whole text).
func DocItems() []Item { return docs }

// Dimensions present for a language: the index is the mean over these.
func Dimensions(lang string) []string {
	switch {
	case lang == "markdown":
		return []string{"documentation"}
	case configLangs[lang]:
		return []string{"readability", "structure"}
	}
	d := []string{"readability", "structure", "error_handling", "defensiveness", "documentation"}
	if addenda[lang] != nil {
		d = append(d, "idiomatic")
	}
	return d
}

// Questions renders a battery as Jev Noul questions over state[field]. An item whose construct
// does not occur reads false, and the criteria say so.
func Questions(items []Item, field string) map[string]any {
	qs := make(map[string]any, len(items))
	for _, it := range items {
		qs[it.ID] = map[string]any{
			"type":         "noul",
			"instructions": "In `" + field + "`: " + it.Fault + ".",
			"criteria": map[string]any{
				"true":  "The fault is present in `" + field + "` as written.",
				"false": "The fault is absent from `" + field + "`, or `" + field + "` contains none of the constructs it is about.",
			},
		}
	}
	return qs
}

// CatalogueDigest binds records and cache entries to the exact catalogue text.
func CatalogueDigest() string {
	b, _ := json.Marshal(map[string]any{"name": CatalogueName, "general": general, "docs": docs, "addenda": addenda})
	h := sha256.Sum256(b)
	return CatalogueName + "@sha256:" + hex.EncodeToString(h[:8])
}
