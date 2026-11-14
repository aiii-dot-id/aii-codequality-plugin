package cq

import (
	"reflect"
	"strings"
	"testing"
)

const measureFixture = `// Licence line.

package p

type T struct{ n int }

type Set[K comparable] struct{ m map[K]bool }

func short() {
	return
}

// long has a doc comment, which is not part of its length.
func (t *T) long(a, b int, c string) int {
	for i := 0; i < a; i++ {
		if i > b {
			switch c {
			case "x":
				t.n++
			}
		} else if i == b {
			t.n--
		}
	}
	return t.n
}

func (s *Set[K]) Add(k K) { s.m[k] = true }

func external(x int) int
`

func TestMeasureGoFunctions(t *testing.T) {
	got := MeasureGo(measureFixture)
	want := FileMeasure{Lines: 30, Types: 2, Funcs: []FuncMeasure{
		{Name: "short", Line: 9, Lines: 3},
		{Name: "T.long", Line: 14, Lines: 13, Depth: 3, Params: 3},
		{Name: "Set.Add", Line: 28, Lines: 1, Params: 1},
		{Name: "external", Line: 30, Lines: 1, Params: 1},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MeasureGo:\n got %+v\nwant %+v", got, want)
	}
}

func TestMeasureGoLargerThanASegment(t *testing.T) {
	const blocks = 4000
	src := "package p\n\nfunc big(n int) {\n" +
		strings.Repeat("\tif n > 0 {\n\t\tn-- // padding so that the function passes one segment's bytes\n\t}\n", blocks) +
		"}\n\nfunc after() {}\n"
	if len(src) <= SegmentBytes {
		t.Fatalf("the fixture is %d bytes, not more than a segment (%d)", len(src), SegmentBytes)
	}
	got := MeasureGo(src)
	if got.Err != "" || len(got.Funcs) != 2 {
		t.Fatalf("err %q, %d functions; want none and 2", got.Err, len(got.Funcs))
	}
	if want := (FuncMeasure{Name: "big", Line: 3, Lines: 3*blocks + 2, Depth: 1, Params: 1}); got.Funcs[0] != want {
		t.Fatalf("big: got %+v want %+v", got.Funcs[0], want)
	}
	if want := (FuncMeasure{Name: "after", Line: 3*blocks + 6, Lines: 1}); got.Funcs[1] != want {
		t.Fatalf("after: got %+v want %+v", got.Funcs[1], want)
	}
}

func TestMeasureGoNoParse(t *testing.T) {
	got := MeasureGo("package p\n\nfunc (\n")
	if got.Lines != 3 || len(got.Funcs) != 0 || !strings.HasPrefix(got.Err, UncheckedNoParse+": line ") {
		t.Fatalf("got %+v; want 3 lines, no function, and %q with the line and the complaint", got, UncheckedNoParse)
	}
}

func TestRankFuncs(t *testing.T) {
	files := map[string]FileMeasure{
		"a.go": {Funcs: []FuncMeasure{{Name: "f", Line: 1, Lines: 10, Depth: 1}, {Name: "g", Line: 20, Lines: 50}}},
		"b.go": {Funcs: []FuncMeasure{{Name: "k", Line: 5, Lines: 10, Depth: 1}, {Name: "h", Line: 30, Lines: 50, Depth: 3}}},
	}
	var got []string
	for _, r := range RankFuncs(files) {
		got = append(got, r.File+":"+r.Name)
	}

	want := []string{"b.go:h", "a.go:g", "a.go:f", "b.go:k"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order: got %v want %v", got, want)
	}
}
