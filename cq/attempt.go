package cq

import (
	"encoding/json"
	"runtime"
)

const NotFinished = "not_finished"

const MaxAttempts = 2

const NotFinishedListed = 200

const NamesBytes = 16 << 10

func keepName(list []string, name string, most int) []string {
	if len(list) >= most {
		return list
	}
	b, err := json.Marshal(append(list[:len(list):len(list)], name))
	if err != nil || len(b) > NamesBytes {
		return list
	}
	return append(list, name)
}

type Attempt struct {
	Ref     string `json:"ref"`
	Endings int    `json:"endings,omitempty"`
	Live    bool   `json:"live,omitempty"`
}

type AttemptStore interface {
	Load() (Attempt, error)
	Save(Attempt) error
	Clear() error
}

type attempts struct {
	store   AttemptStore
	prior   Attempt
	reached bool
	on      Attempt
	stored  Attempt
}

func loadAttempts(store AttemptStore) (*attempts, error) {
	a := &attempts{store: store}
	if store == nil {
		return a, nil
	}
	rec, err := store.Load()
	if err != nil {
		return a, err
	}
	a.stored = rec
	if rec.Live {
		rec.Endings++
		rec.Live = false
		if err := a.write(rec); err != nil {
			return a, err
		}
	}
	a.prior = rec
	return a, nil
}

func (a *attempts) write(rec Attempt) error {
	if rec == a.stored {
		return nil
	}
	var err error
	if rec == (Attempt{}) {
		err = a.store.Clear()
	} else {
		err = a.store.Save(rec)
	}
	if err == nil {
		a.stored = rec
	}
	return err
}

func (a *attempts) begin(ref string) (giveUp bool, err error) {
	if a.store == nil {
		return false, nil
	}
	n := 0
	if a.prior.Ref != "" && ref == a.prior.Ref {
		a.reached = true
		n = a.prior.Endings
	}
	if n >= MaxAttempts {
		a.on = Attempt{}
		return true, nil
	}
	a.on = Attempt{Ref: ref, Endings: n, Live: true}
	err = a.write(a.on)

	runtime.GC()
	return false, err
}

func (a *attempts) finished() { a.on = Attempt{} }

func (a *attempts) done() error {
	if a.store == nil {
		return nil
	}
	a.prior, a.reached, a.on = Attempt{}, false, Attempt{}
	return a.write(Attempt{})
}

func (a *attempts) left(ref string) {
	if a != nil && a.prior.Ref == ref {
		a.prior, a.reached = Attempt{}, false
	}
}

func (a *attempts) settle() error {
	if a.store == nil {
		return nil
	}
	want := a.on
	want.Live = false
	if want.Endings == 0 {
		want = Attempt{}
	}
	if a.prior.Ref != "" && !a.reached {
		want = a.prior
	}
	a.on = Attempt{}
	return a.write(want)
}

func (s *Scan) notFinished(e Entry, rel string) error {
	if err := s.cover(Coverage{Ref: rel, Kind: "file", Disposition: "unknown", Reasons: []string{NotFinished}, Size: e.Size}); err != nil {
		return err
	}
	if err := s.skippedGo(rel, e.Name, NotFinished); err != nil {
		return err
	}
	s.FilesSeen++
	if s.Excluded == nil {
		s.Excluded = map[string]int{}
	}
	s.Excluded[NotFinished]++
	s.NotFinished = keepName(s.NotFinished, rel, NotFinishedListed)
	if s.File != nil && s.File.Ref == rel {
		s.File = nil
	}
	return nil
}
