package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
)

const (
	intentName       = "intent-v2"
	intentQuestion   = "In `source`, at %s, a comment states that this error or failure is ignored or not propagated on purpose."
	intentQuestionAt = "In `source`, a comment states that an error or failure of the kind flagged here is ignored or not propagated on purpose."
	intentTrue       = "Such a comment is present at or directly above those lines."
	intentFalse      = "No such comment is there."
)

func IntentDigest() string {
	b, _ := json.Marshal([]string{intentName, intentQuestion, intentQuestionAt, intentTrue, intentFalse})
	h := sha256.Sum256(b)
	return intentName + "@sha256:" + hex.EncodeToString(h[:8])
}

var ErrIntentFull = fmt.Errorf("%w: intent answers", ErrJournalFull)

const IntentFailed = "intent_failed"

func intentKeys(r Record) []string {
	var keys []string
	for _, f := range r.Findings {
		if intentAsked(f) {
			keys = append(keys, IntentKey(f.ID, f.Where))
		}
	}
	sort.Strings(keys)
	return keys
}

func intentID(codeID string, keys []string) string {
	identity, _ := json.Marshal([]any{codeID, IntentBattery, keys, IntentDigest()})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:])
}

func askIntent(j Judge, c Cache, backend string, r Record, ctx Chunk) (Record, bool, error) {
	keys := intentKeys(r)
	if len(keys) == 0 {
		return Record{}, false, nil
	}
	where := map[string]string{}
	for _, f := range r.Findings {
		if intentAsked(f) {
			where[IntentKey(f.ID, f.Where)] = f.Where
		}
	}
	questions := map[string]any{}
	for i, k := range keys {
		text := intentQuestionAt
		if where[k] != "" {
			text = fmt.Sprintf(intentQuestion, where[k])
		}
		questions[fmt.Sprintf("q%d", i+1)] = map[string]any{
			"type":         "noul",
			"instructions": fmt.Sprintf(questionLines, ctx.FirstLine, ctx.LastLine, text),
			"criteria":     map[string]any{"true": intentTrue, "false": intentFalse},
		}
	}
	state := map[string]any{"language": r.Language, "source": ctx.Text}
	request, err := json.Marshal([]any{state, questions, PreprocessingVersion, IntentDigest()})
	if err != nil {
		return Record{}, false, err
	}
	out := Record{Version: RecordVersion, SourceDigest: r.SourceDigest, Preprocessing: PreprocessingVersion, ThresholdPolicy: ThresholdPolicy,
		Ref: r.Ref, Chunk: r.Chunk, Lines: [2]int{ctx.FirstLine, ctx.LastLine}, Language: r.Language, Answers: map[string]float64{},
		Catalogue: IntentDigest(), Backend: backend, Model: j.Model(), Battery: IntentBattery, Boundary: r.Boundary, WindowCount: r.WindowCount,
		Expected: keys, ID: intentID(r.ID, keys)}
	key := CacheKey(string(request), r.Language, KeyModel(backend, j.Model()), IntentBattery)
	var answers map[string]float64
	hit := false
	if gate, ok := j.(interface{ CacheEligible() bool }); !ok || gate.CacheEligible() {
		if hit, err = cached(c, key, &answers); err != nil {
			return Record{}, false, err
		}
	}
	if !hit {
		got, err := j.Ask(state, questions)
		out.Calls = sentCalls(err)
		if err != nil {
			return out, true, err
		}
		answers = map[string]float64{}
		for i, k := range keys {
			if p, ok := got[fmt.Sprintf("q%d", i+1)]; ok {
				answers[k] = p
			}
		}
	} else {
		out.Cached = 1
	}
	for _, k := range keys {
		if p, ok := answers[k]; ok && validProbability(p) {
			out.Answers[k] = p
		}
	}
	if !batteryAnswered(out) {
		return out, true, errIntentPartial
	}
	if !hit && keep(c, key, out.Answers) != nil {
		out.Uncached++
	}
	return out, true, nil
}

func failedIntent(r Record, cause string) Record {
	r.ReasonCodes = append(r.ReasonCodes, IntentFailed, cause)
	return r
}

type IntentCursor struct {
	j       *Journal
	at      int64
	next    string
	started bool
	seen    map[uint64]struct{}
	Err     error
}

func NewIntentCursor(j *Journal) *IntentCursor {
	return &IntentCursor{j: j, seen: map[uint64]struct{}{}}
}

func (c *IntentCursor) consume(ref string, keep bool) (recs []Record, start int64, n int) {
	start = c.at
	end, err := c.j.WalkAt(c.at, func(e JournalEvent, at, _ int64) error {
		if e.Record == nil {
			return nil
		}
		r := path.Clean(e.Record.Ref)
		if r != ref {
			c.next, c.at = r, at
			return errStopWalk
		}
		if keep {
			recs = append(recs, *e.Record)
		}
		n++
		return nil
	})
	switch {
	case errors.Is(err, errStopWalk):
	case err != nil:
		c.Err = err
	default:
		c.next, c.at = "", end
	}
	return recs, start, n
}

func (c *IntentCursor) Take(ref string) (recs []Record, start int64, n int) {
	ref = path.Clean(ref)
	if c == nil || c.j == nil {
		return nil, 0, 0
	}
	if c.Err != nil {
		return []Record{IntentUnreadable(ref)}, 0, 0
	}
	c.seen[refHash(ref)] = struct{}{}
	if !c.started {
		c.started = true
		c.consume("\x00", false)
	}
	for c.Err == nil && c.next != "" && c.next != ref {
		if _, seen := c.seen[refHash(c.next)]; !seen {
			return nil, 0, 0
		}
		c.Err = fmt.Errorf("%w: intent answers for %s after its file", ErrJournalCorrupt, c.next)
	}
	if c.Err != nil {
		return []Record{IntentUnreadable(ref)}, 0, 0
	}
	if c.next != ref {
		return nil, 0, 0
	}
	recs, start, n = c.consume(ref, true)
	if c.Err != nil {
		return []Record{IntentUnreadable(ref)}, 0, 0
	}
	return recs, start, n
}

func (c *IntentCursor) Finish() error {
	if c == nil || c.j == nil || c.Err != nil {
		if c != nil {
			return c.Err
		}
		return nil
	}
	if c.next != "" {
		return fmt.Errorf("%w: intent answers for %s, a file the journal does not hold", ErrJournalCorrupt, c.next)
	}
	return nil
}

func ReadIntent(j *Journal, start int64, n int) ([]Record, error) {
	var recs []Record
	_, err := j.WalkAt(start, func(e JournalEvent, _, _ int64) error {
		if len(recs) == n {
			return errStopWalk
		}
		if e.Record != nil {
			recs = append(recs, *e.Record)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, err
	}
	return recs, nil
}

var errIntentPartial = errors.New("intent answers incomplete")

var errNoIntentFile = errors.New("intent answer without an intent file")
