package cq

import (
	"errors"
	"fmt"
	"testing"
)

type countedJournalStore struct {
	*journalStore
	reads int
}

func (s *countedJournalStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	s.reads++
	return s.journalStore.Read(p, off, n)
}
func TestJournalWriterValidatesOnceAndInvalidatesAfterFailure(t *testing.T) {
	s := &countedJournalStore{journalStore: &journalStore{exists: true}}
	p := &JournalProgress{Journal: &Journal{Store: s, Path: "journal"}}
	for i := 0; i < 800; i++ {
		if err := p.Save(*journalRecord(fmt.Sprint(i)).Record); err != nil {
			t.Fatal(err)
		}
	}
	if s.reads != 1 {
		t.Fatalf("quadratic replay: %d reads for 800 serialized appends", s.reads)
	}
	s.appendErr, s.writeBeforeError = errors.New("torn append"), 7
	if err := p.Save(*journalRecord("torn").Record); err == nil {
		t.Fatal("write failure acknowledged")
	}
	s.appendErr = nil
	before := len(s.data)
	if err := p.Save(*journalRecord("retry").Record); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("cached end bypassed torn tail: %v", err)
	}
	if len(s.data) != before {
		t.Fatal("appended after corrupt tail")
	}
}
