package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
)

const (
	RecordVersion   = 2
	ThresholdPolicy = "noul-0.4-0.6-v1"
	ReviewPolicy    = "evidence-tier-v1"
)

const (
	TierP     = 0.80
	TierWhere = 0.60
)

const (
	TierPinned       = "pinned"
	TierUnpinned     = "unpinned"
	TierIntentStated = "intent_stated"
	TierAll          = "all"
)

var tierOrder = []string{TierPinned, TierUnpinned, TierIntentStated}

type Assessment struct {
	Status         string   `json:"assessment"`
	HasFindings    bool     `json:"has_findings"`
	Applicable     int      `json:"applicable"`
	Answered       int      `json:"answered"`
	Abstained      int      `json:"abstained"`
	Missing        int      `json:"missing"`
	ContextLimited int      `json:"context_limited"`
	NonApplicable  int      `json:"non_applicable"`
	Reasons        []string `json:"reason_codes"`
}

func validProbability(p float64) bool { return !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= 1 }

func AssessmentOf(r Record) Assessment {
	a := Assessment{Status: "unknown", HasFindings: len(r.Findings) > 0, Reasons: append([]string{}, r.ReasonCodes...)}
	if r.Version != RecordVersion || r.ThresholdPolicy != ThresholdPolicy {
		a.Reasons = unique(append(a.Reasons, "legacy_coverage_unknown"))
		return a
	}
	expected := unique(r.Expected)
	a.Applicable, a.NonApplicable = len(expected), len(unique(r.NonApplicable))
	for _, id := range expected {
		p, ok := r.Answers[id]
		if !ok || !validProbability(p) {
			a.Missing++
			continue
		}
		a.Answered++
		if p >= PAbstain && p < PTrue {
			a.Abstained++
		}
		if contains(r.ContextLimited, id) {
			a.ContextLimited++
		}
	}
	if a.Missing > 0 {
		a.Reasons = append(a.Reasons, "missing_answers")
	}
	if a.Abstained > 0 {
		a.Reasons = append(a.Reasons, "abstained")
	}
	if a.ContextLimited > 0 {
		a.Reasons = append(a.Reasons, "context_limited")
	}
	if r.Skipped != "" {
		a.Reasons = append(a.Reasons, r.Skipped)
	}
	determinate := 0
	for _, id := range expected {
		if p, ok := r.Answers[id]; ok && validProbability(p) && (p < PAbstain || p >= PTrue) && !contains(r.ContextLimited, id) {
			determinate++
		}
	}
	if determinate > 0 {
		a.Status = "partial"
		if determinate == a.Applicable && len(a.Reasons) == 0 {
			a.Status = "assessed"
		}
	}
	a.Reasons = unique(a.Reasons)
	return a
}

type Coverage struct {
	Ref             string   `json:"ref"`
	SourceDigest    string   `json:"source_digest,omitempty"`
	Kind            string   `json:"kind"`
	Disposition     string   `json:"disposition"`
	Reasons         []string `json:"reason_codes"`
	Size            int64    `json:"size,omitempty"`
	ExpectedChunks  int      `json:"expected_chunks,omitempty"`
	CompletedChunks int      `json:"completed_chunks,omitempty"`
}

type ReviewEvidence struct {
	Finding
	Lines          [2]int `json:"lines"`
	Model          string `json:"model"`
	Backend        string `json:"backend,omitempty"`
	Catalogue      string `json:"catalogue"`
	ContextLimited bool   `json:"context_limited,omitempty"`

	IntentStated  bool     `json:"intent_stated,omitempty"`
	IntentStatedP *float64 `json:"intent_stated_p,omitempty"`
	IntentGuard   string   `json:"intent_guard,omitempty"`
	IntentNone    string   `json:"intent_none,omitempty"`

	IntentClaim string `json:"intent_claim,omitempty"`
}

const (
	intentNoteClear   = "Judge says a comment describes a deliberate omission; evaluator-directed-comment check clear; labelled intent_stated"
	intentNoteFired   = "Judge says a comment describes a deliberate omission; evaluator-directed-comment check fired; priority unchanged"
	intentNoteUnknown = "Judge says a comment describes a deliberate omission; evaluator-directed-comment check inconclusive or not answered; priority unchanged"
)

type FileReview struct {
	Ref          string `json:"ref"`
	SourceDigest string `json:"source_digest,omitempty"`
	Language     string `json:"language,omitempty"`
	Assessment
	Lanes      []string       `json:"lanes"`
	Tier       string         `json:"tier,omitempty"`
	Intent     *IntentCounts  `json:"intent,omitempty"`
	Languages  []string       `json:"languages,omitempty"`
	Size       *FileSize      `json:"size,omitempty"`
	Slop       []SlopFinding  `json:"slop,omitempty"`
	SlopCount  int            `json:"slop_count,omitempty"`
	SlopTop    []string       `json:"slop_top,omitempty"`
	SlopQs     *SlopQuestions `json:"slop_questions,omitempty"`
	NotChecked []string       `json:"not_checked,omitempty"`
	NoCode     int            `json:"no_code_windows,omitempty"`

	Unlisted Unlisted `json:"counted_not_listed,omitempty"`

	Omitted *Omitted `json:"omitted,omitempty"`

	shape           *FileShape
	judgedSlop      slopJudged
	Findings        []ReviewEvidence `json:"findings"`
	Ranges          [][2]int         `json:"assessed_ranges"`
	Provenance      []string         `json:"provenance"`
	SelectionReason string           `json:"selection_reason"`
	NextCheck       string           `json:"suggested_next_check"`
	LegacyIndex     int              `json:"legacy_index"`
	LegacyBand      string           `json:"legacy_band"`
	Coverage        *Coverage        `json:"coverage,omitempty"`
}

type ReviewReport struct {
	Policy             string       `json:"report_policy"`
	Files              []FileReview `json:"files"`
	Gaps               []Coverage   `json:"coverage_gaps"`
	InventoryComplete  bool         `json:"inventory_complete"`
	AssessmentComplete bool         `json:"assessment_complete"`
	ReportComplete     bool         `json:"report_complete"`
	Caveat             string       `json:"caveat"`
}

func fileKey(ref, digest string) string { return path.Clean(ref) + "\x00" + digest }

const IntentBattery = "intent"

func IntentKey(id, where string) string {
	if where == "" {
		where = "window"
	}
	return id + " @ " + where
}

func intentAsked(f Finding) bool {
	return f.ID == "EH-01" || f.ID == "EH-02" || f.ID == "EH-03"
}

type intentJoin struct {
	answers    map[string]float64
	failed     map[string]bool
	guard      map[string]string
	unreadable map[string]bool
	size       map[string]FileSize
	shapes     map[string]*FileShape
	side       map[string][]Record
}

const (
	GuardClear   = "clear"
	GuardFired   = "fired"
	GuardUnknown = "unknown"
)

func IntentUnreadable(ref string) Record {
	return Record{Ref: ref, Battery: IntentBattery, ReasonCodes: []string{"intent_unreadable"}}
}

func newIntentJoin(records []Record) (intentJoin, []Record) {
	j := intentJoin{answers: map[string]float64{}, failed: map[string]bool{}, guard: map[string]string{}, unreadable: map[string]bool{}, size: map[string]FileSize{}, shapes: map[string]*FileShape{}, side: map[string][]Record{}}
	var assessed []Record
	for _, r := range records {
		if r.Battery == SizeBattery {
			j.size[fileKey(r.Ref, r.SourceDigest)], _ = sizeRecorded(r)
			continue
		}
		if r.Battery == SlopBattery || r.Battery == OutlineBattery {
			k := fileKey(r.Ref, r.SourceDigest)
			j.side[k] = append(j.side[k], r)
			continue
		}
		if r.Battery == ShapeBattery {
			if r.Shape != nil && r.Shape.Skipped == "" {
				j.shapes[fileKey(r.Ref, r.SourceDigest)] = r.Shape
			}
			if size, ok := sizeRecorded(r); ok {
				j.size[fileKey(r.Ref, r.SourceDigest)] = size
			}
			continue
		}
		if r.Battery == IntentBattery {
			if contains(r.ReasonCodes, "intent_unreadable") {
				j.unreadable[path.Clean(r.Ref)] = true
				continue
			}
			window := fmt.Sprintf("%s\x00%d\x00", fileKey(r.Ref, r.SourceDigest), r.Chunk)
			failed := contains(r.ReasonCodes, IntentFailed)
			for _, k := range r.Expected {
				if p, ok := r.Answers[k]; ok && validProbability(p) && !failed {
					j.answers[window+k] = p
				} else if _, saved := j.answers[window+k]; !saved {
					j.failed[window+k] = true
				}
			}
			continue
		}
		assessed = append(assessed, r)
		if r.Battery == "docs" {
			window := fmt.Sprintf("%s\x00%d", fileKey(r.Ref, r.SourceDigest), r.Chunk)
			if p, ok := r.Answers["GC-02"]; ok && validProbability(p) {
				switch {
				case p < PAbstain:
					if j.guard[window] != GuardFired {
						j.guard[window] = GuardClear
					}
				case p >= PTrue:
					j.guard[window] = GuardFired
				}
			}
		}
	}
	return j, assessed
}

func (j intentJoin) mark(r Record, e *ReviewEvidence) {
	if r.Battery != "code" || !intentAsked(e.Finding) {
		return
	}
	window := fmt.Sprintf("%s\x00%d", fileKey(r.Ref, r.SourceDigest), r.Chunk)
	key := window + "\x00" + IntentKey(e.ID, e.Where)
	p, ok := j.answers[key]
	switch {
	case ok:
		e.IntentStatedP = &p
		e.IntentGuard = j.guard[window]
		if e.IntentGuard == "" {
			e.IntentGuard = GuardUnknown
		}
		e.IntentStated = p >= PTrue && e.IntentGuard == GuardClear
		if p >= PTrue {
			e.IntentClaim = map[string]string{GuardClear: intentNoteClear, GuardFired: intentNoteFired, GuardUnknown: intentNoteUnknown}[e.IntentGuard]
		}
	case j.unreadable[path.Clean(r.Ref)]:
		e.IntentNone = IntentNoneUnreadable
	case j.failed[key]:
		e.IntentNone = IntentNoneFailed
	default:
		e.IntentNone = IntentNoneNotAsked
	}
}

const (
	IntentNoneNotAsked   = "none: not asked"
	IntentNoneFailed     = "none: failed"
	IntentNoneUnreadable = "none: sidecar unreadable"
)

type IntentCounts struct {
	Asked      int `json:"intent_asked"`
	Saved      int `json:"intent_saved"`
	Failed     int `json:"intent_failed"`
	YesClear   int `json:"intent_yes_guard_clear,omitempty"`
	YesFired   int `json:"intent_yes_guard_fired,omitempty"`
	YesUnknown int `json:"intent_yes_guard_unknown,omitempty"`
}

func (c *IntentCounts) add(e ReviewEvidence) {
	switch {
	case e.IntentStatedP != nil:
		c.Asked++
		c.Saved++
		if *e.IntentStatedP >= PTrue {
			switch e.IntentGuard {
			case GuardClear:
				c.YesClear++
			case GuardFired:
				c.YesFired++
			default:
				c.YesUnknown++
			}
		}
	case e.IntentNone == IntentNoneFailed:
		c.Asked++
		c.Failed++
	}
}

func (c *IntentCounts) Add(o IntentCounts) {
	c.Asked += o.Asked
	c.Saved += o.Saved
	c.Failed += o.Failed
	c.YesClear += o.YesClear
	c.YesFired += o.YesFired
	c.YesUnknown += o.YesUnknown
}

func (c IntentCounts) result() map[string]any {
	return map[string]any{"intent_asked": c.Asked, "intent_saved": c.Saved, "intent_failed": c.Failed,
		"intent_yes_guard_clear": c.YesClear, "intent_yes_guard_fired": c.YesFired, "intent_yes_guard_unknown": c.YesUnknown}
}

func intentCountsOf(f FileReview) IntentCounts {
	var c IntentCounts
	for _, e := range f.Findings {
		c.add(e)
	}
	return c
}

func DeduplicateRecords(records []Record) []Record {
	out := make([]Record, 0, len(records))
	at := map[string]int{}
	for _, r := range records {
		key := fileKey(r.Ref, r.SourceDigest) + "\x00" + r.ID
		if r.ID == "" {
			b, _ := json.Marshal(r)
			h := sha256.Sum256(b)
			key = hex.EncodeToString(h[:])
		}
		if i, ok := at[key]; ok {
			out[i] = r
		} else {
			at[key] = len(out)
			out = append(out, r)
		}
	}
	return out
}

func BuildReview(records []Record, coverage []Coverage, inventoryComplete, reportComplete bool, lane string) ReviewReport {
	report := ReviewReport{Policy: ReviewPolicy, Files: []FileReview{}, Gaps: []Coverage{}, InventoryComplete: inventoryComplete && reportComplete, ReportComplete: reportComplete,
		Caveat: reviewCaveat}
	cov := map[string]Coverage{}
	for _, c := range coverage {
		c.Ref = path.Clean(c.Ref)
		c = currentCoverage(c)

		if c.Kind == "file" {
			key := fileKey(c.Ref, "")
			if prior, ok := cov[key]; ok && prior.Kind == "file" && prior.Disposition == "pending" {
				delete(cov, key)
			}
		}
		cov[fileKey(c.Ref, c.SourceDigest)] = c
	}
	intent, assessed := newIntentJoin(records)
	grouped := map[string][]Record{}
	for _, r := range DeduplicateRecords(assessed) {
		k := fileKey(r.Ref, r.SourceDigest)
		grouped[k] = append(grouped[k], r)
	}
	for k, c := range cov {
		if c.Kind != "subtree" {
			if _, ok := grouped[k]; !ok {
				grouped[k] = nil
			}
		}
	}
	complete := inventoryComplete && reportComplete
	for k, rs := range grouped {
		f := FileReview{Assessment: Assessment{Status: "unknown", Reasons: []string{}}, Lanes: []string{}, Findings: []ReviewEvidence{}, Ranges: [][2]int{}, Provenance: []string{}, LegacyIndex: 100}
		determinate, allAssessed := false, true
		chunks := map[int]bool{}
		noCode, docsAnswered := map[int]bool{}, map[int]bool{}
		seenEvidence := map[string]bool{}
		for _, r := range rs {
			chunks[r.Chunk] = true
			f.Ref, f.SourceDigest, f.Language = path.Clean(r.Ref), r.SourceDigest, r.Language
			if r.Language != "" {
				f.Languages = strings.Split(r.Language, "+")
			}
			if r.Battery == "docs" && batteryAnswered(r) {
				docsAnswered[r.Chunk] = true
			}
			if r.Skipped == "too_small" {
				noCode[r.Chunk] = true
				continue
			}
			a := AssessmentOf(r)
			f.Applicable += a.Applicable
			f.Answered += a.Answered
			f.Abstained += a.Abstained
			f.Missing += a.Missing
			f.ContextLimited += a.ContextLimited
			f.NonApplicable += a.NonApplicable
			f.Reasons = append(f.Reasons, a.Reasons...)
			if a.Status == "assessed" || a.Status == "partial" {
				determinate = true
			}
			allAssessed = allAssessed && a.Status == "assessed"
			if a.Answered > 0 {
				f.Ranges = append(f.Ranges, r.Lines)
			}
			f.Provenance = append(f.Provenance, strings.Join([]string{r.Model, r.Backend, r.Catalogue, r.Preprocessing, r.ThresholdPolicy}, " | "))
			if r.Index < f.LegacyIndex {
				f.LegacyIndex = r.Index
			}
			for _, finding := range r.Findings {
				finding.Lead = Lead(finding.ID)

				if a := finding.LocationAnswer; a != nil {
					finding.LocationAnswer = &Choice{Choice: a.Choice, Confidence: a.Confidence}
				}
				e := ReviewEvidence{Finding: finding, Lines: r.Lines, Model: r.Model, Backend: r.Backend, Catalogue: r.Catalogue, ContextLimited: contains(r.ContextLimited, finding.ID)}
				intent.mark(r, &e)
				b, _ := json.Marshal(e)
				ek := string(b)
				if !seenEvidence[ek] {
					f.Findings = append(f.Findings, e)
					seenEvidence[ek] = true
				}
			}
		}

		for chunk := range noCode {
			if docsAnswered[chunk] {
				f.NoCode++
				continue
			}
			allAssessed = false
			f.Reasons = append(f.Reasons, "too_small", "missing_answers")
		}
		if determinate {
			f.Status = "partial"
		}
		if c, ok := cov[k]; ok {
			f.Coverage = &c
			f.Ref, f.SourceDigest = c.Ref, c.SourceDigest
			f.Reasons = append(f.Reasons, c.Reasons...)
			switch c.Disposition {
			case "excluded":
				if len(rs) == 0 {
					f.Status = "excluded"
				}
			case "complete":
				if len(rs) > 0 && allAssessed && len(c.Reasons) == 0 && c.ExpectedChunks > 0 && c.CompletedChunks+f.NoCode == c.ExpectedChunks && len(chunks) == c.ExpectedChunks {
					f.Status = "assessed"
				}
			}
		} else {
			f.Reasons = append(f.Reasons, "file_completion_unknown")
		}
		f.HasFindings = len(f.Findings) > 0
		f.Reasons = unique(f.Reasons)
		f.Provenance = unique(f.Provenance)
		f.LegacyBand = Band(f.LegacyIndex)
		if f.Status != "assessed" {
			complete = false
			gap := Coverage{Ref: f.Ref, SourceDigest: f.SourceDigest, Kind: "file", Disposition: f.Status, Reasons: f.Reasons}
			if f.Coverage != nil {
				gap = *f.Coverage
				gap.Reasons = f.Reasons
				gap.Disposition = f.Status
			}
			report.Gaps = append(report.Gaps, gap)
		}
		for _, l := range []string{"correctness", "maintainability"} {
			_, n := lanePriority(f, l)
			if n > 0 {
				f.Lanes = append(f.Lanes, l)
			}
		}
		if contains(f.Lanes, "correctness") {
			f.Tier, _, _ = tierEvidence(f)
		}
		for _, e := range f.Findings {
			if intentAsked(e.Finding) {
				c := intentCountsOf(f)
				f.Intent = &c
				break
			}
		}
		if size, ok := intent.size[fileKey(f.Ref, f.SourceDigest)]; ok {
			f.Size = &size
		}
		f.shape = intent.shapes[fileKey(f.Ref, f.SourceDigest)]
		f.judgedSlop = intent.slopOf(f, intent.side[fileKey(f.Ref, f.SourceDigest)])
		if f.judgedSlop.asked.Asked > 0 {
			a := f.judgedSlop.asked
			f.SlopQs = &a
		}
		switch {
		case lane == "slop":

		case lane == "size":

			if f.Size == nil {
				continue
			}
		case !f.HasFindings || lane != "" && !contains(f.Lanes, lane):
			continue
		}
		f.SelectionReason = selectionReason(f, lane)
		f.NextCheck = reviewNextCheck
		report.Files = append(report.Files, f)
	}
	for _, c := range cov {
		if c.Kind == "subtree" {
			report.Gaps = append(report.Gaps, c)
			if c.Disposition != "excluded" {
				report.InventoryComplete = false
			}
			complete = false
		}
	}
	sort.Slice(report.Files, func(i, j int) bool {
		return reviewKey(report.Files[i], lane).less(reviewKey(report.Files[j], lane))
	})
	sort.Slice(report.Gaps, func(i, j int) bool {
		a, b := report.Gaps[i], report.Gaps[j]
		ar, br := gapPriority(a), gapPriority(b)
		if ar != br {
			return ar < br
		}
		return fileKey(a.Ref, a.SourceDigest) < fileKey(b.Ref, b.SourceDigest)
	})
	report.AssessmentComplete = complete
	return report
}

func findingLane(f Finding) string {
	if strings.HasPrefix(f.ID, "EH-") || strings.HasPrefix(f.ID, "DF-") || f.ID == "ST-03" || strings.HasPrefix(f.ID, "ID-") && f.Severity == "high" {
		return "correctness"
	}
	return "maintainability"
}
func lanePriority(f FileReview, lane string) (int, int) {
	rank := -1
	ids := map[string]bool{}
	for _, e := range f.Findings {
		if findingLane(e.Finding) != lane {
			continue
		}
		ids[e.ID] = true
		if n := severityRank[e.Severity]; n > rank {
			rank = n
		}
	}
	return rank, len(ids)
}

func pinnedEvidence(e ReviewEvidence) bool {
	return findingLane(e.Finding) == "correctness" && e.P >= TierP && e.Where != "" && e.WhereConfidence >= TierWhere &&
		!e.ContextLimited && !e.IntentStated
}

func tierEvidence(f FileReview) (tier string, p float64, n int) {
	qualifies := map[string]func(ReviewEvidence) bool{
		TierPinned:       pinnedEvidence,
		TierUnpinned:     func(e ReviewEvidence) bool { return findingLane(e.Finding) == "correctness" && !e.IntentStated },
		TierIntentStated: func(e ReviewEvidence) bool { return findingLane(e.Finding) == "correctness" && e.IntentStated },
	}
	for _, t := range tierOrder {
		ids := map[string]bool{}
		best := -1.0
		for _, e := range f.Findings {
			if qualifies[t](e) {
				ids[e.ID] = true
				best = max(best, e.P)
			}
		}
		if len(ids) > 0 {
			return t, best, len(ids)
		}
	}
	return "", 0, 0
}

func topEvidence(f FileReview, lane string) *ReviewEvidence {
	var top *ReviewEvidence
	better := func(a, b ReviewEvidence) bool {
		if severityRank[a.Severity] != severityRank[b.Severity] && f.Tier == "" {
			return severityRank[a.Severity] > severityRank[b.Severity]
		}
		if a.P != b.P {
			return a.P > b.P
		}
		return a.ID < b.ID
	}
	for i, e := range f.Findings {
		ok := false
		switch {
		case lane == "maintainability" || f.Tier == "":
			ok = findingLane(e.Finding) == "maintainability"
		case f.Tier == TierPinned:
			ok = pinnedEvidence(e)
		case f.Tier == TierUnpinned:
			ok = findingLane(e.Finding) == "correctness" && !e.IntentStated
		default:
			ok = findingLane(e.Finding) == "correctness" && e.IntentStated
		}
		if ok && (top == nil || better(e, *top)) {
			top = &f.Findings[i]
		}
	}
	return top
}

func selectionReason(f FileReview, lane string) string {
	if lane == "" {
		if contains(f.Lanes, "correctness") {
			lane = "correctness"
		} else {
			lane = "maintainability"
		}
	}
	if lane == "correctness" {
		tier, p, n := tierEvidence(f)
		return fmt.Sprintf(reviewSelectionTier, tier, p, n, f.Status)
	}
	rank, n := lanePriority(f, lane)
	severity := []string{"low", "medium", "high"}[max(0, rank)]
	return fmt.Sprintf(reviewSelection, lane, severity, n, f.Status)
}
func gapPriority(c Coverage) int {
	if c.Disposition == "excluded" {
		return 2
	}
	if contains(c.Reasons, NotAssessedEarlier) || contains(c.Reasons, "budget_pending") {
		return 1
	}
	return 0
}
func unique(xs []string) []string {
	out := append([]string{}, xs...)
	sort.Strings(out)
	n := 0
	for _, x := range out {
		if n == 0 || out[n-1] != x {
			out[n] = x
			n++
		}
	}
	return out[:n]
}

type rankedRow struct {
	ref, id, fault, where string
	p                     float64
	key                   foldKey
}

func pinnedRow(f FileReview) (rankedRow, bool) {
	if f.Tier != TierPinned {
		return rankedRow{}, false
	}
	t := topEvidence(f, "correctness")
	if t == nil {
		return rankedRow{}, false
	}
	return rankedRow{ref: f.Ref, id: t.ID, fault: Faults([]string{t.ID})[t.ID], where: t.Where, p: t.P, key: reviewKey(f, "correctness")}, true
}

func topRows(rows []rankedRow, row rankedRow, n int) []rankedRow {
	i := sort.Search(len(rows), func(i int) bool { return row.key.less(rows[i].key) })
	if i >= n {
		return rows
	}
	rows = append(rows, rankedRow{})
	copy(rows[i+1:], rows[i:])
	rows[i] = row
	return rows[:min(len(rows), n)]
}

func correctnessRows(rows []rankedRow) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"ref": r.ref, "id": r.id, "fault": r.fault, "p": r.p, "where": r.where})
	}
	return out
}

const worstBasis = "legacy maintainability index"

const NotAssessedEarlier = "not_assessed_by_earlier_version"

var renamedReasons = map[string]string{"oversize": NotAssessedEarlier, "nul_bytes": "not_text", "no_language": "language_not_recognised"}

var OutOfScope = []string{"not_text", "language_not_recognised", "generated", "minified", LargerThanSource}

var ScopeWords = map[string]string{
	IgnoredDir:                "what Go's tools ignore: a folder named testdata or beginning with . (every file in it), or a Go file in a folder beginning with _",
	"not_text":                "not text: a NUL byte, or not valid UTF-8",
	"language_not_recognised": "language not recognised",
	"generated":               "generated: it carries a generated-code marker in its first 40 lines",
	"minified":                "minified: a mean line length over 110 bytes",
	LargerThanSource:          "larger than 1 MiB: not treated as a source file",
}

func scopeReason(c Coverage) string {
	if contains(c.Reasons, IgnoredDir) {
		return IgnoredDir
	}
	if c.Kind != "file" {
		return ""
	}
	for _, r := range OutOfScope {
		if contains(c.Reasons, r) {
			return r
		}
	}
	return ""
}

func currentCoverage(c Coverage) Coverage {
	renamed := false
	for _, r := range c.Reasons {
		if _, ok := renamedReasons[r]; ok {
			renamed = true
		}
	}
	if renamed {
		out := make([]string, 0, len(c.Reasons))
		for _, r := range c.Reasons {
			if n, ok := renamedReasons[r]; ok {
				r = n
			}
			out = append(out, r)
		}
		c.Reasons = out
	}
	if scopeReason(c) != "" {
		c.Disposition = "excluded"
	}
	return c
}
