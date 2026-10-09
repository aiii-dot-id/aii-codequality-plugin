package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

type Judge interface {
	Ask(state map[string]any, questions map[string]any) (map[string]float64, error)
	Choose(state map[string]any, questions map[string]any) (map[string]Choice, error)
	Model() string
}

type Choice struct {
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}

type Cache interface {
	Get(key string) (json.RawMessage, bool)
	Put(key string, answers json.RawMessage) error
}

var ErrJudge = errors.New("judge unavailable")

var ErrRefused = errors.New("judge refused this text")

func JudgeReply(status int, body []byte) (map[string]float64, error) {
	if err := replyError(status, body); err != nil {
		return nil, err
	}
	return ParseAnswers(body)
}

func ChoiceReply(status int, body []byte) (map[string]Choice, error) {
	if err := replyError(status, body); err != nil {
		return nil, err
	}
	return ParseChoices(body)
}

func replyError(status int, body []byte) error {
	switch {
	case status == 200:
		return nil
	case status == 0 || status == 429 || status >= 500:
		return fmt.Errorf("%w: Jev answered %d", ErrJudge, status)
	case status == 401 || status == 403 && json.Valid(body):
		return fmt.Errorf("%w (%d): paste a key that works on the plugin's card", ErrKey, status)
	case status == 403:
		return fmt.Errorf("%w: Jev answered 403 with a page, not JSON", ErrRefused)
	}
	return fmt.Errorf("%w: Jev answered %d: %.200s", ErrRefused, status, body)
}

var ErrKey = errors.New("Jev refused the key")

const (
	PTrue    = 0.6
	PAbstain = 0.4

	MinBlankedBytes = 64

	ChunkBytes = 29000

	WhereMin = 0.7
)

type Finding struct {
	ID              string  `json:"id"`
	P               float64 `json:"p"`
	Severity        string  `json:"severity"`
	Dimension       string  `json:"dimension,omitempty"`
	Where           string  `json:"where,omitempty"`
	WhereConfidence float64 `json:"where_confidence,omitempty"`
	Lead            bool    `json:"lead,omitempty"`
}

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
	Skipped    string             `json:"skipped,omitempty"`
	Catalogue  string             `json:"catalogue"`
	Model      string             `json:"model"`
}

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

type Chunk struct {
	Text      string
	FirstLine int
	LastLine  int
}

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

func CacheKey(text, lang, model, call string) string {
	h := sha256.New()
	for _, s := range []string{text, lang, CatalogueDigest(), model, call} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func JudgeText(t *Table, j Judge, c Cache, ref, lang, text string) ([]Record, error) {
	L := t.Langs[lang]
	if L == nil {
		return nil, fmt.Errorf("language %q is not in the table", lang)
	}
	var blanked, comments string
	var inString []bool
	if lang == "markdown" {
		comments = text
	} else {
		blanked, comments, _, inString = BlankComments(text, L)
	}
	blanked, k1 := Redact(blanked)
	comments, k2 := Redact(comments)
	model := j.Model()
	ask := func(field, body string, items []Item, call string) (map[string]float64, bool, error) {
		key := CacheKey(body, lang, model, call)
		var a map[string]float64
		if hit, err := cached(c, key, &a); err != nil || hit {
			return a, hit, err
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
		return a, false, keep(c, key, a)
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
		used, hit, err := locate(j, c, lang, model, ch, linesOf(inString, ch), r.Findings)
		if err != nil {
			return recs, err
		}
		r.Calls += used
		r.Cached += hit
		for k, v := range r.Answers {
			r.Answers[k] = math.Round(v*1000) / 1000
		}
		recs = append(recs, r)
	}
	return recs, nil
}

func linesOf(marks []bool, ch Chunk) []bool {
	lo, hi := ch.FirstLine-1, ch.LastLine
	if lo >= len(marks) {
		return nil
	}
	return marks[lo:min(hi, len(marks))]
}

func locate(j Judge, c Cache, lang, model string, ch Chunk, inString []bool, findings []Finding) (calls, hits int, err error) {
	code := map[string]string{}
	for _, it := range CodeItems(lang) {
		code[it.ID] = it.Fault
	}
	var ids []string
	for _, f := range findings {
		if code[f.ID] != "" {
			ids = append(ids, f.ID)
		}
	}
	units := Units(ch.Text, ch.FirstLine, inString)
	if len(ids) == 0 || len(units) < 2 {
		return 0, 0, nil
	}
	sort.Strings(ids)
	state := map[string]string{}
	options := map[string]any{}
	for _, u := range units {
		state[u.Key] = u.Text
		options[u.Key] = "the unit " + u.Key
	}
	questions := map[string]any{}
	for _, id := range ids {
		questions[id] = map[string]any{"type": "choice", "criteria": options,
			"instructions": "In `units`: which unit contains the place where " + code[id] + "?"}
	}
	keys := make([]string, len(units))
	for i, u := range units {
		keys[i] = u.Key
	}

	key := CacheKey(ch.Text+"\x00"+strings.Join(ids, ",")+"\x00"+strings.Join(keys, "\n"), lang, model, "locate")
	var got map[string]Choice
	hit, err := cached(c, key, &got)
	if err != nil {
		return 0, 0, err
	}
	if !hit {
		if got, err = j.Choose(map[string]any{"language": lang, "units": state}, questions); err != nil {
			return 0, 0, err
		}
		if err := keep(c, key, got); err != nil {
			return 0, 0, err
		}
	}
	for k := range findings {
		a, ok := got[findings[k].ID]
		if ok && a.Confidence >= WhereMin && state[a.Choice] != "" {
			findings[k].Where = a.Choice
			findings[k].WhereConfidence = math.Round(a.Confidence*1000) / 1000
		}
	}
	calls, hits = count(hit)
	return calls, hits, nil
}

func cached(c Cache, key string, v any) (bool, error) {
	if c == nil {
		return false, nil
	}
	raw, ok := c.Get(key)
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("cache: entry %s: %w", key[:12], err)
	}
	return true, nil
}

func keep(c Cache, key string, v any) error {
	if c == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err == nil {
		err = c.Put(key, raw)
	}
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}

func count(hit bool) (calls, cached int) {
	if hit {
		return 0, 1
	}
	return 1, 0
}

func ParseChoices(body []byte) (map[string]Choice, error) {
	var r struct {
		Answers map[string]struct {
			Choice     *string  `json:"choice"`
			Confidence *float64 `json:"confidence"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%w: response is not JSON: %v", ErrJudge, err)
	}
	if len(r.Answers) == 0 {
		return nil, fmt.Errorf("%w: response has no answers", ErrJudge)
	}
	out := make(map[string]Choice, len(r.Answers))
	for id, a := range r.Answers {
		if a.Choice == nil || a.Confidence == nil || *a.Confidence < 0 || *a.Confidence > 1 || math.IsNaN(*a.Confidence) {
			return nil, fmt.Errorf("%w: answer %s has no choice and confidence", ErrJudge, id)
		}
		out[id] = Choice{Choice: *a.Choice, Confidence: *a.Confidence}
	}
	return out, nil
}

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

func RequestBody(model string, state, questions map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{"model": model, "state": state, "questions": questions})
}
