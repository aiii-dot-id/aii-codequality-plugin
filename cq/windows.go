package cq

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"unicode/utf8"
)

type SourceWindow struct {
	Code          Chunk
	Context       Chunk
	DocItems      []Item
	NonApplicable []string
	SplitUnit     bool
	Boundary      string
	Redacted      int
}

//go:noinline
func ContextWindows(t *Table, lang, source string, size int) ([]SourceWindow, error) {
	return contextWindowsAt(t, lang, source, size, 1, false)
}

//go:noinline
func contextWindowsAt(t *Table, lang, source string, size, firstLine int, limitedStart bool) ([]SourceWindow, error) {
	return contextWindowsWith(t, lang, source, size, firstLine, limitedStart, nil)
}

type SegmentUnits struct {
	Cuts     []int  `json:"cuts"`
	Boundary string `json:"boundary"`
}

const unitGap = 256

func (u *SegmentUnits) thin() *SegmentUnits {
	if u == nil || len(u.Cuts) < 3 {
		return u
	}
	cuts := []int{u.Cuts[0]}
	for _, c := range u.Cuts[1 : len(u.Cuts)-1] {
		if c-cuts[len(cuts)-1] >= unitGap {
			cuts = append(cuts, c)
		}
	}
	cuts = append(cuts, u.Cuts[len(u.Cuts)-1])
	return &SegmentUnits{Cuts: cuts, Boundary: u.Boundary}
}

func unitsOf(source, lang string) *SegmentUnits {
	cuts, boundary := windowUnits(source, lang)
	return &SegmentUnits{Cuts: cuts, Boundary: boundary}
}

func contextWindowsWith(t *Table, lang, source string, size, firstLine int, limitedStart bool, units *SegmentUnits) ([]SourceWindow, error) {
	if t == nil || t.Langs[lang] == nil {
		return nil, fmt.Errorf("context windows: unknown language %q", lang)
	}
	if size <= 0 {
		size = ChunkBytes
	}
	if size < utf8.UTFMax {
		return nil, fmt.Errorf("context windows: size must be at least %d bytes", utf8.UTFMax)
	}
	if len(source) > 1<<20 {
		return nil, fmt.Errorf("context windows: source exceeds 1 MiB")
	}
	if !utf8.ValidString(source) {
		return nil, fmt.Errorf("context windows: invalid UTF-8")
	}
	if source == "" {
		return nil, nil
	}
	code, comments, lost := windowBlank(source, t.Langs[lang])
	if lang == "markdown" {
		code = []byte(source)
		for i := range code {
			if code[i] != '\n' && code[i] != '\r' {
				code[i] = ' '
			}
		}
		comments = nil
	}
	context, secrets := windowRedact(source)
	sort.Ints(secrets)
	windowRedactBytes(code)
	if units == nil {
		units = unitsOf(source, lang)
	}
	cuts, boundary := units.Cuts, units.Boundary

	limited := lost || boundary == "go_parse_fallback" || limitedStart
	var out []SourceWindow
	line := firstLine
	commentIndex, secretIndex := 0, 0
	for u := 0; u+1 < len(cuts); u++ {
		start, end := cuts[u], cuts[u+1]
		for u+2 < len(cuts) && cuts[u+2]-start <= size {
			u++
			end = cuts[u+1]
		}
		split := end-start > size
		for start < end {
			stop := end
			if stop-start > size {
				stop = start + size
				for stop > start && !utf8.RuneStart(context[stop]) {
					stop--
				}

				if n := strings.LastIndexByte(context[start:stop], '\n'); n >= 0 {
					stop = start + n + 1
				}
			}
			body := context[start:stop]
			last := line + strings.Count(body, "\n")
			if strings.HasSuffix(body, "\n") {
				last--
			}
			w := SourceWindow{Code: Chunk{string(code[start:stop]), line, last}, Context: Chunk{body, line, last}, Boundary: boundary, SplitUnit: split || limited}
			if split {
				w.Boundary += "/split"
			}
			for commentIndex < len(comments) && comments[commentIndex][1] <= start {
				commentIndex++
			}
			hasComment := commentIndex < len(comments) && comments[commentIndex][0] < stop
			for secretIndex < len(secrets) && secrets[secretIndex] < stop {
				w.Redacted++
				secretIndex++
			}
			for _, it := range DocItems() {

				applies := true
				if lang == "markdown" {
					applies = it.ID == "DC-01" || it.ID == "GC-02"
				} else if it.ID == "DC-03" || it.ID == "DC-04" {
					applies = hasComment
				}
				if applies {
					w.DocItems = append(w.DocItems, it)
				} else {
					w.NonApplicable = append(w.NonApplicable, it.ID)
				}
			}
			out = append(out, w)
			line += strings.Count(body, "\n")
			start = stop
		}
	}
	return out, nil
}

//go:noinline
func windowUnits(source, lang string) ([]int, string) {
	if lang == "markdown" {
		return []int{0, len(source)}, "markdown"
	}
	if lang != "go" {
		return []int{0, len(source)}, "lexical"
	}
	p := parseGoText(source)
	if p.err != nil {
		return []int{0, len(source)}, "go_parse_fallback"
	}
	defer forget(p.f)
	return goCuts(p, source), "go_declaration"
}

const segmentClause = "package segment\n"

type goParsed struct {
	fset   *token.FileSet
	f      *ast.File
	clause bool
	err    error
}

func parseGoText(source string) goParsed {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", sourceBytes(source), parser.ParseComments|parser.SkipObjectResolution)
	if err == nil {
		return goParsed{fset: fset, f: f}
	}
	if strings.HasPrefix(strings.TrimLeft(source, " \t\r\n"), "package ") {
		return goParsed{err: err}
	}
	fset = token.NewFileSet()
	f, err = parser.ParseFile(fset, "", segmentClause+source, parser.ParseComments|parser.SkipObjectResolution)
	return goParsed{fset: fset, f: f, clause: true, err: err}
}

func goCuts(p goParsed, source string) []int {
	fs, f, shift := p.fset, p.f, 0
	if p.clause {
		shift = len(segmentClause)
	}
	cuts := []int{0}
	previousEndLine := 0
	for i, d := range f.Decls {
		pos := d.Pos()
		var doc *ast.CommentGroup
		switch d := d.(type) {
		case *ast.FuncDecl:
			doc = d.Doc
		case *ast.GenDecl:
			doc = d.Doc
		}
		if doc != nil {
			pos = doc.Pos()
		}
		p := fs.PositionFor(pos, false)
		if i > 0 && p.Line > previousEndLine && p.Offset-shift > 0 {
			off := strings.LastIndexByte(source[:p.Offset-shift], '\n') + 1
			if off > cuts[len(cuts)-1] {
				cuts = append(cuts, off)
			}
		}
		previousEndLine = fs.PositionFor(d.End(), false).Line
	}
	return append(cuts, len(source))
}

//go:noinline
func windowRedact(source string) (string, []int) {
	var b []byte
	var starts []int
	for _, p := range secretPatterns {

		var matches [][]int
		if b == nil {
			matches = p.FindAllStringIndex(source, -1)
		} else {
			matches = p.FindAllIndex(b, -1)
		}
		for _, m := range matches {
			if b == nil {
				b = []byte(source)
			}
			starts = append(starts, m[0])
			for i := m[0]; i < m[1]; i++ {
				if b[i] != '\n' && b[i] != '\r' {
					b[i] = '#'
				}
			}
		}
	}
	if b == nil {
		return source, starts
	}
	return string(b), starts
}

func windowRedactBytes(b []byte) {
	for _, p := range secretPatterns {
		for _, m := range p.FindAllIndex(b, -1) {
			for i := m[0]; i < m[1]; i++ {
				if b[i] != '\n' && b[i] != '\r' {
					b[i] = '#'
				}
			}
		}
	}
}

//go:noinline
func windowBlank(source string, l *Lang) ([]byte, [][2]int, bool) {
	b := []byte(source)
	spans, lost := lexComments(source, l, b)
	return b, spans, lost
}

func commentSpans(source string, l *Lang) [][2]int {
	spans, _ := lexComments(source, l, nil)
	return spans
}

func lexComments(source string, l *Lang, b []byte) ([][2]int, bool) {
	var spans [][2]int
	lost := false
	mask := func(start, end int) {
		spans = append(spans, [2]int{start, end})
		for k := start; b != nil && k < end; k++ {
			if b[k] != '\n' && b[k] != '\r' {
				b[k] = ' '
			}
		}
	}
	quote := byte(0)
	raw := false
	for i := 0; i < len(source); {
		c := source[i]
		if quote != 0 {
			if c == '\\' && !raw && i+1 < len(source) {
				i += 2
				continue
			}
			if c == quote {
				quote = 0
			} else if c == '\n' && quote != '`' && l.Key != "python" {
				lost = true
				quote = 0
			}
			i++
			continue
		}
		if l.Key == "python" && (strings.HasPrefix(source[i:], `"""`) || strings.HasPrefix(source[i:], `'''`)) {
			q := source[i : i+3]
			n := strings.Index(source[i+3:], q)
			if n < 0 {
				lost = true
				break
			}
			end := i + 3 + n + 3
			mask(i, end)
			i = end
			continue
		}
		matched := false
		if l.Key != "python" {
			for _, pair := range l.Blocks {
				if pair[0] == "" || pair[1] == "" || !strings.HasPrefix(source[i:], pair[0]) {
					continue
				}
				n := strings.Index(source[i+len(pair[0]):], pair[1])
				end := len(source)
				if n < 0 {
					lost = true
				} else {
					end = i + len(pair[0]) + n + len(pair[1])
				}
				mask(i, end)
				i = end
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if l.LineComment != "" && strings.HasPrefix(source[i:], l.LineComment) {
			n := strings.IndexByte(source[i:], '\n')
			if n < 0 {
				n = len(source) - i
			}
			mask(i, i+n)
			i += n
			continue
		}
		if (c == '`' && !backtickStrings[l.Key]) || (noStrings[l.Key] && (c == '"' || c == '\'')) {
			i++
			continue
		}
		if c == '\'' && charLiterals[l.Key] {
			n := charLiteral(source[i:])
			if n == 0 {
				n = 1
			}
			i += n
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			quote = c
			raw = c == '`' || l.Key == "python" && i > 0 && (source[i-1] == 'r' || source[i-1] == 'R')
		}
		i++
	}
	return spans, lost || quote != 0
}
