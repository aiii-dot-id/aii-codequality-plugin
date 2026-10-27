package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	journalReadBytes          = 64 << 10
	journalEventBytes         = 250 << 10
	defaultJournalBytes int64 = 48 << 20
)

var ErrJournalCorrupt = errors.New("assessment journal is corrupt")

var ErrJournalFull = errors.New("assessment journal storage cap reached")

type JournalEvent struct {
	Record   *Record   `json:"record,omitempty"`
	Coverage *Coverage `json:"coverage,omitempty"`
}

type Journal struct {
	Store    Store
	Path     string
	MaxBytes int64
}

func (j *Journal) limit() (int64, error) {
	if j.Store == nil || j.Path == "" {
		return 0, fmt.Errorf("%w: store and path required", ErrJournalCorrupt)
	}
	if j.MaxBytes < 0 {
		return 0, fmt.Errorf("%w: negative byte limit", ErrJournalFull)
	}
	if j.MaxBytes == 0 {
		return defaultJournalBytes, nil
	}
	return j.MaxBytes, nil
}

func validateJournalEvent(e JournalEvent) error {
	if (e.Record == nil) == (e.Coverage == nil) {
		return fmt.Errorf("%w: exactly one payload required", ErrJournalCorrupt)
	}
	if r := e.Record; r != nil {
		if r.Version != RecordVersion || strings.TrimSpace(r.ID) == "" {
			return fmt.Errorf("%w: v2 record with stable nonempty ID required", ErrJournalCorrupt)
		}
	}
	if c := e.Coverage; c != nil {
		if strings.TrimSpace(c.Ref) == "" || strings.ContainsAny(c.Ref, "\x00\r\n") || (c.Kind != "file" && c.Kind != "subtree") {
			return fmt.Errorf("%w: coverage requires ref and file/subtree kind", ErrJournalCorrupt)
		}
		switch c.Disposition {
		case "complete", "pending", "unknown", "excluded", "stale":
		default:
			return fmt.Errorf("%w: invalid coverage disposition", ErrJournalCorrupt)
		}
		if c.Size < 0 || c.ExpectedChunks < 0 || c.CompletedChunks < 0 || c.CompletedChunks > c.ExpectedChunks {
			return fmt.Errorf("%w: invalid coverage counts", ErrJournalCorrupt)
		}
	}
	return nil
}

func (j *Journal) Walk(offset int64, visit func(JournalEvent) error) (end int64, err error) {
	end = offset
	limit, err := j.limit()
	if err != nil {
		return end, err
	}
	if offset < 0 {
		return end, fmt.Errorf("%w: negative offset", ErrJournalCorrupt)
	}
	if offset > limit {
		return end, ErrJournalFull
	}
	if offset > 0 {
		b, _, err := j.Store.Read(j.Path, offset-1, 1)
		if err != nil {
			return end, fmt.Errorf("read journal boundary: %w", err)
		}
		if len(b) != 1 || b[0] != '\n' {
			return end, fmt.Errorf("%w: offset is not an event boundary", ErrJournalCorrupt)
		}
	}
	var line []byte
	pos := offset
	for {

		want := int64(journalReadBytes)
		if remaining := limit - pos; remaining < want {
			want = remaining + 1
		}
		b, eof, err := j.Store.Read(j.Path, pos, int(want))
		if err != nil {
			return end, fmt.Errorf("read journal at %d: %w", pos, err)
		}
		if len(b) > int(want) {
			return end, fmt.Errorf("%w: store exceeded read bound", ErrJournalCorrupt)
		}
		if int64(len(b)) > limit-pos {
			return end, ErrJournalFull
		}
		if len(b) == 0 && !eof {
			return end, fmt.Errorf("read journal at %d: %w", pos, io.ErrNoProgress)
		}
		pos += int64(len(b))
		for len(b) > 0 {
			n := bytes.IndexByte(b, '\n')
			take := len(b)
			if n >= 0 {
				take = n + 1
			}
			if len(line)+take > journalEventBytes {
				return end, fmt.Errorf("%w: encoded event exceeds %d bytes", ErrJournalCorrupt, journalEventBytes)
			}
			line = append(line, b[:take]...)
			b = b[take:]
			if n < 0 {
				break
			}
			var event JournalEvent
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&event); err != nil {
				return end, fmt.Errorf("%w: event at %d: %v", ErrJournalCorrupt, end, err)
			}

			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return end, fmt.Errorf("%w: trailing data at %d", ErrJournalCorrupt, end)
			}
			if err := validateJournalEvent(event); err != nil {
				return end, err
			}
			if visit != nil {
				if err := visit(event); err != nil {
					return end, err
				}
			}
			end += int64(len(line))
			line = line[:0]
		}
		if eof {
			if len(line) != 0 {
				return end, fmt.Errorf("%w: truncated tail at %d", ErrJournalCorrupt, end)
			}
			return end, nil
		}
	}
}

func (j *Journal) Append(event JournalEvent) (end int64, err error) {
	end, err = j.Walk(0, nil)
	if err != nil {
		return end, err
	}
	return j.appendAt(end, event)
}

func (j *Journal) appendAt(end int64, event JournalEvent) (int64, error) {
	limit, err := j.limit()
	if err != nil {
		return 0, err
	}
	if err := validateJournalEvent(event); err != nil {
		return 0, err
	}
	b, err := json.Marshal(event)
	if err != nil {
		return 0, fmt.Errorf("encode journal event: %w", err)
	}
	if len(b) >= journalEventBytes {
		return 0, ErrJournalFull
	}
	b = append(b, '\n')
	if end < 0 || end > limit {
		return end, ErrJournalFull
	}
	if int64(len(b)) > limit-end {
		return end, ErrJournalFull
	}
	if err := j.Store.Append(j.Path, b); err != nil {
		return end, fmt.Errorf("append journal at %d: %w", end, err)
	}
	return end + int64(len(b)), nil
}
