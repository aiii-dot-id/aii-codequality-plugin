package cq

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
)

type Store interface {
	Read(path string, offset int64, n int) (data []byte, eof bool, err error)
	Write(path string, data []byte) error
	Append(path string, data []byte) error
}

const IOPage = 512 << 10

func ReadWhole(s Store, path string, max int64) ([]byte, error) {
	b, complete, err := ReadPrefix(s, path, max)
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, ErrTooLarge
	}
	return b, nil
}

//go:noinline
func ReadPrefix(s Store, path string, max int64) ([]byte, bool, error) {
	var out []byte
	for {
		want := max + 1 - int64(len(out))
		if want <= 0 {
			return out[:max], false, nil
		}
		n := IOPage
		if want < int64(n) {
			n = int(want)
		}
		if len(out) >= 128<<10 {
			runtime.GC()
		}
		b, eof, err := readStorePage(s, path, int64(len(out)), n)
		if err != nil {
			return nil, false, err
		}

		if out == nil && !eof && max >= 128<<10 && max <= 1<<20 {
			out = make([]byte, 0, int(max)+1)
		}
		out = append(out, b...)
		if int64(len(out)) > max {
			return out[:max], false, nil
		}
		if eof {
			return out, true, nil
		}
		if len(b) == 0 {
			return nil, false, fmt.Errorf("read of %s returned nothing at %d before its end: %w", path, len(out), io.ErrNoProgress)
		}
	}
}

//go:noinline
func readStorePage(s Store, path string, offset int64, n int) ([]byte, bool, error) {
	return s.Read(path, offset, n)
}

func WholeLines(b []byte) []byte {
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		return b[:i+1]
	}
	return nil
}

const CacheShards = 256

const cachePage = 64 << 10

const cacheWrite = 192 << 10

var ErrCacheFull = errors.New("the answer is larger than the cache keeps")

var ErrNotFound = errors.New("no such folder")

type Lister interface {
	List(dir string) ([]Entry, bool, error)
}

type Publisher interface {
	PublishStaged(path, staged, sha256Hex string) error
}

type ShardCache struct {
	S        Store
	MaxBytes int64
	sizes    map[string]int64
	earlier  map[string]bool
	unknown  error
	listed   bool
}

func line(k string, a json.RawMessage) []byte {
	b, _ := json.Marshal(map[string]any{"k": k, "a": a})
	return append(b, '\n')
}

func (c *ShardCache) ShardBytes() int64 { return max(c.MaxBytes/CacheShards, 4<<10) }

func shardName(key string) string { return "cache/" + key[:2] + ".jsonl" }

func earlierName(key string) string { return "cache/" + key[:1] + ".jsonl" }

func shardStem(name string) string {
	stem, ok := strings.CutSuffix(name, ".jsonl")
	if !ok || !isHex(stem) {
		return ""
	}
	return stem
}

func Shard(name string) bool { return len(shardStem(name)) == 2 }

func EarlierShard(name string) bool { return len(shardStem(name)) == 1 }

func (c *ShardCache) list() {
	if c.listed {
		return
	}
	c.listed = true
	c.sizes, c.earlier = map[string]int64{}, map[string]bool{}
	l, ok := c.S.(Lister)
	if !ok {
		c.unknown = errors.New("the cache's store cannot list its folder")
		return
	}
	entries, cut, err := l.List("cache")
	switch {
	case errors.Is(err, ErrNotFound):
		return
	case err != nil:
		c.unknown = fmt.Errorf("the cache folder could not be listed: %w", err)
		return
	case cut:
		c.unknown = errors.New("the cache folder's listing was cut short")
		return
	}
	for _, e := range entries {
		switch {
		case e.Dir:
		case Shard(e.Name):
			c.sizes["cache/"+e.Name] = e.Size
		case EarlierShard(e.Name):
			c.earlier["cache/"+e.Name] = true
		}
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (c *ShardCache) stream(name string, page int, visit func(line []byte)) (int64, error) {
	longest := int(max(c.ShardBytes()/2, 1<<20))
	var carry []byte
	skipping := false
	var off int64
	for {
		b, eof, err := c.S.Read(name, off, page)
		if err != nil {
			return off, err
		}
		off += int64(len(b))
		read := len(b)
		for len(b) > 0 {
			i := bytes.IndexByte(b, '\n')
			if i < 0 {
				if !skipping && len(carry)+len(b) > longest {
					carry, skipping = carry[:0], true
				}
				if !skipping {
					carry = append(carry, b...)
				}
				break
			}
			if !skipping {
				if len(carry) > 0 {
					carry = append(carry, b[:i]...)
					visit(carry)
					carry = carry[:0]
				} else {
					visit(b[:i])
				}
			}
			skipping = false
			b = b[i+1:]
		}
		if eof {
			return off, nil
		}
		if read == 0 {
			return off, fmt.Errorf("nothing was returned at %d before the end: %w", off, io.ErrNoProgress)
		}
	}
}

func (c *ShardCache) Get(key string) (json.RawMessage, bool, error) {
	c.list()
	if c.unknown != nil {
		return nil, false, c.unknown
	}
	name := shardName(key)
	if _, there := c.sizes[name]; there {
		found, size, err := c.find(name, key)
		if err != nil {
			return nil, false, err
		}
		c.sizes[name] = size
		if found != nil {
			return found, true, nil
		}
	}
	old := earlierName(key)
	if !c.earlier[old] {
		return nil, false, nil
	}
	found, _, err := c.find(old, key)
	if err != nil {
		return nil, false, err
	}
	return found, found != nil, nil
}

func (c *ShardCache) find(name, key string) (json.RawMessage, int64, error) {
	needle := []byte(`"k":` + strconv.Quote(key))
	var found json.RawMessage
	size, err := c.stream(name, cachePage, func(ln []byte) {
		if !bytes.Contains(ln, needle) {
			return
		}
		var e struct {
			K string          `json:"k"`
			A json.RawMessage `json:"a"`
		}
		if json.Unmarshal(ln, &e) == nil && e.K == key {
			found = append(json.RawMessage(nil), e.A...)
		}
	})
	if err != nil {
		return nil, size, fmt.Errorf("%s was not read to its end: %w", name, err)
	}
	return found, size, nil
}

func (c *ShardCache) Put(key string, a json.RawMessage) error {
	ln := line(key, a)
	limit := c.ShardBytes()
	if int64(len(ln)) > limit/2 {
		return ErrCacheFull
	}
	c.list()
	if c.unknown != nil {
		return c.unknown
	}

	name := shardName(key)
	size := c.sizes[name]
	if size+int64(len(ln)) > limit {
		kept, err := c.compact(name, size, limit/2-int64(len(ln)))
		if err != nil {
			return err
		}
		size = kept
	}
	if err := c.S.Append(name, ln); err != nil {

		return err
	}
	c.sizes[name] = size + int64(len(ln))
	return nil
}

func (c *ShardCache) compact(name string, size, budget int64) (int64, error) {
	pub, ok := c.S.(Publisher)
	if !ok {
		return 0, errors.New("the cache's store cannot replace a file in one step")
	}
	staged := name + ".next"
	start := max(size-max(budget, 0), 0)

	off := max(start-1, 0)
	skip := start > 0
	sum := sha256.New()
	var kept int64
	var buf []byte
	written := false
	flush := func() error {
		var err error
		if !written {
			err = c.S.Write(staged, buf)
			written = true
		} else if len(buf) > 0 {
			err = c.S.Append(staged, buf)
		}
		sum.Write(buf)
		kept += int64(len(buf))
		buf = buf[:0]
		return err
	}
	for {
		b, eof, err := c.S.Read(name, off, cachePage)
		if err != nil {
			return 0, err
		}
		off += int64(len(b))
		read := len(b)
		if skip {
			if i := bytes.IndexByte(b, '\n'); i >= 0 {
				b, skip = b[i+1:], false
			} else {
				b = nil
			}
		}
		if eof {

			if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
				b = b[:i+1]
			} else {
				b = nil
			}
		}
		for len(b) > 0 {
			n := min(len(b), cacheWrite-len(buf))
			buf = append(buf, b[:n]...)
			b = b[n:]
			if len(buf) >= cacheWrite {
				if err := flush(); err != nil {
					return 0, err
				}
			}
		}
		if eof {
			break
		}
		if read == 0 {
			return 0, fmt.Errorf("the rewrite of %s read nothing at %d before its end: %w", name, off, io.ErrNoProgress)
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}
	if err := pub.PublishStaged(name, staged, hex.EncodeToString(sum.Sum(nil))); err != nil {
		return 0, err
	}
	c.sizes[name] = kept
	runtime.GC()
	return kept, nil
}
