package cq

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestMeasureGoPhysicalLines(t *testing.T) {
	cases := []struct {
		name, src string
		want      []FuncMeasure
	}{
		{"a directive inside a declaration that resets backward",
			"package p\n//line other.go:1000\nfunc f() {\n//line other.go:1\n}\n",
			[]FuncMeasure{{Name: "f", Line: 3, Lines: 3}}},
		{"a directive before a declaration and a block directive inside the next",
			"package p\n\n//line gen.go:500\nfunc a() {\n\treturn\n}\n\nfunc b() { /*line gen.go:7:1*/ return\n}\n",
			[]FuncMeasure{{Name: "a", Line: 4, Lines: 3}, {Name: "b", Line: 8, Lines: 2}}},
	}
	for _, c := range cases {
		got := MeasureGo(c.src)
		if got.Err != "" || !reflect.DeepEqual(got.Funcs, c.want) {
			t.Errorf("%s: err %q got %+v want %+v", c.name, got.Err, got.Funcs, c.want)
		}
	}
}

func TestMeasureGoThroughLiteralOnATie(t *testing.T) {
	cases := []struct {
		name, body string
		depth      int
		lit        bool
	}{
		{"tie_literal_second", "if x {}; _ = func() {}", 1, true},
		{"tie_literal_first", "_ = func() {}; if x {}", 1, true},
		{"deeper_outside_the_literal", "if x { if x {} }; _ = func() {}", 2, false},
		{"deeper_inside_the_literal", "if x {}; _ = func() { if x {} }", 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MeasureGo("package p\n\nfunc f(x bool) { " + c.body + " }\n")
			if got.Err != "" || len(got.Funcs) != 1 {
				t.Fatalf("err %q, %d functions; want none and 1", got.Err, len(got.Funcs))
			}
			if f := got.Funcs[0]; f.Depth != c.depth || f.ThroughLiteral != c.lit {
				t.Fatalf("depth %d through a literal %v; want %d and %v", f.Depth, f.ThroughLiteral, c.depth, c.lit)
			}
		})
	}
}

func TestMeasureGoIsAWholeFileOnly(t *testing.T) {
	noClause := MeasureGo("func f() {}\n")
	if noClause.Lines != 1 || len(noClause.Funcs) != 0 {
		t.Fatalf("a text with no package clause: got %+v; want 1 line and no function", noClause)
	}
	if want := UncheckedNoParse + ": line 1: no package clause"; noClause.Err != want {
		t.Fatalf("a text with no package clause: err %q; want %q", noClause.Err, want)
	}
	syntax := MeasureGo("package p\n\nfunc (\n")
	if !strings.HasPrefix(syntax.Err, UncheckedNoParse+": line ") || !strings.HasSuffix(syntax.Err, ": unexpected token") {
		t.Fatalf("a syntax error: err %q; want a line and an unexpected token", syntax.Err)
	}
}

func TestMeasureGoNoParseLineIsPhysical(t *testing.T) {
	got := MeasureGo("package p\n//line x.go:900\nvar a = 1 2\n")
	if want := UncheckedNoParse + ": line 3: "; !strings.HasPrefix(got.Err, want) {
		t.Fatalf("err %q; want it to begin %q", got.Err, want)
	}
}

func TestMeasureGoNoParseCarriesNoSource(t *testing.T) {
	generic := strings.Repeat("qz", 8)
	inContext := "token = \"" + generic + "\""
	if _, n := Redact(inContext); n != 1 {
		t.Fatalf("the fixture is not a value Redact knows by its key (%d replaced)", n)
	}
	if _, n := Redact("\"" + generic + "\""); n != 0 {
		t.Fatalf("the fixture is a value Redact knows without its key (%d replaced)", n)
	}
	shaped := "AKIA" + "IOSFODNN7EXAMPLE"
	if _, n := Redact(shaped); n != 1 {
		t.Fatalf("the fixture is not a value Redact knows by its shape (%d replaced)", n)
	}
	want := UncheckedNoParse + ": line 3: unexpected token"
	cases := []struct{ name, src, value string }{
		{"a value known only by its key", "package p\n\ntype " + inContext + "\n", generic},
		{"a value known by its shape", "package p\n\nvar a = 1 \"" + shaped + "\"\n", shaped},
	}
	for _, c := range cases {
		if got := MeasureGo(c.src).Err; got != want {
			t.Errorf("%s: err is not %q (it has %d bytes, the value in it: %v)", c.name, want, len(got), strings.Contains(got, c.value))
		}
	}
}

func TestMeasureGoNoParseIsLineAndKind(t *testing.T) {
	form := regexp.MustCompile(`^it does not parse: line [0-9]+: (no package clause|unexpected token|unterminated literal or comment|illegal character|syntax error)$`)
	marker := "zqxjkvzqxjkv"
	cases := []struct{ name, src, kind string }{
		{"an identifier the parser names", "package p\n\nvar a = 1 " + marker + "\n", "unexpected token"},
		{"a digit its literal has no place for", "package p\n\nvar a = 08 // " + marker + "\n", "syntax error"},
		{"a string that does not end", "package p\n\nvar a = \"" + marker + "\n", "unterminated literal or comment"},
		{"a character Go has no use for", "package p\n\nvar a = 1 # " + marker + "\n", "illegal character"},
	}
	for _, c := range cases {
		got := MeasureGo(c.src).Err
		want := UncheckedNoParse + ": line 3: " + c.kind
		if got != want || !form.MatchString(got) {
			t.Errorf("%s: err is not %q (it has %d bytes, of the form: %v, the marker in it: %v)", c.name, want, len(got), form.MatchString(got), strings.Contains(got, marker))
		}
	}
}
