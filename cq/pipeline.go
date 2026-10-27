package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

const PreprocessingVersion = "context-windows-v2"

var ErrProgress = errors.New("assessment progress not saved")

var ErrInput = errors.New("input cannot be assessed")

func JudgeTextProgress(t *Table, j Judge, c Cache, ref, lang, text string, chunk int, prior []Record, save func(Record) error) ([]Record, error) {

	runtime.GC()
	windows, err := boundedContextWindows(t, lang, text, chunk)
	runtime.GC()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInput, err)
	}
	h := sha256.New()
	for start := 0; start < len(text); start += 32 << 10 {
		_, _ = h.Write([]byte(text[start:min(len(text), start+(32<<10))]))
	}
	digest := hex.EncodeToString(h.Sum(nil))
	backend := ""
	if b, ok := j.(Backender); ok {
		backend = b.Backend()
	}
	previous := map[string]Record{}
	for _, r := range prior {
		if r.Ref == ref && r.SourceDigest == digest && r.Version == RecordVersion && r.Preprocessing == PreprocessingVersion && r.Catalogue == CatalogueDigest() && r.ThresholdPolicy == ThresholdPolicy && r.Backend == backend {
			previous[r.ID] = r
		}
	}
	var recs []Record
	emit := func(r Record) error {
		if save != nil {
			if err := save(r); err != nil {
				return fmt.Errorf("%w: %w", ErrProgress, err)
			}
		}
		for i := range recs {
			if recs[i].ID == r.ID {
				recs[i] = r
				return nil
			}
		}
		recs = append(recs, r)
		return nil
	}
	for wi, w := range windows {
		batteries := []struct {
			name, field string
			body        Chunk
			items       []Item
		}{
			{"code", "code", w.Code, CodeItems(lang)},
			{"docs", "source", w.Context, w.DocItems},
		}
		for _, battery := range batteries {
			if len(battery.items) == 0 {
				continue
			}
			r := Record{Version: RecordVersion, SourceDigest: digest, Preprocessing: PreprocessingVersion, ThresholdPolicy: ThresholdPolicy,
				Ref: ref, Chunk: wi, Lines: [2]int{battery.body.FirstLine, battery.body.LastLine}, Language: lang, Answers: map[string]float64{},
				Catalogue: CatalogueDigest(), Backend: backend, Model: j.Model(), Battery: battery.name, Boundary: w.Boundary, WindowCount: len(windows)}
			for _, it := range battery.items {
				r.Expected = append(r.Expected, it.ID)
			}
			if battery.name == "docs" {
				r.NonApplicable = append([]string(nil), w.NonApplicable...)
			}
			if battery.name == "code" || lang == "markdown" {
				r.Redacted = w.Redacted
			}

			identity, _ := json.Marshal([]any{ref, digest, lang, wi, r.Lines, battery.name, r.Expected, PreprocessingVersion, CatalogueDigest(), ThresholdPolicy, chunk, backend})
			sum := sha256.Sum256(identity)
			r.ID = hex.EncodeToString(sum[:])
			for _, it := range battery.items {
				if w.SplitUnit || needsExternalContext(it.ID) {
					r.ContextLimited = append(r.ContextLimited, it.ID)
				}
			}
			if old, ok := previous[r.ID]; ok && batteryAnswered(old) {
				if old.Model != j.Model() {
					return recs, fmt.Errorf("%w: progress model %q differs from %q", ErrJudge, old.Model, j.Model())
				}

				old.Calls, old.Cached, old.Uncached = 0, 0, 0
				recs = append(recs, old)
				continue
			}
			if battery.name == "code" && len(strings.TrimSpace(battery.body.Text)) < MinBlankedBytes {
				r.Skipped = "too_small"
				finishRecord(&r)
				if err := emit(r); err != nil {
					return recs, err
				}
				continue
			}
			state := map[string]any{"language": lang, battery.field: battery.body.Text}
			questions := windowQuestions(battery.items, battery.field, battery.body)

			request, err := json.Marshal([]any{state, questions, PreprocessingVersion})
			if err != nil {
				return recs, err
			}
			key := func() string { return CacheKey(string(request), lang, KeyModel(backend, j.Model()), battery.name) }
			var answers map[string]float64
			hit := false
			if gate, ok := j.(interface{ CacheEligible() bool }); !ok || gate.CacheEligible() {
				hit, _ = cached(c, key(), &answers)
			}
			if hit && !answersComplete(answers, battery.items) {
				hit = false
			}
			if !hit {
				answers, err = j.Ask(state, questions)
				r.Calls = 1
			} else {
				r.Cached = 1
			}
			r.Model = j.Model()

			for _, it := range battery.items {
				if p, ok := answers[it.ID]; ok && validProbability(p) {
					r.Answers[it.ID] = p
				}
			}
			if err == nil && !answersComplete(answers, battery.items) {
				err = fmt.Errorf("%w: incomplete battery %s", ErrJudge, battery.name)
			}
			if err == nil && !hit && keep(c, key(), r.Answers) != nil {
				r.Uncached++
			}
			if err != nil {
				r.ReasonCodes = append(r.ReasonCodes, "battery_failed")
				var request *RequestError
				if errors.As(err, &request) {
					r.ReasonCodes = append(r.ReasonCodes, string(request.Reason))
				}
			}
			finishRecord(&r)
			if saveErr := emit(r); saveErr != nil {
				return recs, saveErr
			}
			if err != nil {
				return recs, err
			}
			if battery.name == "code" && len(r.Findings) > 0 {

				_, _, _, marks := BlankComments(battery.body.Text, t.Langs[lang])
				calls, hits, _, kept, locationErr := locate(j, c, lang, backend, battery.body, marks, r.Findings)
				r.Calls += calls
				r.Cached += hits
				if !kept {
					r.Uncached++
				}
				if errors.Is(locationErr, ErrProgress) {
					return recs, locationErr
				}
				if locationErr != nil {
					for k := range r.Findings {
						r.Findings[k].LocationReason = "localization_failed"
					}
				}
				if err := emit(r); err != nil {
					return recs, err
				}
			}
		}
	}
	return recs, nil
}

func answersComplete(a map[string]float64, items []Item) bool {
	for _, it := range items {
		if p, ok := a[it.ID]; !ok || !validProbability(p) {
			return false
		}
	}
	return true
}
func batteryAnswered(r Record) bool {
	if len(r.Expected) == 0 {
		return false
	}
	for _, id := range r.Expected {
		if p, ok := r.Answers[id]; !ok || !validProbability(p) {
			return false
		}
	}
	return true
}
func finishRecord(r *Record) {
	r.Findings, r.Abstained, r.Dimensions, r.Index, r.Uncertain = Score(r.Language, r.Answers, r.Battery == "docs")
	r.Band = Band(r.Index)
}

func needsExternalContext(id string) bool {
	switch id {
	case "ST-03", "EH-04", "DF-01", "DF-03", "DF-04":
		return true
	}
	return false
}
