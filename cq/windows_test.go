//go:build !wasm_unknown

package cq

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func windowIDs(items []Item) []string {
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func TestContextWindowsContextualDocsInput(t *testing.T) {
	tb := table(t)
	src := "package p\n\n// Returns true.\nfunc Allowed() bool { return false }\n"
	ws, err := ContextWindows(tb, "go", src, 256)
	if err != nil || len(ws) != 1 {
		t.Fatalf("windows=%+v err=%v", ws, err)
	}
	w := ws[0]
	if !strings.Contains(w.Context.Text, "// Returns true.\nfunc Allowed() bool { return false }") {
		t.Fatalf("comment/code relationship missing: %q", w.Context.Text)
	}
	if strings.Contains(w.Code.Text, "Returns true") || !strings.Contains(w.Code.Text, "return false") {
		t.Fatalf("wrong blanking: %q", w.Code.Text)
	}
	if w.Boundary != "go_declaration" || w.SplitUnit {
		t.Fatalf("complete declaration: %+v", w)
	}
	if !reflect.DeepEqual(windowIDs(w.DocItems), []string{"DC-01", "DC-02", "DC-03", "DC-04", "GC-02"}) {
		t.Fatalf("items: %+v", w.DocItems)
	}
	changed, err := ContextWindows(tb, "go", strings.ReplaceAll(src, "return false", "return true"), 256)
	if err != nil {
		t.Fatal(err)
	}
	if changed[0].Context.Text == w.Context.Text {
		t.Fatal("code-only change must change documentation input")
	}
	assertWindowCoverage(t, src, ws, 256)
}

func TestContextWindowsApplicability(t *testing.T) {
	for _, tt := range []struct {
		name, lang, source string
		yes, no            []string
	}{
		{"comment_free", "go", "package p\nfunc F(x int) int { return 100 / x }\n", []string{"DC-01", "DC-02", "GC-02"}, []string{"DC-03", "DC-04"}},
		{"comment_syntax_in_string", "go", "package p\nvar X = `// not a comment`\n", []string{"DC-01", "DC-02", "GC-02"}, []string{"DC-03", "DC-04"}},
		{"markdown", "markdown", "# Purpose\nA user's guide; no code is present.\n", []string{"DC-01", "GC-02"}, []string{"DC-02", "DC-03", "DC-04"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := ContextWindows(table(t), tt.lang, tt.source, 1024)
			if err != nil || len(ws) != 1 {
				t.Fatalf("%+v %v", ws, err)
			}
			if !reflect.DeepEqual(windowIDs(ws[0].DocItems), tt.yes) || !reflect.DeepEqual(ws[0].NonApplicable, tt.no) {
				t.Fatalf("applicability: %+v", ws[0])
			}
			if tt.lang == "markdown" && strings.TrimSpace(ws[0].Code.Text) != "" {
				t.Fatal("markdown must have no code battery content")
			}
		})
	}
}

func TestContextWindowsCompleteGoDeclarations(t *testing.T) {
	src := "package p\n\n// A returns one.\nfunc A() int {\n return 1\n}\n\n// B returns two.\nfunc B() int {\n return 2\n}\n"
	ws, err := ContextWindows(table(t), "go", src, 70)
	if err != nil || len(ws) != 2 {
		t.Fatalf("%+v %v", ws, err)
	}
	if !strings.Contains(ws[0].Context.Text, "// A returns one.\nfunc A()") || !strings.Contains(ws[1].Context.Text, "// B returns two.\nfunc B()") {
		t.Fatalf("doc attachment lost: %+v", ws)
	}
	for _, w := range ws {
		if w.SplitUnit || w.Boundary != "go_declaration" {
			t.Fatalf("unexpected split: %+v", w)
		}
	}
	if ws[1].Context.FirstLine != 8 {
		t.Fatalf("original line: %+v", ws[1])
	}
	assertWindowCoverage(t, src, ws, 70)
}

func TestContextWindowsFallbackAndSplit(t *testing.T) {
	for _, tt := range []struct {
		name, lang, source, boundary string
		size                         int
		limited                      bool
	}{
		{"parse_failure", "go", "package p\n// broken\nfunc F(\n", "go_parse_fallback", 128, true},
		{"other_language", "python", "# return one\ndef f():\n    return 1\n", "lexical", 128, false},
		{"oversized_declaration", "go", "package p\n// needs entire body\nfunc F() {\n" + strings.Repeat("println(1)\n", 30) + "}\n", "go_declaration/split", 40, true},
		{"long_comment", "javascript", "//" + strings.Repeat("界", 200) + "\nlet x = 1;\n", "lexical/split", 32, true},
		{"unterminated_string", "javascript", "let x = `hello", "lexical", 128, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := ContextWindows(table(t), tt.lang, tt.source, tt.size)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range ws {
				if w.Boundary != tt.boundary || w.SplitUnit != tt.limited {
					t.Fatalf("boundary metadata: %+v", w)
				}
			}
			assertWindowCoverage(t, tt.source, ws, tt.size)
		})
	}
}

func TestContextWindowsRedactionPreservesRanges(t *testing.T) {
	key := "-----BEGIN PRIVATE KEY-----\nABCDEFGsecretmaterial\n-----END PRIVATE KEY-----"
	github := "ghp_" + strings.Repeat("A", 36)
	src := "package p\n// " + github + "\nvar K = `" + key + "`\nvar N = 3\n"
	ws, err := ContextWindows(table(t), "go", src, 32)
	if err != nil {
		t.Fatal(err)
	}
	var context, code strings.Builder
	total := 0
	for _, w := range ws {
		context.WriteString(w.Context.Text)
		code.WriteString(w.Code.Text)
		total += w.Redacted
	}
	for _, text := range []string{context.String(), code.String()} {
		for _, secret := range []string{github, "PRIVATE KEY", "ABCDEFGsecretmaterial"} {
			if strings.Contains(text, secret) {
				t.Fatalf("secret survives: %q", text)
			}
		}
		if len(text) != len(src) || strings.Count(text, "\n") != strings.Count(src, "\n") {
			t.Fatal("redaction shifted source positions")
		}
	}
	if total != 2 {
		t.Fatalf("distinct source redactions=%d", total)
	}
	assertWindowCoverage(t, src, ws, 32)
}

func TestContextWindowsRequestByteBound(t *testing.T) {

	for _, lang := range []string{"markdown", "javascript"} {
		src := "//" + strings.Repeat("界\\\"\t<>&\u2028\x00", 400) + "\ncode()\n"
		ws, err := ContextWindows(table(t), lang, src, 31)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			encoded, err := json.Marshal(w.Context.Text)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > 6*31+2 {
				t.Fatalf("unbounded escaping: %d", len(encoded))
			}
			if !w.SplitUnit {
				t.Fatal("giant line claimed complete")
			}
		}
		assertWindowCoverage(t, src, ws, 31)
	}
}

func TestContextWindowsBoundsAndErrors(t *testing.T) {
	tb := table(t)
	for _, tt := range []struct {
		name      string
		table     *Table
		lang, src string
		size      int
	}{
		{"nil_table", nil, "go", "x", 32},
		{"unknown", tb, "unknown", "x", 32},
		{"oversize", tb, "markdown", strings.Repeat("x", (1<<20)+1), 32},
		{"invalid_utf8", tb, "markdown", string([]byte{0xff}), 32},
		{"tiny_budget", tb, "markdown", "界", 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ContextWindows(tt.table, tt.lang, tt.src, tt.size); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	ws, err := ContextWindows(tb, "go", "", 32)
	if err != nil || len(ws) != 0 {
		t.Fatalf("empty source: %+v %v", ws, err)
	}
	src := strings.Repeat("x", 1<<20)
	ws, err = ContextWindows(tb, "markdown", src, 2048)
	if err != nil || len(ws) != 512 {
		t.Fatalf("1MiB: %d windows %v", len(ws), err)
	}
	assertWindowCoverage(t, src, ws, 2048)
	ws, err = ContextWindows(tb, "markdown", strings.Repeat("x", ChunkBytes+1), 0)
	if err != nil || len(ws) != 2 {
		t.Fatalf("default: %d %v", len(ws), err)
	}
}

func TestContextWindowsBlankerMatchesExistingRules(t *testing.T) {
	tb := table(t)
	for _, tt := range []struct{ lang, src string }{
		{"go", "package p\n// 注释\nvar X = `// literal\nraw` /* block\nend */ + \"/* string */\"\n"},
		{"go", "package p\nvar C = '\\'' // quote\nvar R = '界'\n"},
		{"python", "'''doc\nstring'''\nx = \"# literal\" # comment\n"},
		{"python", "x = r'\\' # comment\n"},
		{"javascript", "// line\nconst s = `/* literal */`; /* multi\nline */ fn();\n"},
		{"rust", "fn f<'a>(x: &'a str) { /* doc */ println!(\"//\"); }"},
		{"html", "<p><!-- comment -->text</p>"},
		{"go", "package p\n/* unterminated\n"},
	} {
		blanked, spans, lost := windowBlank(tt.src, tb.Langs[tt.lang])
		got := string(blanked)
		want, _, wantLost, _ := BlankComments(tt.src, tb.Langs[tt.lang])
		var removed strings.Builder
		from := 0
		for _, span := range spans {
			removed.WriteString(got[from:span[0]])
			removed.WriteString(strings.Repeat("\n", strings.Count(got[span[0]:span[1]], "\n")))
			from = span[1]
		}
		removed.WriteString(got[from:])
		if trailingSpace.ReplaceAllString(removed.String(), "\n") != want || lost != wantLost {
			t.Fatalf("%s got=%q lost=%v want=%q lost=%v", tt.lang, got, lost, want, wantLost)
		}
		if len(got) != len(tt.src) || strings.Count(got, "\n") != strings.Count(tt.src, "\n") {
			t.Fatal("blanking shifted positions")
		}
	}
}

func assertWindowCoverage(t *testing.T, src string, ws []SourceWindow, size int) {
	t.Helper()
	offset, line := 0, 1
	for _, w := range ws {
		n := len(w.Context.Text)
		if n == 0 || n > size || len(w.Code.Text) != n || !utf8.ValidString(w.Context.Text) || !utf8.ValidString(w.Code.Text) {
			t.Fatalf("invalid bounded/aligned text: %+v", w)
		}
		if offset+n > len(src) {
			t.Fatal("overlap or extra bytes")
		}
		original := src[offset : offset+n]
		last := line + strings.Count(original, "\n")
		if strings.HasSuffix(original, "\n") {
			last--
		}
		if w.Context.FirstLine != line || w.Code.FirstLine != line || w.Context.LastLine != last || w.Code.LastLine != last {
			t.Fatalf("source range drift at offset %d: %+v", offset, w)
		}
		for i := 0; i < len(original); i++ {
			if original[i] == '\n' && (w.Context.Text[i] != '\n' || w.Code.Text[i] != '\n') {
				t.Fatalf("lost newline at %d", offset+i)
			}
			if w.Context.Text[i] != original[i] && w.Context.Text[i] != '#' {
				t.Fatalf("context byte shifted at %d", offset+i)
			}
			if w.Code.Text[i] != original[i] && w.Code.Text[i] != ' ' && w.Code.Text[i] != '#' {
				t.Fatalf("code byte shifted at %d", offset+i)
			}
		}
		offset += n
		line += strings.Count(original, "\n")
	}
	if offset != len(src) {
		t.Fatalf("covered %d of %d bytes", offset, len(src))
	}
}

func TestContextWindowsSmallUTF8Budgets(t *testing.T) {
	tb := table(t)
	for _, src := range []string{
		"\n\n界\r\n// comment\r\nconst x = `界界界`;\n",
		"// secret = \"界界界界界界界界界界界界\"\nconst x = 1;",
		"/* multi\nline */x(); // tail\n",
		"const x = '\\''; // comment\n",
	} {
		for size := 4; size < 40; size++ {
			ws, err := ContextWindows(tb, "javascript", src, size)
			if err != nil {
				t.Fatal(err)
			}
			assertWindowCoverage(t, src, ws, size)
		}
	}
}

func TestContextWindowsPackDeclarationsAndLineDirectives(t *testing.T) {
	src := "package p\n//line imaginary.go:1000\nvar X = 1\n// Y is two.\nvar Y = 2\n"
	ws, err := ContextWindows(table(t), "go", src, 1024)
	if err != nil || len(ws) != 1 {
		t.Fatalf("complete declarations not packed: %d %v", len(ws), err)
	}
	assertWindowCoverage(t, src, ws, 1024)
	ws, err = ContextWindows(table(t), "go", src, 52)
	if err != nil || len(ws) != 2 {
		t.Fatalf("declaration split: %d %v", len(ws), err)
	}
	if ws[1].Context.FirstLine != 4 || ws[1].SplitUnit {
		t.Fatalf("line directive replaced physical source coordinates: %+v", ws[1])
	}
	assertWindowCoverage(t, src, ws, 52)
}

func FuzzContextWindowsRanges(f *testing.F) {
	f.Add("// hello\nlet x = '界';\n", uint16(19))
	f.Add("/* multi\nline */\nfoo();\n", uint16(4))
	f.Add("// token = \"abcdefghijklmnop\"\nconst x = `界`;", uint16(8))
	tb, err := LoadTable()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, src string, size uint16) {
		if !utf8.ValidString(src) || len(src) > 8192 {
			t.Skip()
		}
		bound := int(size)%256 + 4
		ws, err := ContextWindows(tb, "javascript", src, bound)
		if err != nil {
			t.Fatal(err)
		}
		assertWindowCoverage(t, src, ws, bound)
	})
}
