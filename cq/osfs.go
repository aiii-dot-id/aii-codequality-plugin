//go:build !wasm_unknown

package cq

import (
	"io"
	"os"
	"path/filepath"
)

// OSFS is the FS of a directory on the local disk, for tests and cmd/cqvalidate. Paths are
// slash-relative to Root; symlinks are reported, never followed.
type OSFS struct {
	Root     string
	MaxList  int // entries per listing before truncation; 0 = 1024, as the host does
	Listings int // directories listed, for measurements
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

// MemCache is an in-memory Cache.
type MemCache map[string]map[string]float64

func (m MemCache) Get(k string) (map[string]float64, bool)  { a, ok := m[k]; return a, ok }
func (m MemCache) Put(k string, a map[string]float64) error { m[k] = a; return nil }
