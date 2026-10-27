package cq

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type tornCheckpointStore struct {
	*memStore
	fail bool
	cut  int
}

func (s *tornCheckpointStore) Write(p string, b []byte) error {
	if !s.fail {
		return s.memStore.Write(p, b)
	}
	n := s.cut
	if n < 0 || n > len(b) {
		n = len(b)
	}
	s.files[p] = append([]byte(nil), b[:n]...)
	return errors.New("injected write failure")
}
func TestCheckpointPairTornWriteRecovery(t *testing.T) {
	for _, cut := range []int{0, 1, 20, -1} {
		s := &tornCheckpointStore{memStore: newMemStore()}
		s.files["a"], s.files["b"] = nil, nil
		pair := CheckpointPair{Store: s, Paths: [2]string{"a", "b"}}
		old := json.RawMessage(`{"cursor":1}`)
		if err := pair.Save(old); err != nil {
			t.Fatal(err)
		}
		s.fail, s.cut = true, cut
		if err := pair.Save(json.RawMessage(`{"cursor":2}`)); err == nil {
			t.Fatal("failed save acknowledged")
		}
		s.fail = false
		state, gen, _, err := pair.Load()
		if err != nil {
			t.Fatal(err)
		}
		expected := string(old)
		if cut < 0 {
			expected = `{"cursor":2}`
		}
		if string(state) != expected {
			t.Fatalf("cut %d lost committed cursor: %s gen %d", cut, state, gen)
		}
		if err := pair.Save(json.RawMessage(`{"cursor":3}`)); err != nil {
			t.Fatal(err)
		}
		state, _, _, err = pair.Load()
		if err != nil || string(state) != `{"cursor":3}` {
			t.Fatalf("retry: %s %v", state, err)
		}
	}
}
func TestCheckpointPairBoundsAndCorruption(t *testing.T) {
	s := newMemStore()
	s.files["a"], s.files["b"] = nil, nil
	p := CheckpointPair{Store: s, Paths: [2]string{"a", "b"}}
	if err := p.Save(json.RawMessage(`{ "path": "<x>&" }`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.Load(); err != nil {
		t.Fatalf("escaped state digest: %v", err)
	}
	big, _ := json.Marshal(strings.Repeat("x", CheckpointBytes))
	if err := p.Save(big); !errors.Is(err, ErrCheckpointFull) {
		t.Fatalf("oversize: %v", err)
	}
	s.files["a"] = []byte("corrupt")
	if _, _, _, err := p.Load(); !errors.Is(err, ErrCheckpointCorrupt) {
		t.Fatalf("no valid checkpoint: %v", err)
	}
	delete(s.files, "b")
	if _, _, _, err := p.Load(); err == nil || errors.Is(err, ErrCheckpointCorrupt) {
		t.Fatalf("read failure mistaken for corruption: %v", err)
	}
}
