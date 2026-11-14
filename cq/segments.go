package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"runtime"
	"strings"
	"unicode/utf8"
	"unsafe"
)

const SegmentBytes = 192 << 10

const MaxSourceBytes = 1 << 20

const LargerThanSource = "larger_than_1_mib"

func lineTops(source string, l *Lang, depth0 int) (tops, depths []int, end int) {
	depth := depth0
	quote := byte(0)
	raw := false
	for i := 0; i < len(source); {
		c := source[i]
		if quote != 0 {
			if c == '\\' && !raw && i+1 < len(source) {
				i += 2
				continue
			}
			if c == quote {
				quote = 0
			} else if c == '\n' && quote != '`' && l.Key != "python" {
				quote = 0
				tops, depths = append(tops, i+1), append(depths, depth)
			}
			i++
			continue
		}
		if l.Key == "python" && (strings.HasPrefix(source[i:], `"""`) || strings.HasPrefix(source[i:], `'''`)) {
			n := strings.Index(source[i+3:], source[i:i+3])
			if n < 0 {
				return tops, depths, depth
			}
			i += 3 + n + 3
			continue
		}
		matched := false
		if l.Key != "python" {
			for _, pair := range l.Blocks {
				if pair[0] == "" || pair[1] == "" || !strings.HasPrefix(source[i:], pair[0]) {
					continue
				}
				n := strings.Index(source[i+len(pair[0]):], pair[1])
				if n < 0 {
					return tops, depths, depth
				}
				i += len(pair[0]) + n + len(pair[1])
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if l.LineComment != "" && strings.HasPrefix(source[i:], l.LineComment) {
			n := strings.IndexByte(source[i:], '\n')
			if n < 0 {
				return tops, depths, depth
			}
			i += n
			continue
		}
		switch {
		case c == '\n':
			tops, depths = append(tops, i+1), append(depths, depth)
		case c == '{':
			depth++
		case c == '}':
			depth--
		case (c == '`' && !backtickStrings[l.Key]) || (noStrings[l.Key] && (c == '"' || c == '\'')):
		case c == '\'' && charLiterals[l.Key]:
			n := charLiteral(source[i:])
			if n == 0 {
				n = 1
			}
			i += n
			continue
		case c == '"' || c == '\'' || c == '`':
			quote = c
			raw = c == '`' || l.Key == "python" && i > 0 && (source[i-1] == 'r' || source[i-1] == 'R')
		}
		i++
	}
	return tops, depths, depth
}

func goDeclStart(line string) bool {
	for _, p := range []string{"func ", "func(", "type ", "var ", "const ", "import ", "import(", "//"} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

func openKey(source string) bool {
	return strings.Count(source, "-----BEGIN ") > strings.Count(source, "-----END ")
}

func cutSegment(buf string, l *Lang, depth0 int, final bool) (n, depth int, limited bool) {
	if final && len(buf) <= SegmentBytes {
		_, _, depth = lineTops(buf, l, depth0)
		return len(buf), depth, false
	}
	window := buf[:min(len(buf), SegmentBytes)]
	tops, depths, _ := lineTops(window, l, depth0)
	best, second, third := -1, -1, -1
	for k := len(tops) - 1; k >= 0; k-- {
		at := tops[k]
		if at <= 0 || at > len(window) || openKey(window[:at]) {
			continue
		}
		line := window[at:]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		if l.Key == "go" {
			if depths[k] == 0 && goDeclStart(line) && (at < 2 || window[at-2] == '\n' || !strings.HasPrefix(line, "//")) {
				best = k
				break
			}
			if depths[k] == 0 && second < 0 {
				second = k
			}
		} else {
			if at >= 2 && window[at-2] == '\n' {
				best = k
				break
			}
			if second < 0 {
				second = k
			}
		}
		if third < 0 {
			third = k
		}
	}
	for _, k := range []int{best, second, third} {
		if k >= 0 {
			return tops[k], depths[k], false
		}
	}

	if nl := strings.LastIndexByte(window, '\n'); nl > 0 {
		return nl + 1, depth0, true
	}

	cut := len(window)
	if cut < len(buf) {
		for cut > 1 && !utf8.RuneStart(buf[cut]) {
			cut--
		}
	} else if r, size := utf8.DecodeLastRuneInString(window); r == utf8.RuneError && size <= 1 {
		for cut > 1 && !utf8.RuneStart(window[cut-1]) {
			cut--
		}
		cut--
	}
	return cut, depth0, true
}

type Digester interface {
	Digest(file string) (sha256Hex string, size int64, err error)
}

type RangeFS interface {
	ReadAt(file string, off int64, n int) ([]byte, bool, error)
}

type FileCursor struct {
	Ref     string     `json:"ref"`
	Size    int64      `json:"size"`
	Digest  string     `json:"digest"`
	Windows int        `json:"windows"`
	Segs    [][5]int64 `json:"segs"`
	Next    int        `json:"next"`
	Done    int        `json:"done,omitempty"`
	Partial int        `json:"partial,omitempty"`
	Calls   int        `json:"calls,omitempty"`
	Lines   int        `json:"lines,omitempty"`
	Top     int        `json:"top_level,omitempty"`

	Outline []OutlineItem `json:"outline,omitempty"`

	Lang  string   `json:"lang,omitempty"`
	Langs []string `json:"langs,omitempty"`

	Units []SegmentUnits `json:"units,omitempty"`
}

func (c *FileCursor) unitsAt(k int) *SegmentUnits {
	if len(c.Units) != len(c.Segs) || k >= len(c.Units) {
		return nil
	}
	return &c.Units[k]
}

var ErrNotText = errors.New("not text")

var ErrFileChanged = errors.New("the file changed after it was planned")

func segmentHash(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64())
}

func (c *FileCursor) segmentAt(fs RangeFS, file string, k int) (string, error) {
	end := c.Size
	if k+1 < len(c.Segs) {
		end = c.Segs[k+1][0]
	}
	off := c.Segs[k][0]
	b, _, err := fs.ReadAt(file, off, int(end-off))
	if err != nil {
		return "", err
	}
	if int64(len(b)) != end-off || segmentHash(string(b)) != c.Segs[k][4] {
		return "", ErrFileChanged
	}
	return unsafe.String(unsafe.SliceData(b), len(b)), nil
}

func (c *FileCursor) unchanged(fs RangeFS, file string) error {

	if d, ok := fs.(Digester); ok {
		sum, size, err := d.Digest(file)
		if err != nil {
			return err
		}
		if size != c.Size || sum != c.Digest {
			return ErrFileChanged
		}
		return nil
	}
	for k := range c.Segs {
		runtime.GC()
		if _, err := c.segmentAt(fs, file, k); err != nil {
			return err
		}
	}
	b, eof, err := fs.ReadAt(file, c.Size, 1)
	if err != nil {
		return err
	}
	if len(b) > 0 || !eof {
		return ErrFileChanged
	}
	return nil
}

func PlanFile(t *Table, fs RangeFS, file, ref, lang string, chunk int, test bool) (*FileCursor, *FileShape, error) {
	c := &FileCursor{Ref: ref}
	sb := newShapeBuilder(t, lang)
	h := sha256.New()
	var off int64
	line, window, depth, limited := 1, 0, 0, false
	head := ""
	ended := true
	for {
		runtime.GC()
		b, eof, err := fs.ReadAt(file, off, SegmentBytes)
		if err != nil {
			return nil, nil, err
		}
		if len(b) == 0 {
			if !eof {
				return nil, nil, fmt.Errorf("read %s at %d: no progress", file, off)
			}
			break
		}
		if off+int64(len(b)) > MaxSourceBytes {
			return nil, nil, ErrTooLarge
		}
		buf := string(b)
		if off == 0 {
			head = buf[:min(len(buf), 4096)]
		}
		n, d, lim := cutSegment(buf, t.Langs[lang], depth, eof)
		seg := buf[:n]
		if !utf8.ValidString(seg) || strings.IndexByte(seg, 0) >= 0 {
			return nil, nil, ErrNotText
		}

		var units *SegmentUnits
		if lang == "go" {
			p := parseGoText(seg)
			if p.err != nil {
				units = &SegmentUnits{Cuts: []int{0, len(seg)}, Boundary: "go_parse_fallback"}
			} else {
				units = &SegmentUnits{Cuts: goCuts(p, seg), Boundary: "go_declaration"}
			}
			sb.parsed = &p
		} else {
			units = unitsOf(seg, lang)
		}
		units = units.thin()
		windows, err := boundedWindowsWith(t, lang, seg, chunk, line, limited, units)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrInput, err)
		}
		_, _ = h.Write([]byte(seg))

		end := eof && n == len(buf)
		sb.add(seg, line, limited || lim && !end || depth != 0 || d != 0 && !end)
		top, _ := TopLevelDecls(seg, t.Langs[lang], depth)
		c.Top += top
		if len(seg) > 0 {
			ended = strings.HasSuffix(seg, "\n")
		}
		flag := int64(0)
		if limited {
			flag = 1
		}
		c.Segs = append(c.Segs, [5]int64{off, int64(line), int64(window), flag, segmentHash(seg)})
		c.Units = append(c.Units, *units)
		line += strings.Count(seg, "\n")
		window += len(windows)
		off += int64(n)
		depth, limited = d, lim
		if eof && n == len(buf) {
			break
		}
	}
	c.Size, c.Windows, c.Digest = off, window, hex.EncodeToString(h.Sum(nil))
	c.Lines = line - 1
	if !ended {
		c.Lines++
	}
	shape := sb.finish(c.Lines, c.Top, test)
	shape.Tagged = lang == "go" && platformTagged(ref, head)
	c.Outline = shape.Outline
	return c, &shape, nil
}

func JudgeFileProgress(t *Table, j Judge, c Cache, fs RangeFS, file, lang string, cur *FileCursor, chunk int, prior []Record,
	save func(Record) error, intent bool, segmentDone func() error, langs ...string) ([]Record, error) {
	var out []Record
	for cur.Next < len(cur.Segs) {
		k := cur.Next
		runtime.GC()
		seg, err := cur.segmentAt(fs, file, k)
		if err != nil {
			return out, err
		}
		windows, err := boundedWindowsWith(t, lang, seg, chunk, int(cur.Segs[k][1]), cur.Segs[k][3] == 1, cur.unitsAt(k))
		seg = ""
		runtime.GC()
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrInput, err)
		}
		first := int(cur.Segs[k][2])
		next := 0
		windowsOf := func() (int, SourceWindow, bool, error) {
			if next == len(windows) {
				return 0, SourceWindow{}, false, nil
			}
			next++
			return first + next - 1, windows[next-1], true, nil
		}
		recs, err := judgeWindows(t, j, c, cur.Ref, lang, cur.Digest, cur.Windows, windowsOf, chunk, prior, save, intent, langs)
		runtime.GC()
		out = append(out, recs...)
		for _, r := range recs {
			cur.Calls += r.Calls
		}
		if err != nil {
			return out, err
		}
		done, partial := windowsJudged(recs)
		cur.Done += done
		cur.Partial += partial
		cur.Next = k + 1
		if err := segmentDone(); err != nil {
			return out, err
		}
	}
	return out, nil
}

func windowsJudged(recs []Record) (done, partial int) {
	docs, incomplete := map[int]bool{}, map[int]bool{}
	for _, r := range recs {
		if r.Battery == IntentBattery || r.Battery == SlopBattery || r.Battery == OutlineBattery {
			continue
		}
		switch {
		case !batteryAnswered(r):
			incomplete[r.Chunk] = true
		case r.Battery == "docs":
			docs[r.Chunk] = true
		}
	}
	for k := range incomplete {
		delete(docs, k)
	}
	return len(docs), len(incomplete)
}
