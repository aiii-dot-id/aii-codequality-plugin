package cq

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

type FuncMeasure struct {
	Name   string `json:"name"`
	Line   int    `json:"line"`
	Lines  int    `json:"lines"`
	Depth  int    `json:"depth"`
	Params int    `json:"params"`

	ThroughLiteral bool `json:"through_literal,omitempty"`
}

type FileMeasure struct {
	Lines int           `json:"lines"`
	Types int           `json:"types,omitempty"`
	Funcs []FuncMeasure `json:"funcs,omitempty"`
	Err   string        `json:"err,omitempty"`
}

func MeasureGo(source string) FileMeasure {
	m := FileMeasure{Lines: CountLines(source)}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", sourceBytes(source), parser.ParseComments|parser.SkipObjectResolution)
	defer forget(f)
	if err != nil {
		m.Err = noParseWhy(source, err)
		return m
	}

	line := func(pos token.Pos) int { return fset.PositionFor(pos, false).Line }
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				m.Types += len(d.Specs)
			}
		case *ast.FuncDecl:
			m.Funcs = append(m.Funcs, measureFunc(d, line))
		}
	}
	return m
}

const (
	noParseNoClause     = "no package clause"
	noParseUnexpected   = "unexpected token"
	noParseUnterminated = "unterminated literal or comment"
	noParseIllegal      = "illegal character"
	noParseOther        = "syntax error"
)

func noParseWhy(source string, err error) string {
	var list scanner.ErrorList
	if !errors.As(err, &list) || len(list) == 0 {
		return UncheckedNoParse
	}
	first := list[0]
	for _, e := range list[1:] {
		sooner := e.Pos.Offset < first.Pos.Offset
		sharper := e.Pos.Offset == first.Pos.Offset && noParseKind(first.Msg) == noParseUnexpected && noParseKind(e.Msg) != noParseUnexpected
		if sooner || sharper {
			first = e
		}
	}
	why := UncheckedNoParse + ": "
	if off := first.Pos.Offset; off >= 0 && off <= len(source) {
		why += "line " + itoa(strings.Count(source[:off], "\n")+1) + ": "
	}
	return why + noParseKind(first.Msg)
}

func noParseKind(msg string) string {
	switch {
	case strings.HasPrefix(msg, "expected 'package'"):
		return noParseNoClause
	case strings.HasPrefix(msg, "expected "):
		return noParseUnexpected
	case strings.Contains(msg, "not terminated"):
		return noParseUnterminated
	case strings.HasPrefix(msg, "illegal "):
		return noParseIllegal
	}
	return noParseOther
}

func measureFunc(d *ast.FuncDecl, line func(token.Pos) int) FuncMeasure {
	at := line(d.Pos())
	fm := FuncMeasure{Name: d.Name.Name, Line: at, Lines: line(d.End()) - at + 1}
	if r := receiverName(d); r != "" {
		fm.Name = r + "." + fm.Name
	}
	if d.Type.Params != nil {
		for _, f := range d.Type.Params.List {
			fm.Params += max(len(f.Names), 1)
		}
	}
	if d.Body != nil {
		fm.Depth, fm.ThroughLiteral = nestDepth(d.Body)
	}
	return fm
}

func receiverName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	e := d.Recv.List[0].Type
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

type RankedFunc struct {
	File string `json:"file"`
	FuncMeasure
}

func RankFuncs(files map[string]FileMeasure) []RankedFunc {
	var out []RankedFunc
	for file, m := range files {
		for _, f := range m.Funcs {
			out = append(out, RankedFunc{File: file, FuncMeasure: f})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Lines != b.Lines {
			return a.Lines > b.Lines
		}
		if a.Depth != b.Depth {
			return a.Depth > b.Depth
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return out
}
