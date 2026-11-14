package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

type mapStore map[string][]byte

func (m mapStore) Read(p string, off int64, n int) ([]byte, bool, error) {
	b, ok := m[p]
	if !ok {
		return nil, false, errors.New("FS_NOT_FOUND")
	}
	if off >= int64(len(b)) {
		return nil, true, nil
	}
	end := min(off+int64(n), int64(len(b)))
	return append([]byte(nil), b[off:end]...), end == int64(len(b)), nil
}
func (m mapStore) Write(p string, b []byte) error  { m[p] = append([]byte(nil), b...); return nil }
func (m mapStore) Append(p string, b []byte) error { m[p] = append(m[p], b...); return nil }

type keptAttempts struct{ a cq.Attempt }

func (k *keptAttempts) Load() (cq.Attempt, error) { return k.a, nil }
func (k *keptAttempts) Save(a cq.Attempt) error   { k.a = a; return nil }
func (k *keptAttempts) Clear() error              { k.a = cq.Attempt{}; return nil }

type workerEndedHere struct{}

type endingSend struct {
	inner    poster
	mark     string
	answered bool
	endings  int
}

func (e *endingSend) send(url string, o *sdk.HTTPOptions) (sdk.HTTPResult, error) {
	if strings.Contains(string(o.Body), e.mark) {
		if e.answered {
			e.endings++
			panic(workerEndedHere{})
		}
		e.answered = true
	}
	return e.inner(url, o)
}

func TestR4ARuntimeSaveKeepsTheNewestBoundary(t *testing.T) {
	cfg := config{backend: backendLocal, localBase: "https://fixture.invalid", model: "fixed", ints: defaultInts()}
	dir := t.TempDir()
	good := func(name string) string {
		return "package p\n\n// " + name + " returns x doubled.\nfunc " + name + "(x int) int {\n\treturn x * 2\n}\n"
	}
	files := map[string]string{
		"a.go": good("A"),
		"b.go": "package p\n\n// EndsTheWorker is the function no step gets through.\nfunc EndsTheWorker(x int) int {\n\treturn x + 1\n}\n",
		"c.go": good("C"),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs := &cq.OSFS{Root: dir}
	m := mapStore{"journal": nil, "cp0": nil, "cp1": nil}
	pair := cq.CheckpointPair{Store: m, Paths: [2]string{"cp0", "cp1"}}
	first, err := cq.NewScan(fs, "s", ".", "", nil, nil, false, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := scanBinding(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first.Runtime, _ = json.Marshal(binding)
	initial, _ := json.Marshal(first)
	load := func() *cq.Scan {
		t.Helper()
		var s cq.Scan
		if err := json.Unmarshal(initial, &s); err != nil {
			t.Fatal(err)
		}
		state, _, _, err := pair.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(state) > 0 {
			if err := json.Unmarshal(state, &s); err != nil {
				t.Fatal(err)
			}
		}
		return &s
	}
	at := &keptAttempts{}
	end := &endingSend{inner: (&capture{model: "fixed"}).send, mark: "EndsTheWorker"}
	const most = 8
	ended, stopped := 0, false
	var last *cq.Scan
	for i := 0; i <= most; i++ {
		s := load()
		last = s
		if s.Status != "running" {
			stopped = true
			break
		}
		if i == most {
			break
		}
		s.Progress = &cq.JournalProgress{Journal: &cq.Journal{Store: m, Path: "journal"}, Offset: s.ProgressOffset, IntentOffset: s.IntentOffset}
		s.Attempts = at
		save := func() error {
			b, err := json.Marshal(s)
			if err != nil {
				return err
			}
			return pair.Save(b)
		}
		s.Checkpoint = save
		j, err := newJudge(cfg, 200)
		if err != nil {
			t.Fatal(err)
		}
		j.writeRuntimeCheckpoint = func(b []byte) error { return pair.Save(b) }
		if err = j.bindScan(cfg, s, int64(1000*(i+1))); err != nil {
			t.Fatal(err)
		}
		j.send = end.send
		died := func() (died bool) {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(workerEndedHere); !ok {
						panic(r)
					}
					died = true
				}
			}()
			if _, err := s.Step(tableT(t), fs, j, cq.MemCache{}, 200, 400); err != nil {
				t.Fatal(err)
			}
			return false
		}()
		if died {
			ended++
			if ended == 1 {

				r := load()
				if len(r.Pending) == 0 || r.Pending[0].Name != "b.go" {
					t.Errorf("after the first ending the scan loads with %d files seen, pending %v, stack %v; want it at b.go, with a.go behind it", r.FilesSeen, r.Pending, r.Stack)
				}
				if at.a.Ref != "b.go" || !at.a.Live {
					t.Errorf("after the first ending the record is %+v; want it live on b.go", at.a)
				}
			}
			continue
		}
		if err := save(); err != nil {
			t.Fatal(err)
		}
		if err := s.Settle(); err != nil {
			t.Fatal(err)
		}
	}
	if !stopped || last.Status != "done" {
		t.Errorf("the scan is %q after %d steps and %d endings; want it done", last.Status, most, ended)
	}
	if ended != cq.MaxAttempts {
		t.Errorf("the worker was ended %d times on b.go; want %d and then the file refused by name", ended, cq.MaxAttempts)
	}
	if _, err := cq.FoldJournal(&cq.Journal{Store: m, Path: "journal"}, func(*cq.FileRun) error { return nil }); err != nil {
		t.Errorf("the journal is not one the report reads: %v", err)
	}
	if len(last.NotFinished) != 1 || last.NotFinished[0] != "b.go" || last.FilesJudged != 2 {
		t.Errorf("named %v, judged %d of %d seen; want b.go named and 2 judged", last.NotFinished, last.FilesJudged, last.FilesSeen)
	}
	if at.a != (cq.Attempt{}) {
		t.Errorf("the record is left at %+v after the scan ended", at.a)
	}
}
