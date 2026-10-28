package cq

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrStaleCursor = errors.New(staleCursor)
var ErrReviewEntryTooLarge = errors.New(reviewEntryTooLarge)

type reviewCursor struct {
	Generation string `json:"generation"`
	Shape      string `json:"shape"`
	Lane       string `json:"lane"`
	Offset     int    `json:"offset"`
}

type ReviewPage struct {
	Policy             string          `json:"report_policy"`
	Generation         string          `json:"generation"`
	Entries            json.RawMessage `json:"entries"`
	Next               string          `json:"next_page_cursor"`
	Total              int             `json:"total_entries"`
	GapCount           int             `json:"coverage_gap_count"`
	CoverageShape      string          `json:"coverage_shape"`
	InventoryComplete  bool            `json:"inventory_complete"`
	AssessmentComplete bool            `json:"assessment_complete"`
	ReportComplete     bool            `json:"report_complete"`
	Caveat             string          `json:"caveat"`
}

func ReviewGeneration(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func PageReview(report ReviewReport, generation, shape, lane, cursor string, pageBytes, limit int) (ReviewPage, error) {
	out := ReviewPage{Policy: report.Policy, Generation: generation, GapCount: len(report.Gaps), CoverageShape: "coverage", InventoryComplete: report.InventoryComplete, AssessmentComplete: report.AssessmentComplete, ReportComplete: report.ReportComplete, Caveat: report.Caveat, Entries: json.RawMessage("[]")}
	if shape != "review_queue" && shape != "coverage" {
		return out, fmt.Errorf("unknown review shape %q", shape)
	}
	if lane != "" && lane != "correctness" && lane != "maintainability" {
		return out, fmt.Errorf("unknown review lane %q", lane)
	}
	if pageBytes < 4096 || pageBytes > 200000 || limit < 1 || limit > 100 {
		return out, errors.New(pageBoundsInvalid)
	}
	offset := 0
	if cursor != "" {
		var c reviewCursor
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Generation != generation || c.Shape != shape || c.Lane != lane || c.Offset < 0 {
			return out, ErrStaleCursor
		}
		offset = c.Offset
	}
	total := len(report.Files)
	if shape == "coverage" {
		total = len(report.Gaps)
	}
	out.Total = total
	if offset > total {
		return out, ErrStaleCursor
	}
	rows := make([]json.RawMessage, 0, min(limit, total-offset))
	size := 2
	next := offset
	for next < total && len(rows) < limit {
		var entry any
		if shape == "coverage" {
			entry = report.Gaps[next]
		} else {
			entry = report.Files[next]
		}
		b, err := json.Marshal(entry)
		if err != nil {
			return out, err
		}
		if size+len(b)+1 > pageBytes {
			if len(rows) == 0 {
				return out, ErrReviewEntryTooLarge
			}
			break
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
		b, err := json.Marshal(reviewCursor{Generation: generation, Shape: shape, Lane: lane, Offset: next})
		if err != nil {
			return out, err
		}
		out.Next = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}
