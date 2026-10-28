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
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type Cache interface {
	Get(key string) (json.RawMessage, bool)
	Put(key string, answers json.RawMessage) error
}

var ErrTooLarge = errors.New("file is larger than the limit it is read to")

var ErrJudge = errors.New("judge unavailable")

var ErrRefused = errors.New("judge refused this text")

var ErrKey = errors.New("the judge refused the key")

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
	LocationReason  string  `json:"location_reason,omitempty"`
	LocationAnswer  *Choice `json:"location_answer,omitempty"`
}

type Record struct {
	Version         int                `json:"version,omitempty"`
	ID              string             `json:"record_id,omitempty"`
	SourceDigest    string             `json:"source_digest,omitempty"`
	Preprocessing   string             `json:"preprocessing,omitempty"`
	Battery         string             `json:"battery,omitempty"`
	Boundary        string             `json:"boundary,omitempty"`
	WindowCount     int                `json:"window_count,omitempty"`
	ThresholdPolicy string             `json:"threshold_policy,omitempty"`
	Expected        []string           `json:"expected,omitempty"`
	NonApplicable   []string           `json:"non_applicable,omitempty"`
	ContextLimited  []string           `json:"context_limited,omitempty"`
	ReasonCodes     []string           `json:"reason_codes,omitempty"`
	Ref             string             `json:"ref"`
	Chunk           int                `json:"chunk"`
	Lines           [2]int             `json:"lines"`
	Language        string             `json:"language"`
	Findings        []Finding          `json:"findings"`
	Abstained       []string           `json:"abstained"`
	Dimensions      map[string]int     `json:"dimensions"`
	Index           int                `json:"index"`
	Band            string             `json:"band"`
	Uncertain       bool               `json:"uncertain"`
	Redacted        int                `json:"redacted"`
	Answers         map[string]float64 `json:"answers"`
	Calls           int                `json:"calls"`
	Cached          int                `json:"cached"`
	Skipped         string             `json:"skipped,omitempty"`
	Catalogue       string             `json:"catalogue"`
	Model           string             `json:"model"`
	Backend         string             `json:"backend,omitempty"`
	Uncached        int                `json:"uncached,omitempty"`
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

func Chunks(blanked string) []Chunk { return ChunksOf(blanked, ChunkBytes) }

func ChunksOf(blanked string, size int) []Chunk {
	if size <= 0 {
		size = ChunkBytes
	}
	if len(blanked) <= size {
		return []Chunk{{Text: blanked, FirstLine: 1, LastLine: strings.Count(blanked, "\n") + 1}}
	}
	var out []Chunk
	line := 1
	for len(blanked) > 0 {
		cut := len(blanked)
		if cut > size {
			cut = size
			if i := strings.LastIndex(blanked[:cut], "\n\n"); i > size/2 {
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

func KeyModel(backend, model string) string {
	if backend == "" || backend == "jev" {
		return model
	}
	return backend + "\x00" + model
}

type Backender interface{ Backend() string }

func JudgeText(t *Table, j Judge, c Cache, ref, lang, text string) ([]Record, error) {
	return JudgeTextChunked(t, j, c, ref, lang, text, ChunkBytes)
}

func JudgeTextChunked(t *Table, j Judge, c Cache, ref, lang, text string, chunk int) ([]Record, error) {
	return JudgeTextProgress(t, j, c, ref, lang, text, chunk, nil, nil)
}

func linesOf(marks []bool, ch Chunk) []bool {
	lo, hi := ch.FirstLine-1, ch.LastLine
	if lo >= len(marks) {
		return nil
	}
	return marks[lo:min(hi, len(marks))]
}

func locate(j Judge, c Cache, lang, backend string, ch Chunk, inString []bool, findings []Finding) (calls, hits int, model string, kept bool, err error) {
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
	units := units(ch.Text, ch.FirstLine, inString, 0)
	if len(ids) == 0 {
		return 0, 0, "", true, nil
	}
	for k := range findings {
		findings[k].LocationReason = "location_unknown"
	}
	if len(units) < 2 || len(units) > 254 {
		why := "single_unit_not_localized"
		if len(units) > 254 {
			why = "location_option_limit"
		}
		for k := range findings {
			if code[findings[k].ID] != "" {
				findings[k].LocationReason = why
			}
		}
		return 0, 0, "", true, nil
	}
	sort.Strings(ids)
	state := map[string]string{}
	options := map[string]any{}
	for _, u := range units {
		state[u.Key] = u.Text
		options[u.Key] = "the unit " + u.Key
	}
	options["cannot_localize"] = locateNone
	questions := map[string]any{}
	for _, id := range ids {
		questions[id] = map[string]any{"type": "choice", "criteria": options,
			"instructions": fmt.Sprintf(locateQuestion, code[id])}
	}
	keys := make([]string, len(units))
	for i, u := range units {
		keys[i] = u.Key
	}

	asked := "localization-escape-v2\x00" + ch.Text + "\x00" + strings.Join(ids, ",") + "\x00" + strings.Join(keys, "\n")
	model = j.Model()
	var got map[string]Choice
	hit, err := cached(c, CacheKey(asked, lang, KeyModel(backend, model), "locate"), &got)
	if err != nil {
		return 0, 0, "", true, err
	}
	kept = true
	if !hit {
		if got, err = j.Choose(map[string]any{"language": lang, "units": state}, questions); err != nil {
			return 1, 0, "", true, err
		}
		model = j.Model()
		kept = keep(c, CacheKey(asked, lang, KeyModel(backend, model), "locate"), got) == nil
	}
	for k := range findings {
		a, ok := got[findings[k].ID]
		if !ok {
			continue
		}
		findings[k].LocationAnswer = &a
		switch {
		case a.Choice == "cannot_localize":
			findings[k].LocationReason = "cannot_localize"
		case state[a.Choice] == "":
			findings[k].LocationReason = "invalid_location_key"
		case !validProbability(a.Confidence) || a.Confidence < WhereMin:
			findings[k].LocationReason = "low_location_confidence"
		default:
			findings[k].Where = a.Choice
			findings[k].WhereConfidence = a.Confidence
			findings[k].LocationReason = ""
			if len(a.Probabilities) == 0 {
				findings[k].LocationReason = "location_distribution_unavailable"
			}
		}
	}
	calls, hits = count(hit)
	return calls, hits, model, kept, nil
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
		return false, nil
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

func ReplyModel(body []byte) string {
	var r struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Model) > 128 || strings.TrimSpace(r.Model) != r.Model {
		return ""
	}
	return r.Model
}

func ParseChoices(body []byte) (map[string]Choice, error) {
	var r struct {
		Answers map[string]struct {
			Choice        *string            `json:"choice"`
			Confidence    *float64           `json:"confidence"`
			Probabilities map[string]float64 `json:"probabilities"`
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
		sum := 0.0
		for _, p := range a.Probabilities {
			if !validProbability(p) {
				return nil, fmt.Errorf("%w: answer %s has invalid probabilities", ErrJudge, id)
			}
			sum += p
		}
		if len(a.Probabilities) > 0 && math.Abs(sum-1) > 0.01 {
			return nil, fmt.Errorf("%w: answer %s probabilities do not sum to one", ErrJudge, id)
		}
		out[id] = Choice{Choice: *a.Choice, Confidence: *a.Confidence, Probabilities: a.Probabilities}
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

const MaxRequestBytes = 256 << 10

var ErrRequestSize = errors.New("judge request exceeds encoded body limit")

func RequestBody(model string, state, questions map[string]any) ([]byte, error) {
	b, err := json.Marshal(map[string]any{"model": model, "state": state, "questions": questions})
	if err != nil {
		return nil, err
	}
	if len(b) > MaxRequestBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrRequestSize, len(b), MaxRequestBytes)
	}
	return b, nil
}
