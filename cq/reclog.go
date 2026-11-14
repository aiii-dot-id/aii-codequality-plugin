package cq

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const RecMaxBytes = 250 << 10

const recSumHex = 16

var ErrRecBody = errors.New("record body cannot be written as one line")

func recSum(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:recSumHex/2])
}

func RecLine(body []byte) ([]byte, error) {
	if len(body) == 0 || bytes.IndexByte(body, '\n') >= 0 {
		return nil, fmt.Errorf("%w: it is empty or holds a newline", ErrRecBody)
	}
	n := len(body) + recSumHex + 3
	if n > RecMaxBytes {
		return nil, fmt.Errorf("%w: %d bytes, and a line holds %d", ErrRecBody, n, RecMaxBytes)
	}
	out := make([]byte, 0, n)
	out = append(out, '\n')
	out = append(out, recSum(body)...)
	out = append(out, ' ')
	out = append(out, body...)
	return append(out, '\n'), nil
}

func recBody(line []byte) ([]byte, bool) {
	if len(line) < recSumHex+2 || line[recSumHex] != ' ' {
		return nil, false
	}
	body := line[recSumHex+1:]
	if recSum(body) != string(line[:recSumHex]) {
		return nil, false
	}
	return body, true
}

type RecStats struct {
	Records int

	Torn int

	Bytes int64
}

func FoldRecs(s Store, file string, visit func(body []byte) error) (RecStats, error) {
	var st RecStats
	f := recFolder{visit: visit, st: &st}
	for {
		page, eof, err := s.Read(file, st.Bytes, IOPage)
		if err != nil {
			return st, err
		}
		st.Bytes += int64(len(page))
		if err := f.feed(page); err != nil {
			return st, err
		}
		if eof {
			return st, f.close()
		}
		if len(page) == 0 {
			return st, fmt.Errorf("record file %s: a read gave nothing before the file's end", file)
		}
	}
}

type recFolder struct {
	visit func([]byte) error
	st    *RecStats
	line  []byte
	over  bool
}

func (f *recFolder) feed(page []byte) error {
	for len(page) > 0 {
		i := bytes.IndexByte(page, '\n')
		if i < 0 {
			f.keep(page)
			return nil
		}
		f.keep(page[:i])
		if err := f.close(); err != nil {
			return err
		}
		page = page[i+1:]
	}
	return nil
}

func (f *recFolder) keep(part []byte) {
	if f.over {
		return
	}
	if len(f.line)+len(part) > RecMaxBytes {
		f.over, f.line = true, f.line[:0]
		return
	}
	f.line = append(f.line, part...)
}

func (f *recFolder) close() error {
	line, over := f.line, f.over
	f.line, f.over = f.line[:0], false
	if over {
		f.st.Torn++
		return nil
	}
	if len(line) == 0 {
		return nil
	}
	body, ok := recBody(line)
	if !ok {
		f.st.Torn++
		return nil
	}
	f.st.Records++
	if f.visit == nil {
		return nil
	}
	return f.visit(body)
}
