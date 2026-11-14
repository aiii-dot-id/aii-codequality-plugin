package cq

import (
	"errors"
	"strings"
	"testing"
)

type recMem struct {
	data  []byte
	chunk int
}

func (m *recMem) Read(_ string, off int64, n int) ([]byte, bool, error) {
	if off >= int64(len(m.data)) {
		return nil, true, nil
	}
	rest := m.data[off:]
	if m.chunk > 0 && n > m.chunk {
		n = m.chunk
	}
	if n >= len(rest) {
		return append([]byte(nil), rest...), true, nil
	}
	return append([]byte(nil), rest[:n]...), false, nil
}

func (m *recMem) Write(_ string, d []byte) error {
	m.data = append([]byte(nil), d...)
	return nil
}

func (m *recMem) Append(_ string, d []byte) error {
	m.data = append(m.data, d...)
	return nil
}

func recMust(t *testing.T, body string) []byte {
	t.Helper()
	l, err := RecLine([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func recFold(t *testing.T, m *recMem) (string, RecStats) {
	t.Helper()
	var got []string
	st, err := FoldRecs(m, "f", func(b []byte) error {
		got = append(got, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(got, "|"), st
}

const (
	recA = `{"n":"A"}`
	recB = `{"n":"B","pad":"xxxxxxxxxxxxxxxx"}`
	recC = `{"n":"C"}`
)

func TestRecLogARecordAfterATornPrefixIsStillRead(t *testing.T) {
	for _, chunk := range []int{0, 7} {
		m := &recMem{chunk: chunk}
		m.Append("f", recMust(t, recA))
		b := recMust(t, recB)
		m.Append("f", b[:len(b)-9])
		m.Append("f", recMust(t, recC))
		got, st := recFold(t, m)
		if got != recA+"|"+recC {
			t.Fatalf("chunk %d: read %q, want A then C: the record after a torn prefix is lost", chunk, got)
		}
		if st.Records != 2 || st.Torn != 1 {
			t.Fatalf("chunk %d: %d records and %d torn, want 2 and 1", chunk, st.Records, st.Torn)
		}
	}
}

func TestRecLogADamagedRecordIsSkippedAndCounted(t *testing.T) {
	a, b, c := recMust(t, recA), recMust(t, recB), recMust(t, recC)
	b[1+recSumHex+1+3] ^= 1
	m := &recMem{}
	m.Append("f", a)
	m.Append("f", b)
	m.Append("f", c)
	got, st := recFold(t, m)
	if got != recA+"|"+recC {
		t.Fatalf("read %q, want A then C: a body that does not match its checksum was read", got)
	}
	if st.Torn != 1 {
		t.Fatalf("%d torn, want 1", st.Torn)
	}
}

func TestRecLogALineOverTheBoundIsOneTornRecord(t *testing.T) {
	for _, chunk := range []int{0, 1000} {
		m := &recMem{chunk: chunk}
		m.Append("f", recMust(t, recA))
		m.Append("f", []byte("\n"+strings.Repeat("x", RecMaxBytes+10)+"\n"))
		m.Append("f", recMust(t, recC))
		got, st := recFold(t, m)
		if got != recA+"|"+recC || st.Torn != 1 {
			t.Fatalf("chunk %d: read %q with %d torn, want A then C and 1", chunk, got, st.Torn)
		}
	}
}

func TestRecLogAVerifiedLastLineIsARecordAndStaysOne(t *testing.T) {
	m := &recMem{}
	m.Append("f", recMust(t, recA))
	c := recMust(t, recC)
	m.Append("f", c[:len(c)-1])
	if got, st := recFold(t, m); got != recA+"|"+recC || st.Torn != 0 {
		t.Fatalf("read %q with %d torn, want A then C and 0", got, st.Torn)
	}
	m.Append("f", recMust(t, recB))
	if got, st := recFold(t, m); got != recA+"|"+recC+"|"+recB || st.Torn != 0 {
		t.Fatalf("after an append: read %q with %d torn, want A, C, B and 0", got, st.Torn)
	}
}

func TestRecLogRefusesABodyThatIsNotOneLine(t *testing.T) {
	for name, body := range map[string]string{
		"empty":   "",
		"newline": "a\nb",
		"large":   strings.Repeat("x", RecMaxBytes),
	} {
		if _, err := RecLine([]byte(body)); !errors.Is(err, ErrRecBody) {
			t.Errorf("%s: %v, want ErrRecBody", name, err)
		}
	}
}
