//go:build !wasm_unknown

// cqvalidate runs the plugin's core (package cq) against Jev over local corpora, to validate the
// plugin's judgments. It binds cq to the local disk and to Jev over HTTPS; the key is read from
// JEV_API_KEY and never printed.
//
//	cqvalidate grid -manifest samples.json -out records.jsonl
//	    judges every sample of a manifest and writes one record per judgment (index, band,
//	    findings, answers, the sample's labels). The manifest is {"samples": [...]}, each sample
//	    {"kind": "expert", "id", "lang", "text", "labels"} or {"kind": "dose", "id", "lang",
//	    "doses": {"0": {"text", "injected": [item ids]}, "4": {...}}}. The run resumes.
//	cqvalidate scan -root /path/to/repo -max-files 400 -out scan.json
//	    walks a tree with the plugin's own resumable Step (six calls per step, as one invoke),
//	    then walks it again to count the calls an unchanged tree costs, and writes the summary.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
)

// jev is a cq.Judge over HTTPS. Unlike the plugin, which leaves a retry to the next invoke, it
// retries transient failures itself so a long validation run completes.
type jev struct {
	url, key, model string
	client          http.Client
	calls, tokens   atomic.Int64
	retries         atomic.Int64
}

func (j *jev) Model() string { return j.model }

func (j *jev) Ask(state, questions map[string]any) (map[string]float64, error) {
	body, err := cq.RequestBody(j.model, state, questions)
	if err != nil {
		return nil, err
	}
	delay := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequest("POST", j.url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+j.key)
		res, err := j.client.Do(req)
		status := 0
		var out []byte
		if err == nil {
			status = res.StatusCode
			out, _ = io.ReadAll(res.Body)
			res.Body.Close()
		}
		if status == 200 {
			j.calls.Add(1)
			var u struct {
				Usage struct {
					In int64 `json:"input_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(out, &u) == nil {
				j.tokens.Add(u.Usage.In)
			}
			return cq.ParseAnswers(out)
		}
		if status == 403 && !json.Valid(out) {
			return nil, fmt.Errorf("%w: status 403 from the edge", cq.ErrRefused)
		}
		transient := err != nil || status == 429 || status >= 500
		if !transient || attempt == 6 {
			msg := strings.ReplaceAll(string(out), j.key, "<key>")
			if err != nil {
				msg = strings.ReplaceAll(err.Error(), j.key, "<key>")
			}
			return nil, fmt.Errorf("%w: status %d: %.300s", cq.ErrJudge, status, msg)
		}
		j.retries.Add(1)
		time.Sleep(delay)
		delay *= 2
	}
}

func newJev(model string) *jev {
	key := os.Getenv("JEV_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "cqvalidate: JEV_API_KEY is not set")
		os.Exit(2)
	}
	return &jev{url: "https://api.typesafe.ai/v1/systemone", key: key, model: model, client: http.Client{Timeout: 60 * time.Second}}
}

// syncCache is a MemCache safe for the parallel workers.
type syncCache struct {
	mu sync.Mutex
	m  cq.MemCache
}

func (c *syncCache) Get(k string) (map[string]float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.Get(k)
}

func (c *syncCache) Put(k string, a map[string]float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.Put(k, a)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cqvalidate grid|scan [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "grid":
		grid(os.Args[2:])
	case "scan":
		scan(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: cqvalidate grid|scan [flags]")
		os.Exit(2)
	}
}

// ---- grid ---------------------------------------------------------------------------------------

type sample struct {
	Kind   string                     `json:"kind"`
	Lang   string                     `json:"lang"`
	ID     string                     `json:"id"`
	Text   string                     `json:"text"`
	Labels map[string]int             `json:"labels"`
	Tools  map[string]any             `json:"tools"`
	Doses  map[string]json.RawMessage `json:"doses"`
}

type job struct {
	key, id, kind, lang, text string
	extra                     map[string]any
}

func grid(args []string) {
	fs := flag.NewFlagSet("grid", flag.ExitOnError)
	manifest := fs.String("manifest", "", "the samples to judge")
	outPath := fs.String("out", "", "records, one JSON per line")
	keysPath := fs.String("keys", "", "optional JSON file whose \"keys\" restrict the run")
	workers := fs.Int("workers", 4, "parallel Jev requests")
	model := fs.String("model", "jev-1.13.0", "Jev model id")
	fs.Parse(args)
	var m struct {
		Samples []sample `json:"samples"`
	}
	b, err := os.ReadFile(*manifest)
	must(err)
	must(json.Unmarshal(b, &m))
	var jobs []job
	for _, s := range m.Samples {
		if s.Kind == "dose" {
			for d, raw := range s.Doses {
				var v struct {
					Text     string   `json:"text"`
					Injected []string `json:"injected"`
				}
				must(json.Unmarshal(raw, &v))
				var dose int
				fmt.Sscan(d, &dose)
				jobs = append(jobs, job{s.ID + "@" + d, s.ID, s.Kind, s.Lang, v.Text, map[string]any{"dose": dose, "injected": v.Injected}})
			}
		} else {
			jobs = append(jobs, job{s.ID, s.ID, s.Kind, s.Lang, s.Text, map[string]any{"labels": s.Labels, "tools": s.Tools}})
		}
	}
	if *keysPath != "" {
		var k struct {
			Keys []string `json:"keys"`
		}
		kb, err := os.ReadFile(*keysPath)
		must(err)
		must(json.Unmarshal(kb, &k))
		want := map[string]bool{}
		for _, x := range k.Keys {
			want[x] = true
		}
		kept := jobs[:0]
		for _, jb := range jobs {
			if want[jb.key] {
				kept = append(kept, jb)
			}
		}
		jobs = kept
	}
	done := map[string]bool{}
	if f, err := os.ReadFile(*outPath); err == nil {
		for _, ln := range bytes.Split(f, []byte("\n")) {
			var r map[string]any
			if json.Unmarshal(ln, &r) == nil && r["error"] == nil {
				done[r["key"].(string)] = true
			}
		}
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].key < jobs[b].key })
	var todo []job
	for _, jb := range jobs {
		if !done[jb.key] {
			todo = append(todo, jb)
		}
	}
	fmt.Fprintf(os.Stderr, "grid: %d judgments, %d already done, %d to run\n", len(jobs), len(done), len(todo))
	t, err := cq.LoadTable()
	must(err)
	j := newJev(*model)
	cache := &syncCache{m: cq.MemCache{}}
	out, err := os.OpenFile(*outPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	must(err)
	defer out.Close()
	var mu sync.Mutex
	ch := make(chan job)
	var wg sync.WaitGroup
	var n atomic.Int64
	start := time.Now()
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for jb := range ch {
				rec := map[string]any{"key": jb.key, "id": jb.id, "kind": jb.kind, "lang": jb.lang}
				for k, v := range jb.extra {
					rec[k] = v
				}
				t0 := time.Now()
				recs, err := cq.JudgeText(t, j, cache, jb.id, jb.lang, jb.text)
				if errors.Is(err, cq.ErrRefused) {
					rec["refused"] = true
				}
				if err != nil {
					rec["error"] = err.Error()
				} else {
					fold(rec, recs)
					rec["seconds"] = time.Since(t0).Seconds()
				}
				line, _ := json.Marshal(rec)
				mu.Lock()
				out.Write(append(line, '\n'))
				mu.Unlock()
				if k := n.Add(1); k%20 == 0 {
					fmt.Fprintf(os.Stderr, "grid: %d/%d in %.1f min\n", k, len(todo), time.Since(start).Minutes())
				}
			}
		}()
	}
	for _, jb := range todo {
		ch <- jb
	}
	close(ch)
	wg.Wait()
	fmt.Fprintf(os.Stderr, "grid: calls %d, input tokens %d, retries %d, %.1f min\n", j.calls.Load(), j.tokens.Load(), j.retries.Load(), time.Since(start).Minutes())
}

// fold turns a file's chunk records into one record: the line-weighted index, the band of
// that index, and the union of findings.
func fold(rec map[string]any, recs []cq.Record) {
	lines, weighted, calls := 0, 0, 0
	seen := map[string]bool{}
	findings := []string{}
	abstained := []string{}
	answers := map[string]float64{}
	uncertain := false
	for _, r := range recs {
		calls += r.Calls
		if r.Band == "" {
			continue
		}
		n := r.Lines[1] - r.Lines[0] + 1
		lines += n
		weighted += n * r.Index
		for _, f := range r.Findings {
			if !seen[f.ID] {
				seen[f.ID] = true
				findings = append(findings, f.ID)
			}
		}
		abstained = append(abstained, r.Abstained...)
		for k, v := range r.Answers {
			if _, ok := answers[k]; !ok {
				answers[k] = v
			}
		}
		uncertain = uncertain || r.Uncertain
	}
	index := 100
	if lines > 0 {
		index = weighted / lines
	}
	rec["index"], rec["band"], rec["findings"], rec["abstained"] = index, cq.Band(index), findings, abstained
	rec["uncertain"], rec["answers"], rec["calls"], rec["chunks"] = uncertain, answers, calls, len(recs)
}

// ---- scan ---------------------------------------------------------------------------------------

func scan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	root := fs.String("root", "", "the tree to walk")
	maxFiles := fs.Int("max-files", 400, "files to examine")
	outPath := fs.String("out", "", "summary JSON")
	recPath := fs.String("records", "", "optional: every record, one JSON per line")
	model := fs.String("model", "jev-1.13.0", "Jev model id")
	fs.Parse(args)
	t, err := cq.LoadTable()
	must(err)
	j := newJev(*model)
	cache := cq.MemCache{}
	disk := &cq.OSFS{Root: *root}
	run := func(pass string) (*cq.Scan, []cq.Record, int, time.Duration) {
		s, err := cq.NewScan(disk, "validate", "src", "", nil, nil, false, *maxFiles, 131072)
		must(err)
		var all []cq.Record
		steps, unavailable := 0, 0
		t0 := time.Now()
		for s.Status == "running" || s.Status == "judge_unavailable" {
			if s.Status == "judge_unavailable" {
				if unavailable++; unavailable == 3 {
					fmt.Fprintf(os.Stderr, "%s: stopped after three failed steps: %.200s\n", pass, s.LastError)
					break
				}
				time.Sleep(5 * time.Second)
			} else {
				unavailable = 0
			}
			recs, err := s.Step(t, disk, j, cache, 6, 400)
			must(err)
			all = append(all, recs...)
			steps++
		}
		return s, all, steps, time.Since(t0)
	}
	callsBefore := j.calls.Load()
	s1, recs, steps, d1 := run("first pass")
	firstCalls, firstTokens := j.calls.Load()-callsBefore, j.tokens.Load()
	s2, _, steps2, d2 := run("second pass")
	secondCalls := j.calls.Load() - callsBefore - firstCalls
	if *recPath != "" {
		f, err := os.Create(*recPath)
		must(err)
		for _, r := range recs {
			b, _ := json.Marshal(r)
			f.Write(append(b, '\n'))
		}
		f.Close()
	}
	summary := cq.Summary(recs, 15)
	windows := 0
	for _, r := range recs {
		if r.Chunk > 0 {
			windows++
		}
	}
	out := map[string]any{
		"root": *root, "catalogue": cq.CatalogueDigest(), "model": *model,
		"first_pass": map[string]any{"status": s1.Status, "steps_as_invokes": steps, "files_seen": s1.FilesSeen, "files_judged": s1.FilesJudged,
			"chunks": s1.Chunks, "extra_chunks_from_long_files": windows, "calls": firstCalls, "input_tokens": firstTokens,
			"seconds": d1.Seconds(), "excluded": s1.Excluded, "languages": s1.LangCounts, "listings": disk.Listings, "refused": s1.Refused},
		"second_pass_unchanged_tree": map[string]any{"status": s2.Status, "steps_as_invokes": steps2, "calls": secondCalls,
			"cache_hits": s2.CacheHits, "seconds": d2.Seconds()},
		"retries": j.retries.Load(), "summary": summary,
	}
	b, _ := json.MarshalIndent(out, "", " ")
	must(os.WriteFile(*outPath, b, 0o644))
	fmt.Fprintf(os.Stderr, "scan %s: %d files judged in %d steps, %d calls; re-scan %d calls\n", *root, s1.FilesJudged, steps, firstCalls, secondCalls)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "cqvalidate:", err)
		os.Exit(1)
	}
}
