package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
)

type journalStore struct {
	data             []byte
	exists           bool
	readErr          error
	appendErr        error
	writeBeforeError int
	readBytes        int
	appendBytes      int
	appends          int
	noProgress       bool
	omitEOF          bool
	shortRead        int
}

func (s *journalStore) Read(_ string, off int64, n int) ([]byte, bool, error) {
	s.readBytes = max(s.readBytes, n)
	if s.readErr != nil {
		return nil, false, s.readErr
	}
	if !s.exists {
		return nil, false, fs.ErrNotExist
	}
	if s.noProgress {
		return nil, false, nil
	}
	if off >= int64(len(s.data)) {
		return nil, true, nil
	}
	if s.shortRead > 0 {
		n = min(n, s.shortRead)
	}
	stop := min(int(off)+n, len(s.data))
	return append([]byte(nil), s.data[off:stop]...), !s.omitEOF && stop == len(s.data), nil
}
func (s *journalStore) Write(_ string, b []byte) error {
	s.data, s.exists = append([]byte(nil), b...), true
	return nil
}
func (s *journalStore) Append(_ string, b []byte) error {
	s.appends++
	s.appendBytes = max(s.appendBytes, len(b))
	if s.appendErr != nil {
		n := s.writeBeforeError
		if n < 0 {
			n = len(b)
		}
		s.data = append(s.data, b[:min(n, len(b))]...)
		return s.appendErr
	}
	s.data = append(s.data, b...)
	return nil
}

func journalFixture(t *testing.T) (*Journal, *journalStore) {
	t.Helper()
	s := &journalStore{}
	if err := s.Write("journal", nil); err != nil {
		t.Fatal(err)
	}
	return &Journal{Store: s, Path: "journal"}, s
}
func journalRecord(id string) JournalEvent {
	return JournalEvent{Record: &Record{Version: RecordVersion, ID: id, Ref: "a.go"}}
}
func journalCoverage() JournalEvent {
	return JournalEvent{Coverage: &Coverage{Ref: "a.go", Kind: "file", Disposition: "pending", ExpectedChunks: 2}}
}
func journalLine(t *testing.T, e JournalEvent) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestJournalStreamsBeyondCheckpointBudget(t *testing.T) {
	j, s := journalFixture(t)
	const count = 800
	var checkpoint int64
	for i := 0; i < count; i++ {
		event := journalRecord(fmt.Sprint(i))
		event.Record.Ref = strings.Repeat("folder/", 40) + "a.go"
		if i%2 == 1 {
			event = journalCoverage()
		}
		end, err := j.Append(event)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if i == 399 {
			checkpoint = end
		}
	}
	if len(s.data) <= 250<<10 {
		t.Fatalf("fixture only %d bytes", len(s.data))
	}
	seen := 0
	end, err := j.Walk(0, func(e JournalEvent) error {
		if seen%2 == 0 && (e.Record == nil || e.Record.ID != fmt.Sprint(seen)) {
			t.Fatalf("record %d: %+v", seen, e)
		}
		if seen%2 == 1 && e.Coverage == nil {
			t.Fatalf("coverage %d missing", seen)
		}
		seen++
		return nil
	})
	if err != nil || seen != count || end != int64(len(s.data)) {
		t.Fatalf("end %d seen %d: %v", end, seen, err)
	}
	seen = 0
	again := &Journal{Store: s, Path: j.Path}
	end, err = again.Walk(checkpoint, func(JournalEvent) error { seen++; return nil })
	if err != nil || seen != 400 || end != int64(len(s.data)) {
		t.Fatalf("resume %d seen %d: %v", end, seen, err)
	}
	if s.readBytes > 64<<10 || s.appendBytes > 250<<10 {
		t.Fatalf("I/O exceeded limits: read %d append %d", s.readBytes, s.appendBytes)
	}
}

func TestJournalTruncationRefusesTail(t *testing.T) {
	j, s := journalFixture(t)
	first := journalLine(t, journalRecord("first"))
	second := journalLine(t, journalRecord("second"))
	for cut := 1; cut < len(second); cut++ {
		s.data = append(append([]byte(nil), first...), second[:cut]...)
		seen := 0
		end, err := j.Walk(0, func(JournalEvent) error { seen++; return nil })
		if !errors.Is(err, ErrJournalCorrupt) || end != int64(len(first)) || seen != 1 {
			t.Fatalf("cut %d: end %d seen %d err %v", cut, end, seen, err)
		}
		before := append([]byte(nil), s.data...)
		if _, err = j.Append(journalRecord("retry")); !errors.Is(err, ErrJournalCorrupt) {
			t.Fatalf("cut %d appended: %v", cut, err)
		}
		if !bytes.Equal(before, s.data) {
			t.Fatal("torn tail was modified")
		}
	}
	s.data = append(append([]byte(nil), first...), second...)
	if end, err := j.Walk(0, nil); err != nil || end != int64(len(s.data)) {
		t.Fatalf("complete control: %d %v", end, err)
	}
}

func TestJournalAppendFailureReplay(t *testing.T) {
	boom := errors.New("injected append error")
	for _, written := range []int{0, 17, -1} {
		t.Run(fmt.Sprint(written), func(t *testing.T) {
			j, s := journalFixture(t)
			prior, err := j.Append(journalRecord("first"))
			if err != nil {
				t.Fatal(err)
			}
			s.appendErr, s.writeBeforeError = boom, written
			if end, err := j.Append(journalRecord("second")); !errors.Is(err, boom) || end != prior {
				t.Fatalf("failed append: %d %v", end, err)
			}
			s.appendErr = nil
			seen := 0
			end, err := j.Walk(0, func(JournalEvent) error { seen++; return nil })
			if written == 17 {
				if !errors.Is(err, ErrJournalCorrupt) || end != prior || seen != 1 {
					t.Fatalf("partial: %d %d %v", end, seen, err)
				}
				calls := s.appends
				if _, err := j.Append(journalRecord("third")); !errors.Is(err, ErrJournalCorrupt) || s.appends != calls {
					t.Fatalf("appended after torn tail: %v", err)
				}
				return
			}
			want := 1
			if written < 0 {
				want = 2
			}
			if err != nil || seen != want || end != int64(len(s.data)) {
				t.Fatalf("replay: %d %d %v", end, seen, err)
			}
			if end, err = j.Append(journalRecord("third")); err != nil || end != int64(len(s.data)) {
				t.Fatalf("retry after reread: %d %v", end, err)
			}
		})
	}
}

func TestJournalCapBoundary(t *testing.T) {
	j, s := journalFixture(t)
	e := journalRecord("one")
	size := int64(len(journalLine(t, e)))
	j.MaxBytes = size
	if end, err := j.Append(e); err != nil || end != size {
		t.Fatalf("exact cap: %d %v", end, err)
	}
	s.omitEOF = true
	if end, err := j.Walk(0, nil); err != nil || end != size {
		t.Fatalf("walk exact cap: %d %v", end, err)
	}
	calls := s.appends
	if end, err := j.Append(e); !errors.Is(err, ErrJournalFull) || end != size || s.appends != calls {
		t.Fatalf("appended over cap: %d %v", end, err)
	}
	s.data = append(s.data, ' ')
	if _, err := j.Walk(0, nil); !errors.Is(err, ErrJournalFull) {
		t.Fatalf("over-cap read: %v", err)
	}
	j.MaxBytes = -1
	if _, err := j.Walk(0, nil); !errors.Is(err, ErrJournalFull) {
		t.Fatalf("negative budget: %v", err)
	}
	j.MaxBytes = 0
	if limit, err := j.limit(); err != nil || limit != 48<<20 {
		t.Fatalf("default cap: %d %v", limit, err)
	}
}

func TestJournalEventBudgetAndPageSplits(t *testing.T) {
	j, s := journalFixture(t)
	e := journalRecord("large")
	base := len(journalLine(t, e))
	e.Record.Model = strings.Repeat("x", (250<<10)-base)
	if size := len(journalLine(t, e)); size != 250<<10 {
		t.Fatalf("fixture: %d", size)
	}
	end, err := j.Append(e)
	if err != nil || end != 250<<10 {
		t.Fatalf("event at cap: %d %v", end, err)
	}

	s.shortRead = 997
	seen := 0
	if _, err = j.Walk(0, func(got JournalEvent) error {
		seen++
		if got.Record.Model != e.Record.Model {
			t.Fatal("split event changed")
		}
		return nil
	}); err != nil || seen != 1 {
		t.Fatalf("walk large event: %d %v", seen, err)
	}
	e.Record.Model += "x"
	calls := s.appends
	if _, err = j.Append(e); !errors.Is(err, ErrJournalFull) || s.appends != calls {
		t.Fatalf("oversized event appended: %v", err)
	}
	s.data = journalLine(t, e)
	if _, err = j.Walk(0, nil); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("oversized stored event: %v", err)
	}
	if s.readBytes > 64<<10 || s.appendBytes > 250<<10 {
		t.Fatalf("I/O: %d %d", s.readBytes, s.appendBytes)
	}
}

func TestJournalCallbackFailureAndCursor(t *testing.T) {
	j, s := journalFixture(t)
	first, err := j.Append(journalRecord("one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.Append(journalRecord("two")); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("visitor stopped")
	seen := 0
	end, err := j.Walk(0, func(JournalEvent) error {
		seen++
		if seen == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || end != first {
		t.Fatalf("callback: %d %v", end, err)
	}
	seen = 0
	end, err = j.Walk(end, func(e JournalEvent) error {
		seen++
		if e.Record.ID != "two" {
			t.Fatal("failed event not replayed")
		}
		return nil
	})
	if err != nil || seen != 1 || end != int64(len(s.data)) {
		t.Fatalf("callback resume: %d %d %v", end, seen, err)
	}
	for _, off := range []int64{-1, 1, first - 1, first + 1, int64(len(s.data)) + 1} {
		if _, err = j.Walk(off, nil); !errors.Is(err, ErrJournalCorrupt) {
			t.Fatalf("bad offset %d: %v", off, err)
		}
	}
	if end, err = j.Walk(int64(len(s.data)), nil); err != nil || end != int64(len(s.data)) {
		t.Fatalf("EOF cursor: %d %v", end, err)
	}

	s.data = append([]byte("garbage"), journalLine(t, journalRecord("suffix"))...)
	if _, err = j.Walk(7, nil); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("valid mid-line suffix: %v", err)
	}
}

func TestJournalInvalidEvents(t *testing.T) {
	valid := journalCoverage()
	badEvents := []JournalEvent{
		{}, {Record: journalRecord("x").Record, Coverage: valid.Coverage},
		journalRecord(" "), {Record: &Record{Version: 1, ID: "legacy"}},
		{Coverage: &Coverage{Ref: "x", Kind: "directory", Disposition: "pending"}},
		{Coverage: &Coverage{Ref: "", Kind: "file", Disposition: "pending"}},
		{Coverage: &Coverage{Ref: "x\x00", Kind: "file", Disposition: "pending"}},
		{Coverage: &Coverage{Ref: "x", Kind: "file", Disposition: "assessed"}},
		{Coverage: &Coverage{Ref: "x", Kind: "file", Disposition: "complete", Size: -1}},
		{Coverage: &Coverage{Ref: "x", Kind: "file", Disposition: "complete", CompletedChunks: 1}},
	}
	for i, e := range badEvents {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			j, s := journalFixture(t)
			if _, err := j.Append(e); !errors.Is(err, ErrJournalCorrupt) || s.appends != 0 {
				t.Fatalf("invalid append: %v", err)
			}
			s.data = journalLine(t, e)
			if _, err := j.Walk(0, nil); !errors.Is(err, ErrJournalCorrupt) {
				t.Fatalf("invalid read: %v", err)
			}
		})
	}
	for _, raw := range []string{"{bad}\n", "\n", "{}\n", "null\n", "{\"record\":null}\n", "{} {}\n", "{\"source\":\"must not persist\"}\n"} {
		j, s := journalFixture(t)
		s.data = []byte(raw)
		if _, err := j.Walk(0, nil); !errors.Is(err, ErrJournalCorrupt) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	for _, kind := range []string{"file", "subtree"} {
		for _, disposition := range []string{"complete", "pending", "unknown", "excluded", "stale"} {
			j, _ := journalFixture(t)
			e := journalCoverage()
			e.Coverage.Kind, e.Coverage.Disposition = kind, disposition
			if _, err := j.Append(e); err != nil {
				t.Fatalf("valid coverage %s/%s: %v", kind, disposition, err)
			}
		}
	}
}

func TestJournalReadErrorsAreNotEmpty(t *testing.T) {
	for _, fault := range []error{fs.ErrPermission, fs.ErrNotExist, io.ErrUnexpectedEOF} {
		j, s := journalFixture(t)
		s.readErr = fault
		if _, err := j.Walk(0, nil); !errors.Is(err, fault) {
			t.Fatalf("read lost %v: %v", fault, err)
		}
		if _, err := j.Append(journalRecord("x")); !errors.Is(err, fault) || s.appends != 0 {
			t.Fatalf("append ignored %v: %v", fault, err)
		}
	}
	j, s := journalFixture(t)
	s.exists = false
	if _, err := j.Append(journalRecord("x")); !errors.Is(err, fs.ErrNotExist) || s.appends != 0 {
		t.Fatalf("implicit initialization: %v", err)
	}
	if err := s.Write(j.Path, nil); err != nil {
		t.Fatal(err)
	}
	if end, err := j.Walk(0, nil); err != nil || end != 0 {
		t.Fatalf("initialized empty: %d %v", end, err)
	}
	if _, err := j.Append(journalRecord("x")); err != nil {
		t.Fatal(err)
	}

	s.readErr = fs.ErrPermission
	if _, err := j.Append(journalRecord("y")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("cached past denied read: %v", err)
	}
	s.readErr = nil
	s.noProgress = true
	if _, err := j.Walk(0, nil); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero bytes without EOF: %v", err)
	}
}
