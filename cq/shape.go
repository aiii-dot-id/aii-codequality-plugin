package cq

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/token"
	"hash/fnv"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type FileShape struct {
	Lines    int    `json:"lines"`
	TopLevel int    `json:"top_level,omitempty"`
	Test     bool   `json:"test,omitempty"`
	Skipped  string `json:"skipped,omitempty"`

	Funcs   []FuncShape  `json:"funcs,omitempty"`
	Blocks  []BlockShape `json:"blocks,omitempty"`
	Dups    [][3]int     `json:"dups,omitempty"`
	Stubs   []LineNote   `json:"stubs,omitempty"`
	Unreach []int        `json:"unreach,omitempty"`
	Rewrap  []int        `json:"rewrap,omitempty"`

	Tagged    bool          `json:"tagged,omitempty"`
	Decls     []string      `json:"decls,omitempty"`
	Refs      []string      `json:"refs,omitempty"`
	Values    []string      `json:"values,omitempty"`
	Unchecked []Unchecked   `json:"unchecked,omitempty"`
	Outline   []OutlineItem `json:"outline,omitempty"`
}

type FuncShape struct {
	Name    string   `json:"name"`
	Line    int      `json:"line"`
	Depth   int      `json:"depth"`
	Params  int      `json:"params"`
	Method  bool     `json:"method,omitempty"`
	Forward bool     `json:"forward,omitempty"`
	Unused  []string `json:"unused,omitempty"`
	Big     bool     `json:"big,omitempty"`

	ThroughLiteral bool `json:"through_literal,omitempty"`

	Callee string `json:"callee,omitempty"`
}

type Unchecked struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Why  string `json:"why"`
}

const (
	UncheckedLarge   = "a declaration larger than one segment"
	UncheckedNoParse = "it does not parse"

	UncheckedNames = "more names than one record keeps"

	UncheckedShape = "more of its shape than one record keeps"

	UncheckedEarlier = "recorded by an earlier version, which gave no reason; a rescan reads it again"
)

func (u *Unchecked) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		var pair [2]int
		if err := json.Unmarshal(b, &pair); err != nil {
			return err
		}
		*u = Unchecked{From: pair[0], To: pair[1], Why: UncheckedEarlier}
		return nil
	}
	type object Unchecked
	var o object
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return err
	}
	*u = Unchecked(o)
	return nil
}

type BlockShape struct {
	Line  int `json:"line"`
	Depth int `json:"depth"`
}

type LineNote struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type OutlineItem struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Sig   string `json:"sig,omitempty"`
	Lines int    `json:"lines"`
}

const ShapeBattery = "shape"

const shapeMost = 200

const notAnAddress = 0x8000_0000_8000_0000

var stubPattern = regexp.MustCompile(`(^|\s)(//|/\*|#|--)\s*(TODO|FIXME)\b|^\s*\*\s*(TODO|FIXME)\b|(?i:not implemented)|panic\("unimplemented"\)`)

func RulesFor(lang string) []string {
	switch {
	case lang == "go":
		return []string{"SL-DUP", "SL-NEST", "SL-PARAMS", "SL-REWRAP", "SL-STUB", "SL-UNREACH", "SL-FORWARD", "SL-UNUSEDPARAM", "SL-UNUSEDFUNC"}
	case lang == "python" || braceLanguage(lang):
		return []string{"SL-DUP", "SL-NEST", "SL-STUB"}
	}
	return []string{"SL-DUP", "SL-STUB"}
}

var braceLangs = map[string]bool{"javascript": true, "typescript": true, "java": true, "csharp": true, "c": true, "cpp": true,
	"rust": true, "php": true, "kotlin": true, "swift": true, "scala": true, "dart": true, "objc": true, "zig": true, "solidity": true}

func braceLanguage(lang string) bool { return braceLangs[lang] }

type shapeBuilder struct {
	shape  FileShape
	lang   string
	l      *Lang
	norm   []uint64
	normAt []int
	refs   map[string]bool
	values map[string]bool
	cut    bool
	parsed *goParsed
	depth  int

	outlineBytes int
}

func (b *shapeBuilder) stub(seg, line string, start int, spans *[][2]int, lexed *bool) bool {
	for _, m := range stubPattern.FindAllStringSubmatchIndex(line, -1) {
		marker, cont := m[4], m[8]
		if marker < 0 && cont < 0 || b.l == nil {
			return true
		}
		if !*lexed {
			*spans, *lexed = commentSpans(seg, b.l), true
		}
		for _, s := range *spans {
			if marker >= 0 && s[0] == start+marker || cont >= 0 && s[0] < start+cont && start+cont < s[1] {
				return true
			}
		}
	}
	return false
}

func newShapeBuilder(t *Table, lang string) *shapeBuilder {
	return &shapeBuilder{lang: lang, l: t.Langs[lang], refs: map[string]bool{}, values: map[string]bool{}}
}

func (b *shapeBuilder) add(seg string, firstLine int, cut ...bool) {
	b.cut = len(cut) > 0 && cut[0]
	lines := strings.Split(seg, "\n")
	var spans [][2]int
	lexed, at := false, 0
	for i, line := range lines {
		n := firstLine + i
		start := at
		at += len(line) + 1
		if i == len(lines)-1 && line == "" {
			break
		}
		if len(b.shape.Stubs) < shapeMost && b.stub(seg, line, start, &spans, &lexed) {
			b.shape.Stubs = append(b.shape.Stubs, LineNote{Line: n, Text: strings.TrimSpace(clip(line, 120))})
		}
		norm := strings.Join(strings.Fields(line), " ")
		if len(strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return -1
		}, norm)) >= 3 {
			h := fnv.New64a()
			_, _ = h.Write([]byte(norm))
			b.norm, b.normAt = append(b.norm, h.Sum64()|notAnAddress), append(b.normAt, n)
		}
	}
	switch {
	case b.lang == "go":
		b.goSegment(seg, firstLine)
	case b.lang == "python":
		b.pythonSegment(seg, firstLine)
	case braceLanguage(b.lang) && b.l != nil:
		b.braceSegment(seg, firstLine)
	}
}

func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " … (" + itoa(len(s)) + " bytes in all)"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func (b *shapeBuilder) goSegment(seg string, firstLine int) {

	p := b.parsed
	b.parsed = nil
	if p == nil {
		q := parseGoText(seg)
		p = &q
	}
	fset, f, err, shift := p.fset, p.f, p.err, 0
	if p.clause {
		shift = 1
	}
	if err != nil {
		why := UncheckedNoParse
		if b.cut {
			why = UncheckedLarge
		}
		if len(b.shape.Unchecked) < shapeMost {
			b.shape.Unchecked = append(b.shape.Unchecked, Unchecked{From: firstLine, To: firstLine + strings.Count(strings.TrimSuffix(seg, "\n"), "\n"), Why: why})
		}
		return
	}
	defer forget(f)
	line := func(p token.Pos) int { return fset.Position(p).Line - shift + firstLine - 1 }
	calls := map[*ast.Ident]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				calls[id] = true
			}
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body != nil {
				ast.Inspect(n.Body, func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok && !calls[id] {
						b.values[id.Name] = true
					}
					return true
				})
			}
			return false
		case *ast.ValueSpec:
			for _, v := range n.Values {
				ast.Inspect(v, func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok && !calls[id] {
						b.values[id.Name] = true
					}
					return true
				})
			}
		}
		return true
	})
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if fields := strings.Fields(c.Text); len(fields) >= 2 && fields[0] == "//go:linkname" {
				for _, name := range fields[1:] {
					b.refs[name[strings.LastIndexByte(name, '.')+1:]] = true
				}
			}
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			b.goFunc(d, line, seg, fset, shift)
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok {
					kind := "type"
					switch ts.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					b.outline(OutlineItem{Kind: kind, Name: ts.Name.Name, Lines: line(ts.End()) - line(ts.Pos()) + 1})
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:

			if n.Recv != nil {
				ast.Inspect(n.Recv, func(m ast.Node) bool { b.ref(m); return true })
			}
			ast.Inspect(n.Type, func(m ast.Node) bool { b.ref(m); return true })
			if n.Body != nil {
				ast.Inspect(n.Body, func(m ast.Node) bool { b.ref(m); return true })
			}
			return false
		}
		b.ref(n)
		return true
	})
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			b.unreachable(fd.Body, line)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok && rewrap(c) && len(b.shape.Rewrap) < shapeMost {
					b.shape.Rewrap = append(b.shape.Rewrap, line(c.Pos()))
				}
				return true
			})
		}
	}
}

func (b *shapeBuilder) ref(n ast.Node) {
	if id, ok := n.(*ast.Ident); ok {
		b.refs[id.Name] = true
	}
}

func (b *shapeBuilder) outline(o OutlineItem) {
	o.Name = clip(o.Name, 120)
	n := len(o.Kind) + len(o.Name) + len(o.Sig) + 48
	if len(b.shape.Outline) < shapeMost && b.outlineBytes+n <= outlineMost {
		b.shape.Outline = append(b.shape.Outline, o)
		b.outlineBytes += n
	}
}

const outlineMost = 32 << 10

func (b *shapeBuilder) goFunc(d *ast.FuncDecl, line func(token.Pos) int, seg string, fset *token.FileSet, shift int) {
	fs := FuncShape{Name: d.Name.Name, Line: line(d.Pos()), Method: d.Recv != nil}
	var names []string
	if d.Type.Params != nil {
		for _, p := range d.Type.Params.List {
			if len(p.Names) == 0 {
				fs.Params++
				continue
			}
			for _, n := range p.Names {
				fs.Params++
				names = append(names, n.Name)
			}
		}
	}
	if d.Body != nil {
		fs.Depth, fs.ThroughLiteral = nestDepth(d.Body)
		used := map[string]bool{}
		ast.Inspect(d.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				used[id.Name] = true
			}
			return true
		})
		for _, n := range names {
			if n != "_" && !used[n] {
				fs.Unused = append(fs.Unused, n)
			}
		}
		fs.Callee = forwards(d, names)
		fs.Forward = fs.Callee != ""
	}
	if len(b.shape.Funcs) < shapeMost*5 {
		b.shape.Funcs = append(b.shape.Funcs, fs)
	}
	if !fs.Method && d.Name.Name != "init" && d.Name.Name != "main" && d.Name.Name != "_" && !ast.IsExported(d.Name.Name) {
		b.shape.Decls = append(b.shape.Decls, d.Name.Name+":"+itoa(fs.Line))
	}
	kind := "func"
	if fs.Method {
		kind = "method"
	}
	head := ""
	if p := fset.Position(d.Pos()).Offset - len(segmentClause)*shift; p >= 0 && p < len(seg) {
		head = seg[p:]
		if i := strings.IndexAny(head, "{\n"); i >= 0 {
			head = head[:i]
		}
		head = clip(strings.TrimSpace(head), 160)
	}
	b.outline(OutlineItem{Kind: kind, Name: d.Name.Name, Sig: head, Lines: line(d.End()) - fs.Line + 1})
}

func nestDepth(body *ast.BlockStmt) (int, bool) {
	most, viaLit := 0, false
	reach := func(d int, lit bool) {
		if d > most {
			most, viaLit = d, lit
		} else if d == most && lit {
			viaLit = true
		}
	}
	var walk func(n ast.Node, d int, lit bool)
	var chain func(x *ast.IfStmt, d int, lit bool)
	chain = func(x *ast.IfStmt, d int, lit bool) {
		reach(d+1, lit)
		if x.Init != nil {
			walk(x.Init, d+1, lit)
		}
		walk(x.Cond, d+1, lit)
		walk(x.Body, d+1, lit)
		switch e := x.Else.(type) {
		case *ast.IfStmt:
			chain(e, d, lit)
		case *ast.BlockStmt:
			walk(e, d+1, lit)
		}
	}
	walk = func(n ast.Node, d int, lit bool) {
		ast.Inspect(n, func(m ast.Node) bool {
			if m == n {
				return true
			}
			switch m := m.(type) {
			case *ast.IfStmt:
				chain(m, d, lit)
				return false
			case *ast.FuncLit:
				reach(d+1, true)
				walk(m.Body, d+1, true)
				return false
			case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				reach(d+1, lit)
				walk(m, d+1, lit)
				return false
			}
			return true
		})
	}
	walk(body, 0, false)
	return most, viaLit
}

func forwards(d *ast.FuncDecl, params []string) string {
	if len(d.Body.List) != 1 || len(params) == 0 {
		return ""
	}
	var call *ast.CallExpr
	switch s := d.Body.List[0].(type) {
	case *ast.ReturnStmt:
		if len(s.Results) == 1 {
			call, _ = s.Results[0].(*ast.CallExpr)
		}
	case *ast.ExprStmt:
		call, _ = s.X.(*ast.CallExpr)
	}
	if call == nil || len(call.Args) != len(params) {
		return ""
	}
	for i, a := range call.Args {
		id, ok := a.(*ast.Ident)
		if !ok || id.Name != params[i] {
			return ""
		}
	}
	return exprText(call.Fun)
}

func rewrap(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch {
	case pkg.Name == "fmt" && sel.Sel.Name == "Errorf" && len(c.Args) == 2:
		lit, ok := c.Args[0].(*ast.BasicLit)
		return ok && (lit.Value == `"%w"` || lit.Value == `"%v"` || lit.Value == `"%s"`)
	case pkg.Name == "errors" && sel.Sel.Name == "New" && len(c.Args) == 1:
		inner, ok := c.Args[0].(*ast.CallExpr)
		if !ok || len(inner.Args) != 0 {
			return false
		}
		s, ok := inner.Fun.(*ast.SelectorExpr)
		return ok && s.Sel.Name == "Error"
	}
	return false
}

func (b *shapeBuilder) unreachable(body *ast.BlockStmt, line func(token.Pos) int) {
	ast.Inspect(body, func(n ast.Node) bool {
		var list []ast.Stmt
		switch n := n.(type) {
		case *ast.BlockStmt:
			list = n.List
		case *ast.CaseClause:
			list = n.Body
		case *ast.CommClause:
			list = n.Body
		}
		for i := 0; i+1 < len(list); i++ {
			if terminates(list[i]) {
				if _, labelled := list[i+1].(*ast.LabeledStmt); !labelled && len(b.shape.Unreach) < shapeMost {
					b.shape.Unreach = append(b.shape.Unreach, line(list[i+1].Pos()))
				}
				break
			}
		}
		return true
	})
}

func terminates(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.GOTO || s.Tok == token.BREAK || s.Tok == token.CONTINUE
	case *ast.ExprStmt:
		c, ok := s.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "panic" {
			return true
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && sel.Sel.Name == "Exit" {
				return true
			}
		}
	}
	return false
}

func (b *shapeBuilder) braceSegment(seg string, firstLine int) {
	tops, depths, end := lineTops(seg, b.l, b.depth)
	start, deepest := firstLine, 0
	for k, at := range tops {
		if at >= len(seg) {
			break
		}
		n := firstLine + strings.Count(seg[:at], "\n")
		d := depths[k]
		if d == 0 {
			if deepest > 1 && len(b.shape.Blocks) < shapeMost {
				b.shape.Blocks = append(b.shape.Blocks, BlockShape{Line: start, Depth: deepest - 1})
			}
			start, deepest = n, 0
		}
		deepest = max(deepest, d)
	}
	if deepest > 1 && len(b.shape.Blocks) < shapeMost {
		b.shape.Blocks = append(b.shape.Blocks, BlockShape{Line: start, Depth: deepest - 1})
	}
	b.depth = end
}

func (b *shapeBuilder) pythonSegment(seg string, firstLine int) {
	start, deepest := 0, 0
	flush := func() {
		if start > 0 && deepest > 1 && len(b.shape.Blocks) < shapeMost {
			b.shape.Blocks = append(b.shape.Blocks, BlockShape{Line: start, Depth: deepest - 1})
		}
	}
	for i, line := range strings.Split(seg, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		level := strings.Count(line[:indent], "\t") + (indent-strings.Count(line[:indent], "\t"))/4
		if level == 0 {
			flush()
			start, deepest = 0, 0
			if strings.HasPrefix(line, "def ") || strings.HasPrefix(line, "class ") || strings.HasPrefix(line, "async def ") {
				start = firstLine + i
			}
			continue
		}
		deepest = max(deepest, level)
	}
	flush()
}

func (b *shapeBuilder) finish(lines, top int, test bool) FileShape {
	s := b.shape
	s.Lines, s.TopLevel, s.Test = lines, top, test
	s.Dups = duplicates(b.norm, b.normAt)
	if b.lang == "go" {
		for k := range b.refs {
			s.Refs = append(s.Refs, k)
		}
		sort.Strings(s.Refs)
		for k := range b.values {
			s.Values = append(s.Values, k)
		}
		sort.Strings(s.Values)
		if namesBytes(s) > namesMost {
			s.dropNames(lines)
		}
	}
	s.fit(lines)
	return s
}

func namesBytes(s FileShape) int {
	n := 0
	for _, l := range [][]string{s.Decls, s.Refs, s.Values} {
		for _, name := range l {
			n += len(name) + 3
		}
	}
	for _, f := range s.Funcs {
		for _, name := range f.Unused {
			n += len(name) + 3
		}
	}
	return n
}

func (s *FileShape) dropNames(lines int) {
	s.Decls, s.Refs, s.Values = nil, nil, nil
	funcs := make([]FuncShape, len(s.Funcs))
	for i, f := range s.Funcs {
		f.Unused = nil
		funcs[i] = f
	}
	s.Funcs = funcs
	if len(s.Unchecked) < shapeMost {
		s.Unchecked = append(s.Unchecked, Unchecked{From: 1, To: max(lines, 1), Why: UncheckedNames})
	}
}

func (s *FileShape) fit(lines int) {
	if shapeBytes(*s) <= shapeEventMost {
		return
	}
	if s.Decls != nil || s.Refs != nil || s.Values != nil || namesBytes(*s) > 0 {
		s.dropNames(lines)
		if shapeBytes(*s) <= shapeEventMost {
			return
		}
	}
	*s = FileShape{Lines: s.Lines, TopLevel: s.TopLevel, Test: s.Test, Skipped: s.Skipped, Tagged: s.Tagged,
		Unchecked: []Unchecked{{From: 1, To: max(lines, 1), Why: UncheckedShape}}}
}

func shapeBytes(s FileShape) int {
	b, _ := json.Marshal(s)
	return len(b)
}

const shapeEventMost = 200 << 10

const namesMost = 96 << 10

func duplicates(h []uint64, at []int) [][3]int {
	const k = 3
	var out [][3]int
	first := map[uint64]int{}
	covered := make([]bool, len(h))
	key := func(i int) uint64 {
		x := uint64(1469598103934665603)
		for j := 0; j < k; j++ {
			x = (x ^ h[i+j]) * 1099511628211
		}
		return x
	}
	for j := 0; j+k <= len(h); j++ {
		if covered[j] {
			continue
		}
		g := key(j)
		i, seen := first[g]
		if !seen {
			first[g] = j
			continue
		}
		if i+k > j {
			continue
		}
		n := 0
		for i+n < j && j+n < len(h) && h[i+n] == h[j+n] {
			n++
		}
		if n < k {
			continue
		}
		for m := j; m < j+n; m++ {
			covered[m] = true
		}
		if len(out) < shapeMost {
			out = append(out, [3]int{at[i], at[j], n})
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func exprText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if left := exprText(x.X); left != "" {
			return left + "." + x.Sel.Name
		}
		return x.Sel.Name
	}
	return "(call)"
}

func sameNameWrapper(fn FuncShape) bool {
	last := fn.Callee
	if i := strings.LastIndexByte(last, '.'); i >= 0 {
		last = last[i+1:]
	}
	return fn.Forward && last != fn.Name && strings.EqualFold(last, fn.Name)
}

func platformTagged(name, text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if expr, ok := strings.CutPrefix(line, "//go:build "); ok && namesPlatform(expr) {
			return true
		}
		if expr, ok := strings.CutPrefix(line, "// +build "); ok && namesPlatform(expr) {
			return true
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
	}

	base := strings.TrimSuffix(path.Base(name), ".go")
	i := strings.IndexByte(base, '_')
	if i < 0 {
		return false
	}
	l := strings.Split(base[i:], "_")
	if n := len(l); n > 0 && l[n-1] == "test" {
		l = l[:n-1]
	}
	n := len(l)
	return n >= 2 && knownOS[l[n-2]] && knownArch[l[n-1]] || n >= 1 && (knownOS[l[n-1]] || knownArch[l[n-1]])
}

func namesPlatform(expr string) bool {
	for _, tag := range strings.FieldsFunc(expr, func(r rune) bool {
		return !(r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		if knownOS[tag] || knownArch[tag] || tag == "unix" {
			return true
		}
	}
	return false
}

var (
	knownOS = map[string]bool{"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "hurd": true, "illumos": true,
		"ios": true, "js": true, "linux": true, "nacl": true, "netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
		"windows": true, "zos": true}
	knownArch = map[string]bool{"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true, "arm64be": true,
		"loong64": true, "mips": true, "mipsle": true, "mips64": true, "mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true,
		"ppc64": true, "ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true, "sparc": true, "sparc64": true, "wasm": true}
)
