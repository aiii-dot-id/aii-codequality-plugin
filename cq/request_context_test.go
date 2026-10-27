package cq

import (
	"errors"
	"strings"
	"testing"
)

func TestEncodedRequestWindows(t *testing.T) {
	table, _ := LoadTable()
	source := strings.Repeat("\x01", 150000)
	windows, e := boundedContextWindows(table, "markdown", source, 131072)
	if e != nil || len(windows) < 3 {
		t.Fatalf("must reduce escaping-heavy windows: %d %v", len(windows), e)
	}
	total := 0
	for _, w := range windows {
		total += len(w.Context.Text)
		b, e := RequestBody("fixture", map[string]any{"language": "markdown", "source": w.Context.Text}, Questions(w.DocItems, "source"))
		if e != nil || len(b) > MaxRequestBytes || len(w.Context.Text) > 131072 {
			t.Fatalf("oversized request: %d %v", len(b), e)
		}
	}
	if total != len(source) {
		t.Fatalf("lost source: %d != %d", total, len(source))
	}
	_, e = RequestBody("fixture", map[string]any{"source": source}, map[string]any{})
	if !errors.Is(e, ErrRequestSize) {
		t.Fatalf("irreducible oversized request sent: %v", e)
	}
}
