//go:build !wasm_unknown

package cq

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

type OSFS struct {
	Root     string
	MaxList  int
	Listings int
}

func (f *OSFS) List(dir string) ([]Entry, bool, error) {
	f.Listings++
	des, err := os.ReadDir(filepath.Join(f.Root, filepath.FromSlash(dir)))
	if err != nil {
		return nil, false, err
	}
	max := f.MaxList
	if max == 0 {
		max = 1024
	}
	truncated := len(des) > max
	if truncated {
		des = des[:max]
	}
	out := make([]Entry, 0, len(des))
	for _, d := range des {
		e := Entry{Name: d.Name(), Symlink: d.Type()&os.ModeSymlink != 0, Dir: d.IsDir()}
		if info, err := d.Info(); err == nil && !e.Dir {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	return out, truncated, nil
}

func (f *OSFS) Read(file string, max int64) ([]byte, error) {
	h, err := os.Open(filepath.Join(f.Root, filepath.FromSlash(file)))
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return io.ReadAll(io.LimitReader(h, max))
}

type MemCache map[string]json.RawMessage

func (m MemCache) Get(k string) (json.RawMessage, bool)  { a, ok := m[k]; return a, ok }
func (m MemCache) Put(k string, a json.RawMessage) error { m[k] = a; return nil }
