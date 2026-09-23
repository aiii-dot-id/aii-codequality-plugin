// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Judge asks one battery of Noul questions about one state and returns P(true) per question.
type Judge interface {
	Ask(state map[string]any, questions map[string]any) (map[string]float64, error)
	Model() string
}

// Cache holds Jev's answers by content key, so an unchanged text is never asked twice.
type Cache interface {
	Get(key string) (map[string]float64, bool)
	Put(key string, answers map[string]float64) error
}

// ErrJudge wraps a transient judge failure: the call did not produce answers; retrying later may.
var ErrJudge = errors.New("judge unavailable")

// ErrRefused wraps the judge's service declining this particular text. Retrying cannot help; the
// text is recorded as not judged.
var ErrRefused = errors.New("judge refused this text")

// JudgeReply reads Jev's answer to one call by its HTTP status. 200 carries the answers. 429
// and 5xx are the service being unavailable: ErrJudge, and the next step retries. A 401, or a
// 403 answered in JSON, is the key being refused — no retry fixes it until the operator pastes
// a key that works. A 403 page from Jev, and any other 4xx, is Jev declining this text:
// ErrRefused, and the scan records the file and moves on. Status 0 is no answer at all.
func JudgeReply(status int, body []byte) (map[string]float64, error) {
	switch {
	case status == 200:
		return ParseAnswers(body)
	case status == 0 || status == 429 || status >= 500:
		return nil, fmt.Errorf("%w: Jev answered %d", ErrJudge, status)
	case status == 401 || status == 403 && json.Valid(body):
		return nil, fmt.Errorf("%w (%d): paste a key that works on the plugin's card", ErrKey, status)
	case status == 403:
		return nil, fmt.Errorf("%w: Jev answered 403 with a page, not JSON", ErrRefused)
	}
	return nil, fmt.Errorf("%w: Jev answered %d: %.200s", ErrRefused, status, body)
}

// ErrKey wraps Jev refusing the key: no retry helps until the operator pastes one that works.
var ErrKey = errors.New("Jev refused the key")

// Scoring constants: a Noul at or above PTrue is a finding; between PAbstain and PTrue it is an
// abstention; below, the fault is absent.
const (
	PTrue    = 0.6
	PAbstain = 0.4
	// MinBlankedBytes: a chunk with less code than this is not judged (an empty chunk reads clean).
	MinBlankedBytes = 64
	// ChunkBytes: the state budget per call, 12,000 tokens at the measured 2.42 bytes per token.
	ChunkBytes = 29000
)

// Finding is one fault Jev judged present.
type Finding struct {
	ID        string  `json:"id"`
	P         float64 `json:"p"`
	Severity  string  `json:"severity"`
	Dimension string  `json:"dimension,omitempty"`
}

// Record is the judgment of one chunk.
type Record struct {
	Ref        string             `json:"ref"`
	Chunk      int                `json:"chunk"`
	Lines      [2]int             `json:"lines"`
	Language   string             `json:"language"`
	Findings   []Finding          `json:"findings"`
	Abstained  []string           `json:"abstained"`
	Dimensions map[string]int     `json:"dimensions"`
	Index      int                `json:"index"`
	Band       string             `json:"band"`
	Uncertain  bool               `json:"uncertain"`
	Redacted   int                `json:"redacted"`
	Answers    map[string]float64 `json:"answers"`
	Calls      int                `json:"calls"`
	Cached     int                `json:"cached"`
	Skipped    string             `json:"skipped,omitempty"` // too_small
	Catalogue  string             `json:"catalogue"`
	Model      string             `json:"model"`
}

// Band names an index range.
func Band(index int) string {
	switch {
	case index >= 90:
		return "clean"
	case index >= 75:
		return "minor"
	case index >= 60:
		return "moderate"
	}
	return "severe"
}

// Score builds the scoring half of a record from the answers of both calls.
func Score(lang string, answers map[string]float64, docsAsked bool) (findings []Finding, abstained []string, dims map[string]int, index int, uncertain bool) {
	deduction := map[string]int{}
	asked := 0
	batteries := [][]Item{CodeItems(lang)}
	if docsAsked {
		batteries = append(batteries, DocItems())
	}
	findings, abstained = []Finding{}, []string{}
	for _, items := range batteries {
		for _, it := range items {
			p, ok := answers[it.ID]
			if !ok {
				continue
			}
			asked++
			switch {
			case p >= PTrue:
				findings = append(findings, Finding{ID: it.ID, P: math.Round(p*1000) / 1000, Severity: it.Severity, Dimension: it.Dimension})
				if it.Dimension != "" {
					deduction[it.Dimension] += Penalty[it.Severity]
				}
			case p >= PAbstain:
				abstained = append(abstained, it.ID)
			}
		}
	}
	dims = map[string]int{}
	sum := 0
	present := Dimensions(lang)
	for _, d := range present {
		v := 100 - deduction[d]
		if v < 0 {
			v = 0
		}
		dims[d] = v
		sum += v
	}
	index = sum / len(present)
	return findings, abstained, dims, index, asked > 0 && len(abstained)*4 > asked
}

// Chunk is a piece of a file judged in one code call.
type Chunk struct {
	Text      string
	FirstLine int
	LastLine  int
}

// Chunks cuts blanked text into pieces of at most ChunkBytes, at blank lines where it can.
// Nearly every source file is one chunk.
func Chunks(blanked string) []Chunk {
	if len(blanked) <= ChunkBytes {
		return []Chunk{{Text: blanked, FirstLine: 1, LastLine: strings.Count(blanked, "\n") + 1}}
	}
	var out []Chunk
	line := 1
	for len(blanked) > 0 {
		cut := len(blanked)
		if cut > ChunkBytes {
			cut = ChunkBytes
			if i := strings.LastIndex(blanked[:cut], "\n\n"); i > ChunkBytes/2 {
				cut = i + 1
			} else if i := strings.LastIndexByte(blanked[:cut], '\n'); i > 0 {
				cut = i + 1
			}
		}
		piece := blanked[:cut]
		n := strings.Count(piece, "\n")
		out = append(out, Chunk{Text: piece, FirstLine: line, LastLine: line + n})
		line += n
		blanked = blanked[cut:]
	}
	return out
}

// CacheKey binds an answer set to the text, language, catalogue, model and call.
func CacheKey(text, lang, model, call string) string {
	h := sha256.New()
	for _, s := range []string{text, lang, CatalogueDigest(), model, call} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// JudgeText runs the pipeline over one text: blank comments, redact secrets, then per chunk the
// code call over the blanked code; the comment call over the file's comments once, attributed
// to the first chunk. The judge is asked only on a cache miss.
func JudgeText(t *Table, j Judge, c Cache, ref, lang, text string) ([]Record, error) {
	L := t.Langs[lang]
	if L == nil {
		return nil, fmt.Errorf("language %q is not in the table", lang)
	}
	var blanked, comments string
	if lang == "markdown" {
		comments = text // Markdown is judged as documentation, whole
	} else {
		blanked, comments, _ = BlankComments(text, L)
	}
	blanked, k1 := Redact(blanked)
	comments, k2 := Redact(comments)
	model := j.Model()
	ask := func(field, body string, items []Item, call string) (map[string]float64, bool, error) {
		key := CacheKey(body, lang, model, call)
		if c != nil {
			if a, ok := c.Get(key); ok {
				return a, true, nil
			}
		}
		a, err := j.Ask(map[string]any{"language": lang, field: body}, Questions(items, field))
		if err != nil {
			return nil, false, err
		}
		for _, it := range items {
			if _, ok := a[it.ID]; !ok {
				return nil, false, fmt.Errorf("%w: no answer for %s", ErrJudge, it.ID)
			}
		}
		if c != nil {
			if err := c.Put(key, a); err != nil {
				return nil, false, fmt.Errorf("cache: %w", err)
			}
		}
		return a, false, nil
	}
	var chunks []Chunk
	if lang == "markdown" {
		chunks = []Chunk{{FirstLine: 1, LastLine: strings.Count(text, "\n") + 1}}
	} else {
		chunks = Chunks(blanked)
	}
	var docAns map[string]float64
	docsAsked := strings.TrimSpace(comments) != ""
	recs := make([]Record, 0, len(chunks))
	for i, ch := range chunks {
		r := Record{Ref: ref, Chunk: i, Lines: [2]int{ch.FirstLine, ch.LastLine}, Language: lang, Answers: map[string]float64{},
			Catalogue: CatalogueDigest(), Model: model}
		if i == 0 {
			r.Redacted = k1 + k2
		}
		code := CodeItems(lang)
		if len(code) > 0 {
			if len(strings.TrimSpace(ch.Text)) < MinBlankedBytes {
				r.Skipped = "too_small"
			} else {
				a, hit, err := ask("code", ch.Text, code, "code")
				if err != nil {
					return recs, err
				}
				r.Calls, r.Cached = count(hit)
				for k, v := range a {
					r.Answers[k] = v
				}
			}
		}
		if i == 0 && docsAsked {
			a, hit, err := ask("comments", comments, DocItems(), "docs")
			if err != nil {
				return recs, err
			}
			calls, cached := count(hit)
			r.Calls += calls
			r.Cached += cached
			docAns = a
		}
		for k, v := range docAns {
			if i == 0 {
				r.Answers[k] = v
			}
		}
		if r.Skipped != "" && r.Calls+r.Cached == 0 {
			r.Findings, r.Abstained, r.Dimensions = []Finding{}, []string{}, map[string]int{}
			recs = append(recs, r)
			continue
		}
		r.Findings, r.Abstained, r.Dimensions, r.Index, r.Uncertain = Score(lang, r.Answers, i == 0 && docsAsked)
		r.Band = Band(r.Index)
		for k, v := range r.Answers {
			r.Answers[k] = math.Round(v*1000) / 1000
		}
		recs = append(recs, r)
	}
	return recs, nil
}

func count(hit bool) (calls, cached int) {
	if hit {
		return 0, 1
	}
	return 1, 0
}

// ParseAnswers reads a Jev /v1/systemone response body into P(true) per Noul.
func ParseAnswers(body []byte) (map[string]float64, error) {
	var r struct {
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%w: response is not JSON: %v", ErrJudge, err)
	}
	if len(r.Answers) == 0 {
		return nil, fmt.Errorf("%w: response has no answers", ErrJudge)
	}
	out := make(map[string]float64, len(r.Answers))
	for id, a := range r.Answers {
		if a.Noul == nil || *a.Noul < 0 || *a.Noul > 1 || math.IsNaN(*a.Noul) {
			return nil, fmt.Errorf("%w: answer %s has no probability", ErrJudge, id)
		}
		out[id] = *a.Noul
	}
	return out, nil
}

// RequestBody is the Jev /v1/systemone request.
func RequestBody(model string, state, questions map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{"model": model, "state": state, "questions": questions})
}
