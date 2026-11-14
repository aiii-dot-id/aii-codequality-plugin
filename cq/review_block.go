package cq

import (
	"bytes"
	"encoding/json"
)

const (
	pageHead = 1024
	pageTail = 4096
)

const entriesMark = `"entries":[]`

type pageBlock struct {
	buf    []byte
	rows   int
	closed bool
}

func newPageBlock(pageBytes int) *pageBlock {
	b := &pageBlock{buf: make([]byte, pageHead, pageHead+pageBytes+pageTail)}
	b.buf = append(b.buf, '[')
	return b
}

func (b *pageBlock) add(entry []byte) {
	if b.rows > 0 {
		b.buf = append(b.buf, ',')
	}
	b.buf = append(b.buf, entry...)
	b.rows++
}

func (b *pageBlock) entries() json.RawMessage {
	if !b.closed {
		b.buf = append(b.buf, ']')
		b.closed = true
	}
	return json.RawMessage(b.buf[pageHead:])
}

func (p ReviewPage) JSON() ([]byte, error) {
	b := p.block
	if b == nil || !b.closed || len(p.Entries) != len(b.buf)-pageHead || &p.Entries[0] != &b.buf[pageHead] {
		return json.Marshal(p)
	}
	shell := p
	shell.Entries = json.RawMessage("[]")
	s, err := json.Marshal(shell)
	if err != nil {
		return nil, err
	}
	i := bytes.Index(s, []byte(entriesMark))
	if i < 0 || bytes.Count(s, []byte(entriesMark)) != 1 {
		return json.Marshal(p)
	}
	head, tail := s[:i+len(entriesMark)-len("[]")], s[i+len(entriesMark):]
	if len(head) > pageHead || len(tail) > cap(b.buf)-len(b.buf) {
		return json.Marshal(p)
	}
	start := pageHead - len(head)
	copy(b.buf[start:pageHead], head)
	out := append(b.buf, tail...)
	return out[start:], nil
}
