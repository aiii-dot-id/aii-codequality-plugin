package cq

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

var ErrStaleCursor = errors.New(staleCursor)
var ErrReviewEntryTooLarge = errors.New(reviewEntryTooLarge)

var ReviewLanes = []string{"correctness", "maintainability", "size", "slop"}

var ErrTierInvalid = errors.New(tierInvalid)

type reviewCursor struct {
	Generation string `json:"generation"`
	Shape      string `json:"shape"`
	Lane       string `json:"lane"`
	Tier       string `json:"tier,omitempty"`
	Path       string `json:"path,omitempty"`
	All        bool   `json:"all,omitempty"`
	Th         [5]int `json:"th"`
	Offset     int    `json:"offset"`
}

const earlierListMax = 100

type EarlierFile struct {
	Ref  string `json:"ref"`
	Size int64  `json:"size"`
}

type ScopeFile struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
	Words  string `json:"why"`
	Size   int64  `json:"size,omitempty"`
}

type TriageRow struct {
	Ref        string        `json:"ref"`
	Tier       string        `json:"tier,omitempty"`
	Top        *TriageTop    `json:"top,omitempty"`
	Counts     TriageCounts  `json:"counts"`
	Intent     *IntentCounts `json:"intent,omitempty"`
	Languages  []string      `json:"languages,omitempty"`
	Size       *FileSize     `json:"size,omitempty"`
	Slop       int           `json:"slop,omitempty"`
	SlopTop    []string      `json:"slop_top,omitempty"`
	Assessment string        `json:"assessment"`
	Answered   int           `json:"answered"`
	Applicable int           `json:"applicable"`
}

type TriageTop struct {
	ID    string  `json:"id"`
	Fault string  `json:"fault"`
	P     float64 `json:"p"`
	Where string  `json:"where,omitempty"`
	Lines [2]int  `json:"lines"`
}

type TriageCounts struct {
	Correctness     int `json:"correctness"`
	Maintainability int `json:"maintainability"`
	IntentStated    int `json:"intent_stated"`
}

func triageRow(f FileReview, lane string) TriageRow {
	row := TriageRow{Ref: f.Ref, Tier: f.Tier, Languages: f.Languages, Size: f.Size, Slop: f.SlopCount, SlopTop: f.SlopTop, Intent: f.Intent, Assessment: f.Status, Answered: f.Answered, Applicable: f.Applicable}
	for _, e := range f.Findings {
		if findingLane(e.Finding) == "correctness" {
			row.Counts.Correctness++
			if e.IntentStated {
				row.Counts.IntentStated++
			}
		} else {
			row.Counts.Maintainability++
		}
	}
	if t := topEvidence(f, lane); t != nil {
		row.Top = &TriageTop{ID: t.ID, Fault: Faults([]string{t.ID})[t.ID], P: t.P, Where: t.Where, Lines: t.Lines}
	}
	return row
}

func (q *ReviewQuery) normalize() error {
	switch q.Shape {
	case "review_queue", "coverage", "triage":
	default:
		return fmt.Errorf("unknown review shape %q", q.Shape)
	}
	if q.Shape == "triage" && q.Lane == "" {
		q.Lane = "correctness"
	}
	if q.Lane != "" && !contains(ReviewLanes, q.Lane) {
		return fmt.Errorf("unknown review lane %q", q.Lane)
	}
	if q.Tier != "" && !q.tiered() {
		return ErrTierInvalid
	}
	if q.tiered() && q.Tier == "" {
		q.Tier = TierPinned
	}
	switch q.Tier {
	case "", TierPinned, TierUnpinned, TierIntentStated, TierAll:
	default:
		return ErrTierInvalid
	}
	if q.PageBytes < 4096 || q.PageBytes > 200000 || q.Limit < 1 || q.Limit > 100 {
		return errors.New(pageBoundsInvalid)
	}
	return nil
}

func (q ReviewQuery) tiered() bool {
	return q.Shape != "coverage" && (q.Lane == "" || q.Lane == "correctness")
}

func (q ReviewQuery) inPath(ref string) bool { return Match(q.Path, ref) }

func (q ReviewQuery) wantsFile(f FileReview) bool {
	if q.Lane == "size" {
		return q.inPath(f.Ref) && f.Size != nil && f.Size.Finding != ""
	}
	if q.Lane == "slop" {
		return q.inPath(f.Ref) && f.SlopCount > 0
	}

	if q.Lane == "" && !f.HasFindings || (q.Lane == "correctness" || q.Lane == "maintainability") && !contains(f.Lanes, q.Lane) {
		return false
	}
	return q.inPath(f.Ref) && (!q.tiered() || q.Tier == TierAll || f.Tier == q.Tier)
}

func (q ReviewQuery) entry(f FileReview) any {
	if q.Shape == "triage" {
		return triageRow(f, q.Lane)
	}
	return f
}

type Omitted struct {
	Findings int    `json:"findings"`
	Slop     int    `json:"slop"`
	Route    string `json:"route"`
}

func fitted(entry any, room int) ([]byte, error) {
	b, err := json.Marshal(entry)
	if err != nil || len(b) <= room {
		return b, err
	}
	f, ok := entry.(FileReview)
	if !ok {
		return nil, ErrReviewEntryTooLarge
	}
	findings, slop := f.Findings, f.Slop
	try := func(nf, ns int) ([]byte, error) {
		f.Findings, f.Slop = findings[:nf], slop[:ns]
		f.Omitted = &Omitted{Findings: len(findings) - nf, Slop: len(slop) - ns,
			Route: fmt.Sprintf("report shape=findings path=%s, for every finding of %s", f.Ref, f.Ref)}
		return json.Marshal(f)
	}
	nf := sort.Search(len(findings)+1, func(n int) bool { b, _ := try(n, 0); return len(b) > room }) - 1
	if nf < 0 {
		return nil, ErrReviewEntryTooLarge
	}
	ns := 0
	if nf == len(findings) {
		ns = sort.Search(len(slop)+1, func(n int) bool { b, _ := try(nf, n); return len(b) > room }) - 1
	}
	return try(nf, ns)
}

func newTierCounts() map[string]int {
	return map[string]int{TierPinned: 0, TierUnpinned: 0, TierIntentStated: 0}
}

type ReviewPage struct {
	Policy             string          `json:"report_policy"`
	Generation         string          `json:"generation"`
	Entries            json.RawMessage `json:"entries"`
	Next               string          `json:"next_page_cursor"`
	Total              int             `json:"total_entries"`
	GapCount           int             `json:"coverage_gap_count"`
	CoverageShape      string          `json:"coverage_shape"`
	TierCounts         map[string]int  `json:"tier_counts,omitempty"`
	EarlierFiles       []EarlierFile   `json:"not_assessed_by_earlier_version,omitempty"`
	EarlierCount       *int            `json:"not_assessed_by_earlier_version_count,omitempty"`
	EarlierVersion     string          `json:"earlier_version,omitempty"`
	OutOfScopeFiles    []ScopeFile     `json:"out_of_scope,omitempty"`
	OutOfScopeCount    *int            `json:"out_of_scope_count,omitempty"`
	PackageNotes       []PackageNote   `json:"package_notes,omitempty"`
	Thresholds         map[string]int  `json:"thresholds,omitempty"`
	SlopNote           string          `json:"slop_note,omitempty"`
	Unlisted           map[string]int  `json:"counted_not_listed,omitempty"`
	UnlistedWords      string          `json:"counted_not_listed_words,omitempty"`
	IntentNote         string          `json:"intent_note,omitempty"`
	IntentTotals       *IntentCounts   `json:"intent,omitempty"`
	InventoryComplete  bool            `json:"inventory_complete"`
	AssessmentComplete bool            `json:"assessment_complete"`
	ReportComplete     bool            `json:"report_complete"`
	Caveat             string          `json:"caveat"`

	block *pageBlock
}

func ReviewGeneration(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func PageReview(report ReviewReport, q ReviewQuery) (ReviewPage, error) {
	out := ReviewPage{Policy: report.Policy, Generation: q.Generation, CoverageShape: "coverage", InventoryComplete: report.InventoryComplete, AssessmentComplete: report.AssessmentComplete, ReportComplete: report.ReportComplete, Caveat: report.Caveat, Entries: json.RawMessage("[]")}
	if err := q.normalize(); err != nil {
		return out, err
	}
	offset := 0
	if q.Cursor != "" {
		var c reviewCursor
		b, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Generation != q.Generation || c.Shape != q.Shape || c.Lane != q.Lane || c.Tier != q.Tier || c.Path != q.Path || c.All != q.Size.ListAll || c.Th != q.Size.key() || c.Offset < 0 {
			return out, ErrStaleCursor
		}
		offset = c.Offset
	}
	var files []FileReview
	var gaps []Coverage
	counts := newTierCounts()
	var intentTotals IntentCounts
	var unlisted Unlisted
	for _, f := range report.Files {
		q.view(&f)
		if q.inPath(f.Ref) && f.Tier != "" {
			counts[f.Tier]++
		}
		if q.inPath(f.Ref) {
			unlisted.addAll(f.Unlisted)
		}
		if q.inPath(f.Ref) && f.Intent != nil {
			intentTotals.Add(*f.Intent)
		}
		if q.wantsFile(f) {
			files = append(files, f)
		}
	}

	sort.SliceStable(files, func(i, j int) bool { return reviewKey(files[i], q.Lane).less(reviewKey(files[j], q.Lane)) })
	earlier, scoped := 0, 0
	for _, g := range report.Gaps {
		if !q.inPath(g.Ref) {
			continue
		}
		gaps = append(gaps, g)
		if why := scopeReason(g); why != "" {
			if scoped < earlierListMax {
				out.OutOfScopeFiles = append(out.OutOfScopeFiles, ScopeFile{Ref: g.Ref, Reason: why, Words: ScopeWords[why], Size: g.Size})
			}
			scoped++
		}
		if g.Kind == "file" && contains(g.Reasons, NotAssessedEarlier) {
			if earlier < earlierListMax {
				out.EarlierFiles = append(out.EarlierFiles, EarlierFile{Ref: g.Ref, Size: g.Size})
			}
			earlier++
		}
	}
	out.GapCount = len(gaps)
	if q.Shape != "coverage" {
		out.TierCounts = counts
		out.IntentTotals = &intentTotals
	}

	if q.Shape != "coverage" {
		out.PackageNotes = q.PackageNotes
		out.SlopNote = q.SlopNote
	}
	q.unlisted(&out, unlisted)
	if q.Lane == "slop" || q.Lane == "size" {
		out.Thresholds = q.Size.Effective()
	}
	if earlier > 0 {
		out.EarlierVersion = q.EarlierVersion
	}
	if q.Shape == "triage" {
		out.OutOfScopeCount = &scoped
		out.EarlierCount = &earlier
	} else {
		out.EarlierFiles = nil
		out.OutOfScopeFiles = nil
	}
	total := len(files)
	if q.Shape == "coverage" {
		total = len(gaps)
	}
	out.Total = total
	if offset > total {
		return out, ErrStaleCursor
	}
	rows := make([]json.RawMessage, 0, min(q.Limit, total-offset))
	size := 2
	next := offset
	for next < total && len(rows) < q.Limit {
		var entry any
		if q.Shape == "coverage" {
			entry = gaps[next]
		} else {
			entry = q.entry(files[next])
		}
		b, err := json.Marshal(entry)
		if err != nil {
			return out, err
		}
		if size+len(b)+1 > q.PageBytes {
			if len(rows) > 0 {
				break
			}
			if b, err = fitted(entry, q.PageBytes-size-1); err != nil {
				return out, err
			}
		}
		size += len(b) + 1
		rows = append(rows, b)
		next++
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return out, err
	}
	out.Entries = b
	if next < total {
		b, err := json.Marshal(reviewCursor{Generation: q.Generation, Shape: q.Shape, Lane: q.Lane, Tier: q.Tier, Path: q.Path, All: q.Size.ListAll, Th: q.Size.key(), Offset: next})
		if err != nil {
			return out, err
		}
		out.Next = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}

func (q ReviewQuery) unlisted(out *ReviewPage, u Unlisted) {
	if kinds := unlistedOn(q.Lane); q.Shape != "coverage" && !q.Size.ListAll && kinds != nil {
		out.Unlisted, out.UnlistedWords = u.of(kinds), UnlistedWords
	}
}

func (q ReviewQuery) view(f *FileReview) {
	q.Size.markWith(f, q.Package)
	if f.SelectionReason != "" {
		f.SelectionReason = selectionReason(*f, q.Lane)
	}
}
