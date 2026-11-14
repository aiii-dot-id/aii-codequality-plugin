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

func JudgeTextProgress(t *Table, j Judge, c Cache, ref, lang, text string, chunk int, prior []Record, save func(Record) error, intent bool, langs ...string) ([]Record, error) {

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
	next := 0
	windowsOf := func() (int, SourceWindow, bool, error) {
		if next == len(windows) {
			return 0, SourceWindow{}, false, nil
		}
		next++
		return next - 1, windows[next-1], true, nil
	}
	return judgeWindows(t, j, c, ref, lang, hex.EncodeToString(h.Sum(nil)), len(windows), windowsOf, chunk, prior, save, intent, langs)
}

func judgeWindows(t *Table, j Judge, c Cache, ref, lang, digest string, count int, next func() (int, SourceWindow, bool, error), chunk int, prior []Record, save func(Record) error, intent bool, langs []string) ([]Record, error) {
	label := lang
	if len(langs) > 1 {
		label = LanguageLabel(langs)
	}
	backend := ""
	if b, ok := j.(Backender); ok {
		backend = b.Backend()
	}
	previous := map[string]Record{}
	for _, r := range prior {
		if r.Ref == ref && r.SourceDigest == digest && r.Version == RecordVersion && r.Preprocessing == PreprocessingVersion && r.Catalogue == CatalogueDigest() && r.ThresholdPolicy == ThresholdPolicy && r.Backend == backend {
			previous[r.ID] = r
		}

		if r.Ref == ref && r.SourceDigest == digest && (r.Battery == IntentBattery && r.Catalogue == IntentDigest() || r.Battery == SlopBattery && r.Catalogue == SlopDigest()) {
			previous[r.ID] = r
		}
	}
	var recs []Record
	emit := func(r Record) error {
		if r.Language == lang {
			r.Language = label
		}
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

	localize := func(r Record, body Chunk) error {
		_, _, _, marks := BlankComments(body.Text, t.Langs[lang])
		found := append([]Finding(nil), r.Findings...)
		calls, hits, _, kept, err := locate(j, c, lang, backend, body, marks, found)
		r.Calls += calls
		r.Cached += hits
		if !kept {
			r.Uncached++
		}
		switch {
		case errors.Is(err, ErrProgress):
			return err
		case errors.Is(err, ErrRetryExhausted):
			for k := range found {
				found[k].LocationReason = "localization_failed"
			}
		case errors.Is(err, ErrJudge) || errors.Is(err, ErrKey) || errors.Is(err, ErrCache):
			for i := range recs {
				if recs[i].ID == r.ID {
					recs[i].Calls = r.Calls
				}
			}
			return err
		case err != nil:
			for k := range found {
				found[k].LocationReason = "localization_failed"
			}
		}
		r.Findings = found
		return emit(r)
	}

	stated := func(r Record, ctx Chunk) error {
		if !intent || r.Battery != "code" {
			return nil
		}
		if keys := intentKeys(r); len(keys) > 0 {
			if old, ok := previous[intentID(r.ID, keys)]; ok {
				old.Calls, old.Cached, old.Uncached = 0, 0, 0
				recs = append(recs, old)
				return nil
			}
		}
		ir, asked, err := askIntent(j, c, backend, r, ctx)
		switch {
		case !asked:
			return err
		case err == nil:
			return emit(ir)
		case errors.Is(err, errIntentPartial):
			return emit(failedIntent(ir, "answered_in_part"))
		case errors.Is(err, ErrRetryExhausted):
			return emit(failedIntent(ir, "retry_exhausted"))
		case errors.Is(err, ErrJudge) || errors.Is(err, ErrKey) || errors.Is(err, ErrCache):
			for i := range recs {
				if recs[i].ID == r.ID {
					recs[i].Calls += ir.Calls
					recs[i].Cached += ir.Cached
				}
			}
			return err
		}
		var refusal *RequestError
		if errors.As(err, &refusal) {
			return emit(failedIntent(ir, string(refusal.Reason)))
		}
		return emit(failedIntent(ir, "refused"))
	}

	sloped := func(r Record, ctx Chunk) error {
		if !intent || r.Battery != "code" {
			return nil
		}
		if old, ok := previous[slopID(r.ID)]; ok {
			old.Calls, old.Cached, old.Uncached = 0, 0, 0
			recs = append(recs, old)
			return nil
		}
		sr, err := askSlop(j, c, backend, r, ctx)
		switch {
		case err == nil:
			return emit(sr)
		case errors.Is(err, errIntentPartial):
			return emit(failedIntent(sr, "answered_in_part"))
		case errors.Is(err, ErrRetryExhausted):
			return emit(failedIntent(sr, "retry_exhausted"))
		case errors.Is(err, ErrJudge) || errors.Is(err, ErrKey) || errors.Is(err, ErrCache):
			for i := range recs {
				if recs[i].ID == r.ID {
					recs[i].Calls += sr.Calls
				}
			}
			return err
		}
		return emit(failedIntent(sr, "refused"))
	}
	for {
		wi, w, more, err := next()
		if err != nil {
			return recs, err
		}
		if !more {
			break
		}
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
				Catalogue: CatalogueDigest(), Backend: backend, Model: j.Model(), Battery: battery.name, Boundary: w.Boundary, WindowCount: count}
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
				if localizationPending(old) {
					if err := localize(old, battery.body); err != nil {
						return recs, err
					}
				}
				if err := stated(latest(recs, old.ID), w.Context); err != nil {
					return recs, err
				}
				if err := sloped(latest(recs, old.ID), w.Context); err != nil {
					return recs, err
				}
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
			if len(langs) > 1 {
				state["languages"] = langs
			}
			questions := windowQuestions(battery.items, battery.field, battery.body)

			request, err := json.Marshal([]any{state, questions, PreprocessingVersion})
			if err != nil {
				return recs, err
			}
			key := func() string { return CacheKey(string(request), lang, KeyModel(backend, j.Model()), battery.name) }
			var answers map[string]float64
			hit := false
			if gate, ok := j.(interface{ CacheEligible() bool }); !ok || gate.CacheEligible() {
				if hit, err = cached(c, key(), &answers); err != nil {

					return recs, err
				}
			}
			if hit && !answersComplete(answers, battery.items) {
				hit = false
			}
			if !hit {
				answers, err = j.Ask(state, questions)
				r.Calls = sentCalls(err)
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
			if errors.Is(err, ErrKey) {

				return recs, err
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

			if localizationPending(r) {
				if err := localize(r, battery.body); err != nil {
					return recs, err
				}
			}
			if err := stated(latest(recs, r.ID), w.Context); err != nil {
				return recs, err
			}
			if err := sloped(latest(recs, r.ID), w.Context); err != nil {
				return recs, err
			}
		}
	}
	return recs, nil
}

func latest(recs []Record, id string) Record {
	for _, r := range recs {
		if r.ID == id {
			return r
		}
	}
	return Record{}
}

func localizationPending(r Record) bool {
	if r.Battery != "code" {
		return false
	}
	for _, f := range r.Findings {
		if f.Where == "" && f.LocationReason == "" {
			return true
		}
	}
	return false
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
