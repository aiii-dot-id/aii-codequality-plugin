package cq

import (
	"bytes"
	"encoding/json"
	"errors"
	"runtime"
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
		if eof || len(b) == 0 {
			return out, true, nil
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

const CacheShards = 16

var ErrCacheFull = errors.New("the answer is larger than the cache keeps")

type ShardCache struct {
	S        Store
	MaxBytes int64
	shards   map[string]*shard
}

type entry struct {
	k string
	a json.RawMessage
}

type shard struct {
	entries []entry
	index   map[string]int
	size    int64
	over    bool
}

func line(k string, a json.RawMessage) []byte {
	b, _ := json.Marshal(map[string]any{"k": k, "a": a})
	return append(b, '\n')
}

func (c *ShardCache) ShardBytes() int64 { return max(c.MaxBytes/CacheShards, 64<<10) }

func (c *ShardCache) shard(key string) (string, *shard) {
	name := "cache/" + key[:1] + ".jsonl"
	if c.shards == nil {
		c.shards = map[string]*shard{}
	}
	if sh, ok := c.shards[name]; ok {
		return name, sh
	}
	sh := &shard{index: map[string]int{}}
	if b, complete, err := ReadPrefix(c.S, name, c.ShardBytes()); err == nil {
		if !complete {
			sh.over = true
			b = WholeLines(b)
		}
		sh.size = int64(len(b))
		for _, ln := range strings.Split(string(b), "\n") {
			var e struct {
				K string          `json:"k"`
				A json.RawMessage `json:"a"`
			}
			if json.Unmarshal([]byte(ln), &e) == nil && e.K != "" {
				sh.index[e.K] = len(sh.entries)
				sh.entries = append(sh.entries, entry{e.K, e.A})
			}
		}
	}
	c.shards[name] = sh
	return name, sh
}

func (c *ShardCache) Get(key string) (json.RawMessage, bool) {
	_, sh := c.shard(key)
	i, ok := sh.index[key]
	if !ok {
		return nil, false
	}
	return sh.entries[i].a, true
}

func (c *ShardCache) Put(key string, a json.RawMessage) error {
	name, sh := c.shard(key)
	ln := line(key, a)
	limit := c.ShardBytes()
	if int64(len(ln)) > limit/2 {
		return ErrCacheFull
	}
	if sh.over || sh.size+int64(len(ln)) > limit {
		if err := c.compact(name, sh, limit/2-int64(len(ln))); err != nil {
			return err
		}
	}
	if err := c.S.Append(name, ln); err != nil {
		return err
	}
	sh.index[key] = len(sh.entries)
	sh.entries = append(sh.entries, entry{key, a})
	sh.size += int64(len(ln))
	return nil
}

func (c *ShardCache) compact(name string, sh *shard, budget int64) error {
	var kept []entry
	seen := map[string]bool{}
	var used int64
	for i := len(sh.entries) - 1; i >= 0; i-- {
		e := sh.entries[i]
		if seen[e.k] {
			continue
		}
		seen[e.k] = true
		n := int64(len(line(e.k, e.a)))
		if used+n > budget {
			break
		}
		used += n
		kept = append(kept, e)
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	var buf []byte
	first := true
	flush := func() error {
		var err error
		if first {
			err = c.S.Write(name, buf)
			first = false
		} else if len(buf) > 0 {
			err = c.S.Append(name, buf)
		}
		buf = buf[:0]
		return err
	}
	for _, e := range kept {
		ln := line(e.k, e.a)
		if len(buf)+len(ln) > IOPage && len(buf) > 0 {
			if err := flush(); err != nil {
				return err
			}
		}
		buf = append(buf, ln...)
	}
	if err := flush(); err != nil {
		return err
	}
	sh.entries, sh.index, sh.size, sh.over = kept, map[string]int{}, used, false
	for i, e := range kept {
		sh.index[e.k] = i
	}
	return nil
}
