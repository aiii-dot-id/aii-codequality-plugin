package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	SlopBattery    = "slop"
	OutlineBattery = "outline"
	slopName       = "slop-v1"
	outlineName    = "outline-v1"
)

var slopItems = []Item{
	{"SL-NARRATE", "slop", "low", "a comment narrates the edit history (previously, changed to, now we, or a date or ticket as its only content) instead of saying why"},
	{"SL-IMPOSSIBLE", "slop", "low", "a check or branch defends against a case the surrounding code makes impossible"},
	{"SL-INDIRECT", "slop", "low", "a wrapper, helper or type here adds a layer and no behaviour"},
	{"SL-MIXED", "slop", "low", "the same job is done two or more different ways here"},
}

var outlineItems = []Item{
	{"SL-FILEJOBS", "slop", "low", "this file holds several unrelated jobs"},
	{"SL-FILEWAYS", "slop", "low", "by its outline, this file does one kind of thing through several parallel sets of functions"},
}

func SlopItems() []Item { return append([]Item(nil), slopItems...) }

func batteryDigest(name string, items []Item) string {
	b, _ := json.Marshal(map[string]any{"name": name, "items": items})
	h := sha256.Sum256(b)
	return name + "@sha256:" + hex.EncodeToString(h[:8])
}

func SlopDigest() string    { return batteryDigest(slopName, slopItems) }
func OutlineDigest() string { return batteryDigest(outlineName, outlineItems) }

func askBattery(j Judge, c Cache, backend, call, digest string, items []Item, field string, state map[string]any, out Record, lines string) (Record, error) {
	questions := Questions(items, field)
	for _, q := range questions {
		fields := q.(map[string]any)
		fields["instructions"] = lines + fields["instructions"].(string)
	}
	request, err := json.Marshal([]any{state, questions, PreprocessingVersion, digest})
	if err != nil {
		return out, err
	}
	key := CacheKey(string(request), out.Language, KeyModel(backend, j.Model()), call)
	var answers map[string]float64
	hit := false
	if gate, ok := j.(interface{ CacheEligible() bool }); !ok || gate.CacheEligible() {
		if hit, err = cached(c, key, &answers); err != nil {
			return out, err
		}
	}
	if !hit {
		answers, err = j.Ask(state, questions)
		out.Calls = sentCalls(err)
		if err != nil {
			return out, err
		}
	} else {
		out.Cached = 1
	}
	for _, it := range items {
		if p, ok := answers[it.ID]; ok && validProbability(p) {
			out.Answers[it.ID] = p
		}
	}
	if !batteryAnswered(out) {
		return out, errIntentPartial
	}
	if !hit && keep(c, key, out.Answers) != nil {
		out.Uncached++
	}
	return out, nil
}

func askSlop(j Judge, c Cache, backend string, r Record, ctx Chunk) (Record, error) {
	out := Record{Version: RecordVersion, SourceDigest: r.SourceDigest, Preprocessing: PreprocessingVersion, ThresholdPolicy: ThresholdPolicy,
		Ref: r.Ref, Chunk: r.Chunk, Lines: [2]int{ctx.FirstLine, ctx.LastLine}, Language: r.Language, Answers: map[string]float64{},
		Catalogue: SlopDigest(), Backend: backend, Model: j.Model(), Battery: SlopBattery, Boundary: r.Boundary, WindowCount: r.WindowCount,
		ID: slopID(r.ID)}
	for _, it := range slopItems {
		out.Expected = append(out.Expected, it.ID)
	}
	state := map[string]any{"language": r.Language, "source": ctx.Text}
	return askBattery(j, c, backend, SlopBattery, SlopDigest(), slopItems, "source", state, out, fmt.Sprintf(questionLines, ctx.FirstLine, ctx.LastLine, ""))
}

func slopID(codeID string) string {
	sum := sha256.Sum256([]byte(codeID + "\x00" + SlopDigest()))
	return hex.EncodeToString(sum[:])
}

func OutlineText(items []OutlineItem) string {
	var b strings.Builder
	for _, o := range items {
		sig := o.Sig
		if sig == "" {
			sig = o.Name
		}
		fmt.Fprintf(&b, "%s %s (%d lines)\n", o.Kind, sig, o.Lines)
	}
	return b.String()
}

func OutlineAsked(s *FileShape) bool {
	if s == nil {
		return false
	}
	n := 0
	for _, o := range s.Outline {
		if o.Kind == "func" || o.Kind == "method" {
			n++
		}
	}
	return n > 1
}

func AskOutline(j Judge, c Cache, ref, digest, lang string, s *FileShape) (Record, error) {
	backend := ""
	if b, ok := j.(Backender); ok {
		backend = b.Backend()
	}
	sum := sha256.Sum256([]byte(ref + "\x00" + digest + "\x00" + OutlineDigest()))
	out := Record{Version: RecordVersion, SourceDigest: digest, ThresholdPolicy: ThresholdPolicy, Ref: ref, Language: lang,
		Answers: map[string]float64{}, Catalogue: OutlineDigest(), Backend: backend, Model: j.Model(), Battery: OutlineBattery,
		ID: hex.EncodeToString(sum[:]), Lines: [2]int{1, s.Lines}}
	for _, it := range outlineItems {
		out.Expected = append(out.Expected, it.ID)
	}
	state := map[string]any{"language": lang, "outline": OutlineText(s.Outline)}
	return askBattery(j, c, backend, OutlineBattery, OutlineDigest(), outlineItems, "outline", state, out, "Judged from the outline, not the bodies. ")
}
