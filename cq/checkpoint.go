package cq

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrCheckpointCorrupt = errors.New("no valid checkpoint remains")
var ErrCheckpointFull = errors.New("checkpoint exceeds single-write bound")

const CheckpointBytes = 250 << 10

type checkpointEnvelope struct {
	Generation uint64          `json:"generation"`
	Digest     string          `json:"digest"`
	State      json.RawMessage `json:"state"`
}

type CheckpointPair struct {
	Store Store
	Paths [2]string
}

func (p CheckpointPair) read() (checkpointEnvelope, int, bool, error) {
	var best checkpointEnvelope
	slot := -1
	corrupt := false
	for i, name := range p.Paths {
		b, err := ReadWhole(p.Store, name, CheckpointBytes)
		if errors.Is(err, ErrTooLarge) {
			corrupt = true
			continue
		}
		if err != nil {
			return best, slot, false, err
		}
		if len(b) == 0 {
			continue
		}
		var e checkpointEnvelope
		if json.Unmarshal(b, &e) != nil || e.Generation == 0 || !json.Valid(e.State) {
			corrupt = true
			continue
		}
		sum := sha256.Sum256(e.State)
		if e.Digest != hex.EncodeToString(sum[:]) {
			corrupt = true
			continue
		}
		if slot < 0 || e.Generation > best.Generation {
			best, slot = e, i
		}
	}
	if slot < 0 && corrupt {
		return best, slot, false, ErrCheckpointCorrupt
	}
	return best, slot, corrupt, nil
}

func (p CheckpointPair) Load() (json.RawMessage, uint64, bool, error) {
	e, _, recovered, err := p.read()
	return e.State, e.Generation, recovered, err
}
func (p CheckpointPair) Save(state json.RawMessage) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, state); err != nil {
		return fmt.Errorf("invalid checkpoint JSON: %w", err)
	}

	state, _ = json.Marshal(json.RawMessage(compact.Bytes()))
	old, slot, _, err := p.read()
	if err != nil {
		return err
	}
	if old.Generation == ^uint64(0) {
		return ErrCheckpointFull
	}
	sum := sha256.Sum256(state)
	next := checkpointEnvelope{Generation: old.Generation + 1, Digest: hex.EncodeToString(sum[:]), State: state}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(b) > CheckpointBytes {
		return ErrCheckpointFull
	}
	target := 0
	if slot == 0 {
		target = 1
	}
	return p.Store.Write(p.Paths[target], b)
}
