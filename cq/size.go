package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

const SizeBattery = "file_size"

func sideRecord(r Record) bool {
	return r.Battery == IntentBattery || r.Battery == SizeBattery || r.Battery == ShapeBattery || r.Battery == SlopBattery || r.Battery == OutlineBattery
}

func ShapeRecord(ref, digest string, shape FileShape) Record {
	id := sha256.Sum256([]byte(ref + "\x00" + digest + "\x00" + ShapeBattery))
	return Record{Version: RecordVersion, ID: hex.EncodeToString(id[:]), Ref: ref, SourceDigest: digest, Battery: ShapeBattery, Shape: &shape}
}

type FileSize struct {
	Lines    int    `json:"lines"`
	TopLevel *int   `json:"top_level_decls,omitempty"`
	Finding  string `json:"finding,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type Thresholds struct {
	Large, VeryLarge, DupLines, Nesting, Params int

	ListAll bool
}

type SizeThresholds = Thresholds

var thresholdKeys = []string{"large_file_lines", "very_large_file_lines", "dup_lines", "nesting", "params"}

var DefaultThresholds = Thresholds{Large: 1000, VeryLarge: 2000, DupLines: 6, Nesting: 4, Params: 6}

func (th Thresholds) orDefault() Thresholds {
	d := DefaultThresholds
	for _, p := range [][2]*int{{&th.Large, &d.Large}, {&th.VeryLarge, &d.VeryLarge}, {&th.DupLines, &d.DupLines}, {&th.Nesting, &d.Nesting}, {&th.Params, &d.Params}} {
		if *p[0] <= 0 {
			*p[0] = *p[1]
		}
	}
	return th
}

func (th Thresholds) key() [5]int {
	th = th.orDefault()
	return [5]int{th.Large, th.VeryLarge, th.DupLines, th.Nesting, th.Params}
}

func (th Thresholds) Effective() map[string]int {
	th = th.orDefault()
	return map[string]int{"large_file_lines": th.Large, "very_large_file_lines": th.VeryLarge, "dup_lines": th.DupLines, "nesting": th.Nesting, "params": th.Params}
}

func ParseThresholds(s string) (th Thresholds, badKey, why string) {
	th = DefaultThresholds
	seen := map[string]bool{}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' })
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return th, f, "is not key=value"
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000000 {
			return th, k, "is not a whole number from 1 to 1000000"
		}
		if seen[k] {
			return th, k, "is given twice"
		}
		seen[k] = true
		switch k {
		case "large_file_lines":
			th.Large = n
		case "very_large_file_lines":
			th.VeryLarge = n
		case "dup_lines":
			th.DupLines = n
		case "nesting":
			th.Nesting = n
		case "params":
			th.Params = n
		default:
			return th, k, "is not a threshold: " + strings.Join(thresholdKeys, ", ")
		}
	}
	return th, "", ""
}

func (th SizeThresholds) mark(f *FileReview) { th.markWith(f, nil) }

func (th SizeThresholds) markWith(f *FileReview, pkg map[string][]SlopFinding) {
	th.unlist(f)
	th.markSlop(f, pkg)
	if f.Size == nil {
		return
	}
	th = th.orDefault()
	f.Size.Finding, f.Size.Severity = "", ""
	switch {
	case f.Size.Lines > th.VeryLarge:
		f.Size.Finding, f.Size.Severity = "very_large_file", "high"
	case f.Size.Lines > th.Large:
		f.Size.Finding, f.Size.Severity = "large_file", "moderate"
	}
}

func CountLines(text string) int {
	n := strings.Count(text, "\n")
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

func TopLevelDecls(source string, l *Lang, depth0 int) (n, depth int) {
	if l == nil || l.Key != "go" {
		return 0, depth0
	}
	tops, depths, depth := lineTops(source, l, depth0)
	starts := append([]int{0}, tops...)
	ds := append([]int{depth0}, depths...)
	for k, at := range starts {
		if at >= len(source) || ds[k] != 0 {
			continue
		}
		line := source[at:]
		if strings.HasPrefix(line, "func ") || strings.HasPrefix(line, "func(") || strings.HasPrefix(line, "type ") {
			n++
		}
	}
	return n, depth
}

func SizeRecord(ref, digest string, lines, top int, goSource bool) Record {
	id := sha256.Sum256([]byte(ref + "\x00" + digest + "\x00" + SizeBattery))
	r := Record{Version: RecordVersion, ID: hex.EncodeToString(id[:]), Ref: ref, SourceDigest: digest, Battery: SizeBattery,
		Lines: [2]int{1, lines}, Answers: map[string]float64{}}
	if goSource {
		r.Answers["top_level_decls"] = float64(top)
	}
	return r
}

func sizeRecorded(r Record) (FileSize, bool) {
	switch {
	case r.Battery == SizeBattery:
		return sizeOf(r), true
	case r.Battery == ShapeBattery && r.Shape != nil && r.Shape.Skipped == "" && !r.Shape.Test:
		size := FileSize{Lines: r.Shape.Lines}
		if r.Shape.TopLevel > 0 || len(r.Shape.Funcs) > 0 || len(r.Shape.Refs) > 0 {
			n := r.Shape.TopLevel
			size.TopLevel = &n
		}
		return size, true
	}
	return FileSize{}, false
}

func sizeOf(r Record) FileSize {
	s := FileSize{Lines: r.Lines[1]}
	if v, ok := r.Answers["top_level_decls"]; ok {
		n := int(v)
		s.TopLevel = &n
	}
	return s
}
