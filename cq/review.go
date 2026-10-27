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
	ReviewPolicy    = "severity-category-breadth-v1"
)

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
}

type FileReview struct {
	Ref          string `json:"ref"`
	SourceDigest string `json:"source_digest,omitempty"`
	Language     string `json:"language,omitempty"`
	Assessment
	Lanes           []string         `json:"lanes"`
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
		Caveat: "Model allegations, not confirmed defects. Severity/category ordering is an unvalidated candidate. Legacy scores are descriptive, not safety verdicts. Historical calibration is not current-model accuracy."}
	cov := map[string]Coverage{}
	for _, c := range coverage {
		c.Ref = path.Clean(c.Ref)

		if c.Kind == "file" {
			key := fileKey(c.Ref, "")
			if prior, ok := cov[key]; ok && prior.Kind == "file" && prior.Disposition == "pending" {
				delete(cov, key)
			}
		}
		cov[fileKey(c.Ref, c.SourceDigest)] = c
	}
	grouped := map[string][]Record{}
	for _, r := range DeduplicateRecords(records) {
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
		seenEvidence := map[string]bool{}
		for _, r := range rs {
			chunks[r.Chunk] = true
			f.Ref, f.SourceDigest, f.Language = path.Clean(r.Ref), r.SourceDigest, r.Language
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
				e := ReviewEvidence{Finding: finding, Lines: r.Lines, Model: r.Model, Backend: r.Backend, Catalogue: r.Catalogue, ContextLimited: contains(r.ContextLimited, finding.ID)}
				b, _ := json.Marshal(e)
				ek := string(b)
				if !seenEvidence[ek] {
					f.Findings = append(f.Findings, e)
					seenEvidence[ek] = true
				}
			}
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
				if len(rs) > 0 && allAssessed && len(c.Reasons) == 0 && c.ExpectedChunks > 0 && c.CompletedChunks == c.ExpectedChunks && len(chunks) == c.ExpectedChunks {
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
		if !f.HasFindings || lane != "" && !contains(f.Lanes, lane) {
			continue
		}
		f.SelectionReason = selectionReason(f, lane)
		f.NextCheck = "Inspect the cited source with callers, ownership contracts and tests; confirm the consequence or reject the allegation."
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
		a, b := report.Files[i], report.Files[j]
		l := lane
		if l == "" {
			ac, bc := contains(a.Lanes, "correctness"), contains(b.Lanes, "correctness")
			if ac != bc {
				return ac
			}
			if ac {
				l = "correctness"
			} else {
				l = "maintainability"
			}
		}
		ar, an := lanePriority(a, l)
		br, bn := lanePriority(b, l)
		if ar != br {
			return ar > br
		}
		if an != bn {
			return an > bn
		}
		return fileKey(a.Ref, a.SourceDigest) < fileKey(b.Ref, b.SourceDigest)
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
func selectionReason(f FileReview, lane string) string {
	if lane == "" {
		if contains(f.Lanes, "correctness") {
			lane = "correctness"
		} else {
			lane = "maintainability"
		}
	}
	rank, n := lanePriority(f, lane)
	severity := []string{"low", "medium", "high"}[max(0, rank)]
	return fmt.Sprintf("%s candidate: maximum %s catalogue severity; %d distinct flagged categories; assessment %s. Category breadth is not a defect count.", lane, severity, n, f.Status)
}
func gapPriority(c Coverage) int {
	if c.Disposition == "excluded" {
		return 2
	}
	if contains(c.Reasons, "oversize") || contains(c.Reasons, "budget_pending") {
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
