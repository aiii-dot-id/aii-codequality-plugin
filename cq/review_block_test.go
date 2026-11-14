package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func foldPage(t *testing.T, j *Journal, q ReviewQuery) (ReviewPage, []byte, []byte) {
	t.Helper()
	p, err := ReviewFold(j, q)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return p, got, want
}

func inBlock(p ReviewPage, got []byte, pageBytes int) bool {
	i := bytes.Index(got, []byte(`"entries":`))
	if p.block == nil || len(p.Entries) == 0 || i < 0 {
		return false
	}
	at := i + len(`"entries":`)
	return at < len(got) && &got[at] == &p.Entries[0] && cap(p.block.buf) == pageHead+pageBytes+pageTail
}

func TestAPageFromItsBlockIsThePageMarshalGives(t *testing.T) {
	ev := synthetic(60)
	odd := reviewFixture("pkg/<a&b>  \"q\".go", "EH-01")
	oddCov := completeFixture(odd)
	ev = append(ev, JournalEvent{Record: &odd}, JournalEvent{Coverage: &oddCov},
		JournalEvent{Coverage: &Coverage{Ref: "pkg/<gap&> ", Kind: "subtree", Disposition: "unknown", Reasons: []string{"rules_error", "children_not_enumerated"}}})
	j := journalOf(t, ev)
	pages := 0
	for _, done := range []bool{true, false} {
		for _, shape := range []string{"review_queue", "coverage"} {
			for _, lane := range []string{"", "correctness", "maintainability"} {
				for _, bound := range []struct{ bytes, limit int }{{200000, 100}, {131072, 7}, {16384, 100}, {16384, 1}} {
					cursor := ""
					for n := 0; ; n++ {
						q := ReviewQuery{Shape: shape, Lane: lane, Cursor: cursor, Generation: "g", PageBytes: bound.bytes, Limit: bound.limit, ScanDone: done}
						p, got, want := foldPage(t, j, q)
						if !bytes.Equal(got, want) {
							t.Fatalf("%s %q %+v page %d: the block's page is not what Marshal gives:\n%s\n%s", shape, lane, bound, n, got, want)
						}
						if !inBlock(p, got, bound.bytes) {
							t.Fatalf("%s %q %+v page %d: the page was not returned from its block", shape, lane, bound, n)
						}
						pages++
						if p.Next == "" {
							break
						}
						if n > 10000 {
							t.Fatal("pages never end")
						}
						cursor = p.Next
					}
				}
			}
		}
	}
	if pages < 100 {
		t.Fatalf("only %d pages were compared", pages)
	}
}

func TestAPagesBoundIsToTheByte(t *testing.T) {
	var ev []JournalEvent
	for _, ref := range []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go"} {
		r := reviewFixture(ref, "EH-01")
		r.Findings[0].Where = strings.Repeat("w", 900)
		c := completeFixture(r)
		ev = append(ev, JournalEvent{Record: &r}, JournalEvent{Coverage: &c})
	}
	j := journalOf(t, ev)
	at := func(pageBytes int) (ReviewPage, []json.RawMessage) {
		p, got, want := foldPage(t, j, ReviewQuery{Shape: "review_queue", Tier: TierAll, Generation: "g", PageBytes: pageBytes, Limit: 100, ScanDone: true})
		if !bytes.Equal(got, want) || !inBlock(p, got, pageBytes) {
			t.Fatalf("a page of %d bytes is not its block's page, or not what Marshal gives", pageBytes)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(p.Entries, &rows); err != nil {
			t.Fatal(err)
		}
		return p, rows
	}
	_, rows := at(200000)
	if len(rows) != 8 {
		t.Fatalf("%d entries in the whole queue, want 8", len(rows))
	}

	exact, k := 2, 0
	for (exact <= 4096 || k < 2) && k < len(rows) {
		exact += len(rows[k]) + 1
		k++
	}
	if exact <= 4096 || k < 2 || k >= len(rows) {
		t.Fatalf("the fixture's entries do not suit the test: %d entries reach %d bytes", k, exact)
	}
	if p, got := at(exact); len(got) != k || p.Next == "" || len(p.Entries) != exact-1 {
		t.Fatalf("at the exact bound of %d: %d entries in %d bytes, want %d in %d", exact, len(got), len(p.Entries), k, exact-1)
	}
	if p, got := at(exact - 1); len(got) != k-1 || p.Next == "" {
		t.Fatalf("one byte under the bound: %d entries, want %d", len(got), k-1)
	}

	big := reviewFixture("big.go", "EH-01")
	big.Findings[0].Where = strings.Repeat("x", 4096)
	bj := journalOf(t, []JournalEvent{{Record: &big}})
	p, err := ReviewFold(bj, ReviewQuery{Shape: "review_queue", Tier: TierAll, Generation: "g", PageBytes: 4096, Limit: 1, ScanDone: true})
	var fit []FileReview
	if err != nil || len(p.Entries) > 4096 || json.Unmarshal(p.Entries, &fit) != nil || len(fit) != 1 || fit[0].Omitted == nil || fit[0].Omitted.Findings != 1 || len(fit[0].Findings) != 0 {
		t.Fatalf("an entry larger than the page: %v, %d bytes: %.300s", err, len(p.Entries), p.Entries)
	}
	long := reviewFixture(strings.Repeat("d/", 2100)+"long.go", "EH-01")
	lj := journalOf(t, []JournalEvent{{Record: &long}})
	p, err = ReviewFold(lj, ReviewQuery{Shape: "review_queue", Tier: TierAll, Generation: "g", PageBytes: 4096, Limit: 1, ScanDone: true})
	if !errors.Is(err, ErrReviewEntryTooLarge) {
		t.Fatalf("an entry no cut fits: %v", err)
	}
	if p.block != nil || string(p.Entries) != "[]" {
		t.Fatalf("a refused page carries entries: %s", p.Entries)
	}
	if p, got, want := foldPage(t, bj, ReviewQuery{Shape: "review_queue", Tier: TierAll, Generation: "g", PageBytes: 200000, Limit: 1, ScanDone: true}); !bytes.Equal(got, want) || !inBlock(p, got, 200000) {
		t.Fatal("the same entry in a page that holds it is not its block's page")
	}
}

func TestALaterPageLeavesAnEarlierOneAsItWas(t *testing.T) {
	j := journalOf(t, synthetic(40))
	ask := func(pageBytes, limit int, cursor string) ReviewQuery {
		return ReviewQuery{Shape: "review_queue", Tier: TierAll, Cursor: cursor, Generation: "g", PageBytes: pageBytes, Limit: limit, ScanDone: true}
	}
	_, large, _ := foldPage(t, j, ask(200000, 100, ""))
	largeThen := append([]byte(nil), large...)
	_, small, _ := foldPage(t, j, ask(16384, 1, ""))
	smallThen := append([]byte(nil), small...)
	if _, err := ReviewFold(j, ask(200000, 100, "not a cursor")); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("a stale cursor: %v", err)
	}
	p, again, want := foldPage(t, j, ask(200000, 100, ""))
	if !bytes.Equal(large, largeThen) || !bytes.Equal(small, smallThen) {
		t.Fatal("a later page changed an earlier page's bytes")
	}
	if !bytes.Equal(again, largeThen) || !bytes.Equal(again, want) {
		t.Fatal("the same request gave a different page")
	}
	twice, err := p.JSON()
	if err != nil || !bytes.Equal(twice, again) {
		t.Fatalf("a second encoding of one page differs: %v", err)
	}
}

func TestAPageOutsideItsBlockIsStillThePage(t *testing.T) {
	r := reviewFixture("a.go", "EH-01")
	old, err := PageReview(BuildReview([]Record{r}, nil, true, true, ""), ReviewQuery{Generation: "g", Shape: "review_queue", Tier: TierAll, Lane: "", Cursor: "", PageBytes: 4096, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := old.JSON()
	want, _ := json.Marshal(old)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("a page built at once: %v\n%s\n%s", err, got, want)
	}
	j := journalOf(t, []JournalEvent{{Record: &r}})
	p, err := ReviewFold(j, ReviewQuery{Shape: "review_queue", Tier: TierAll, Generation: strings.Repeat("g", pageHead), PageBytes: 4096, Limit: 1, ScanDone: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err = p.JSON()
	want, _ = json.Marshal(p)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("a page with a member too long for the room: %v", err)
	}
	if inBlock(p, got, 4096) {
		t.Fatal("a page whose members do not fit the room was returned from its block")
	}
}
