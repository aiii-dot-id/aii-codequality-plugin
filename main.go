// id.aiii.codequality — measures source code quality with Jev as the judge.
//
// judge  asks the catalogue about texts the caller passes in.
// scan   walks a folder the operator granted, judging a few files per invoke; its place in the
//
//	walk and its results live in the plugin's private directory.
//
// report pages a scan's findings or summarises it.
//
// What the host requires of an install: a root grant (plugins.grants.<id>.roots), the host
// grant api.typesafe.ai:443, and an auth profile for Jev's key named in the api_key setting.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const (
	jevHost     = "net.outbound:api.typesafe.ai:443"
	jevURL      = "https://api.typesafe.ai/v1/systemone"
	jevModels   = "https://api.typesafe.ai/v1/models"
	jevDefault  = "jev-latest"
	callTimeout = 4000 // ms per Jev call; six calls fit the 30 s invoke wall
	maxCalls    = 6
	readPage    = 512 << 10 // below the frame budget once base64-encoded
	keepScans   = 8
)

var caps = []string{jevHost, "fs.roots", "fs.private"}

func init() {
	p := sdk.New("id.aiii.codequality")

	p.Describe("judge", sdk.Descriptor{
		Summary:        "Judge the quality of source code passed in, with Jev: findings, dimension scores, an index and a band",
		Input:          "schemas/judge_in.json",
		Output:         "schemas/judge_out.json",
		Effects:        sdk.EffectsWriteExternal,
		Capabilities:   caps,
		MaxResultBytes: 65536,
		Family:         "code quality",
		Keywords:       []string{"code quality", "review", "judge", "maintainability", "findings"},
		Examples:       []string{`{"items":[{"ref":"store.go","text":"package store\n..."}]}`},
	})
	p.Handle("judge", judge)

	p.Describe("scan", sdk.Descriptor{
		Summary:        "Scan a granted folder's source files with Jev, a few per call: start, then step until done",
		Input:          "schemas/scan_in.json",
		Output:         "schemas/scan_out.json",
		Effects:        sdk.EffectsWriteExternal,
		Capabilities:   caps,
		MaxResultBytes: 65536,
		Family:         "code quality",
		Keywords:       []string{"code quality", "scan", "repository", "folder", "quality report"},
		Examples:       []string{`{"action":"start","root":"src","path":"internal"}`, `{"action":"step","scan_id":"cq_1758500000000"}`},
	})
	p.Handle("scan", scan)

	p.Describe("models", sdk.Descriptor{
		Summary:        "List the Jev models the configured key can use — the choices for the model setting",
		Input:          "schemas/models_in.json",
		Output:         "schemas/models_out.json",
		Effects:        sdk.EffectsReadExternal,
		Capabilities:   []string{jevHost},
		MaxResultBytes: 65536,
		Family:         "code quality",
		Keywords:       []string{"jev", "models"},
	})
	p.Handle("models", models)

	p.Describe("report", sdk.Descriptor{
		Summary:        "Report a scan: its summary with the worst files, or its findings page by page; no scan_id lists the scans",
		Input:          "schemas/report_in.json",
		Output:         "schemas/report_out.json",
		Effects:        sdk.EffectsReadInternal,
		Capabilities:   []string{"fs.private"},
		MaxResultBytes: 262144,
		Family:         "code quality",
		Keywords:       []string{"code quality", "report", "findings", "summary"},
	})
	p.Handle("report", report)

	p.Run()
}

// main never runs in the guest; on a host it prints the descriptors for 'aiisdk package'.
func main() { sdk.MainDescribe() }

var tbl *cq.Table

func table() (*cq.Table, error) {
	if tbl == nil {
		t, err := cq.LoadTable()
		if err != nil {
			return nil, err
		}
		tbl = t
	}
	return tbl, nil
}

// ---- the host bindings ------------------------------------------------------------------------

type hostFS struct{ root string }

func (f hostFS) List(dir string) ([]cq.Entry, bool, error) {
	es, truncated, err := sdk.Files.List(f.root, dir)
	if err != nil {
		return nil, false, err
	}
	out := make([]cq.Entry, 0, len(es))
	for _, e := range es {
		out = append(out, cq.Entry{Name: e.Name, Dir: e.Dir, Size: e.Size, Symlink: e.Symlink})
	}
	return out, truncated, nil
}

func (f hostFS) Read(file string, max int64) ([]byte, error) { return readAll(f.root, file, max) }

func readAll(root, file string, max int64) ([]byte, error) {
	var out []byte
	for {
		n := readPage
		if left := max - int64(len(out)); left < int64(n) {
			n = int(left)
		}
		if n <= 0 {
			return out, nil
		}
		b, eof, _, err := sdk.Files.Read(root, file, int64(len(out)), n)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		if eof || len(b) == 0 {
			return out, nil
		}
	}
}

type hostJudge struct{ handle, model string }

func (j hostJudge) Model() string { return j.model }

func (j hostJudge) Ask(state, questions map[string]any) (map[string]float64, error) {
	body, err := cq.RequestBody(j.model, state, questions)
	if err != nil {
		return nil, err
	}
	res, err := sdk.HTTP.Post(jevURL, &sdk.HTTPOptions{Body: string(body), ContentType: "application/json",
		AuthProfile: j.handle, TimeoutMS: callTimeout})
	if err != nil {
		if _, denied := sdk.AsDenied(err); denied {
			return nil, err // a grant or profile is missing: not something a retry fixes
		}
		return nil, fmt.Errorf("%w: %v", cq.ErrJudge, err)
	}
	raw := []byte(res.Body)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s)
	}
	switch {
	case res.Status == 200:
		return cq.ParseAnswers(raw)
	case res.Status == 429 || res.Status >= 500:
		return nil, fmt.Errorf("%w: Jev answered %d", cq.ErrJudge, res.Status)
	case res.Status == 403 && !json.Valid(raw):
		return nil, fmt.Errorf("%w: Jev's edge answered 403 with a page, not JSON", cq.ErrRefused)
	}
	return nil, sdk.Fail("JUDGE_REFUSED", fmt.Sprintf("Jev answered %d: %.300s", res.Status, raw))
}

// hostCache keeps answers in 16 append-only shard files of the private directory.
type hostCache struct {
	shards map[string]map[string]map[string]float64
}

func (c *hostCache) shard(key string) (string, map[string]map[string]float64) {
	name := "cache/" + key[:1] + ".jsonl"
	if c.shards == nil {
		c.shards = map[string]map[string]map[string]float64{}
	}
	if m, ok := c.shards[name]; ok {
		return name, m
	}
	m := map[string]map[string]float64{}
	if b, err := readAll(sdk.PrivateRoot, name, 16<<20); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			var e struct {
				K string             `json:"k"`
				A map[string]float64 `json:"a"`
			}
			if json.Unmarshal([]byte(ln), &e) == nil && e.K != "" {
				m[e.K] = e.A
			}
		}
	}
	c.shards[name] = m
	return name, m
}

func (c *hostCache) Get(key string) (map[string]float64, bool) {
	_, m := c.shard(key)
	a, ok := m[key]
	return a, ok
}

func (c *hostCache) Put(key string, a map[string]float64) error {
	name, m := c.shard(key)
	line, _ := json.Marshal(map[string]any{"k": key, "a": a})
	if err := sdk.Files.Append(sdk.PrivateRoot, name, append(line, '\n')); err != nil {
		return err
	}
	m[key] = a
	return nil
}

func judgeFromSettings() (hostJudge, error) {
	vals, err := sdk.Settings.Load()
	if err != nil {
		return hostJudge{}, err
	}
	handle, _ := vals.Handle("api_key")
	if handle == "" {
		return hostJudge{}, sdk.Fail("JUDGE_UNCONFIGURED", "the api_key setting names no credential — choose the auth profile that holds Jev's key in the plugins view")
	}
	model, _ := vals.String("model")
	if model == "" {
		model = jevDefault
	}
	return hostJudge{handle: handle, model: model}, nil
}

func failure(err error) (any, error) {
	if d, ok := sdk.AsDenied(err); ok {
		return nil, sdk.Deny(d.ReasonCode, "the host denied "+d.Message+" — grant the folder (plugins.grants.<id>.roots), the host api.typesafe.ai:443 and the credential handle")
	}
	return nil, err
}

// ---- models -----------------------------------------------------------------------------------

// models answers the host's question for the model setting's choices:
// Jev's own list, read with the operator's key.
func models(c sdk.Call) (any, error) {
	j, err := judgeFromSettings()
	if err != nil {
		return nil, err
	}
	res, err := sdk.HTTP.Get(jevModels, &sdk.HTTPOptions{AuthProfile: j.handle, TimeoutMS: 10000})
	if err != nil {
		return failure(err)
	}
	raw := []byte(res.Body)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s)
	}
	if res.Status != 200 {
		return nil, sdk.Fail("JUDGE_REFUSED", fmt.Sprintf("Jev answered %d to the model list", res.Status))
	}
	choices, err := cq.ModelChoices(raw)
	if err != nil {
		return nil, sdk.Fail("JUDGE_REFUSED", err.Error())
	}
	return map[string]any{"choices": choices}, nil
}

// ---- judge ------------------------------------------------------------------------------------

func judge(c sdk.Call) (any, error) {
	t, err := table()
	if err != nil {
		return nil, err
	}
	items, ok := sdk.ObjectArray(c.Args().Raw("items"))
	if !ok || len(items) == 0 {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "judge requires arguments.items: [{ref, text, language?}]")
	}
	budget := 0
	for _, it := range items {
		text, _ := it.String("text")
		budget += cq.EstimatedCalls(int64(len(text)))
	}
	if budget > maxCalls {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", fmt.Sprintf("these texts need up to %d Jev calls; one invoke makes at most %d — pass fewer or shorter texts", budget, maxCalls))
	}
	j, err := judgeFromSettings()
	if err != nil {
		return nil, err
	}
	cache := &hostCache{}
	var recs []cq.Record
	for _, it := range items {
		ref, _ := it.String("ref")
		text, _ := it.String("text")
		lang, _ := it.String("language")
		if lang == "" {
			lang = t.Detect(ref, []byte(text))
		}
		if lang == "" || t.Langs[lang] == nil {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", fmt.Sprintf("item %q: name its language (the ref's extension did not say)", ref))
		}
		rs, err := cq.JudgeText(t, j, cache, ref, lang, text)
		if errors.Is(err, cq.ErrRefused) {
			return nil, sdk.Fail("JUDGE_REFUSED_TEXT", fmt.Sprintf("item %q: Jev's service refused this text (its firewall answered 403); the rest of the request was not judged", ref))
		}
		if err != nil {
			return failure(err)
		}
		recs = append(recs, rs...)
	}
	return map[string]any{"records": recs}, nil
}

// ---- scan -------------------------------------------------------------------------------------

func statePath(id string) string   { return "scans/" + id + ".state.json" }
func resultsPath(id string) string { return "scans/" + id + ".jsonl" }

func validID(id string) bool {
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return id != "" && len(id) <= 64
}

func loadScan(id string) (*cq.Scan, error) {
	if !validID(id) {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan_id is required (scan start returns it)")
	}
	b, err := readAll(sdk.PrivateRoot, statePath(id), 8<<20)
	if err != nil {
		return nil, sdk.Fail("SCAN_UNKNOWN", "no scan "+id+" — report with no scan_id lists the scans")
	}
	var s cq.Scan
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveScan(s *cq.Scan) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(b) > 250<<10 {
		return sdk.Fail("SCAN_TOO_WIDE", "the walk's pending directories no longer fit one write — scan a narrower path")
	}
	return sdk.Files.Write(sdk.PrivateRoot, statePath(s.ID), b)
}

func scanView(s *cq.Scan) map[string]any {
	return map[string]any{"scan_id": s.ID, "status": s.Status, "files_seen": s.FilesSeen, "files_judged": s.FilesJudged,
		"chunks": s.Chunks, "calls": s.Calls, "cache_hits": s.CacheHits, "excluded": s.Excluded,
		"languages": s.LangCounts, "last_error": s.LastError, "refused": s.Refused}
}

func scan(c sdk.Call) (any, error) {
	args := c.Args()
	action, _ := args.String("action")
	switch action {
	case "start":
		return scanStart(c)
	case "step":
		id, _ := args.String("scan_id")
		s, err := loadScan(id)
		if err != nil {
			return nil, err
		}
		t, err := table()
		if err != nil {
			return nil, err
		}
		j, err := judgeFromSettings()
		if err != nil {
			return nil, err
		}
		calls := maxCalls
		if n, ok := args.Int("max_calls"); ok && n >= 2 && n < int64(calls) {
			calls = int(n)
		}
		recs, err := s.Step(t, hostFS{s.Root}, j, &hostCache{}, calls, 400)
		if len(recs) > 0 {
			var buf []byte
			for _, r := range recs {
				line, _ := json.Marshal(r)
				buf = append(append(buf, line...), '\n')
			}
			if werr := sdk.Files.Append(sdk.PrivateRoot, resultsPath(s.ID), buf); werr != nil {
				return failure(werr)
			}
		}
		if serr := saveScan(s); serr != nil {
			return nil, serr
		}
		if err != nil {
			return failure(err)
		}
		out := scanView(s)
		judged := make([]any, 0, len(recs))
		for _, r := range recs {
			judged = append(judged, map[string]any{"ref": r.Ref, "chunk": r.Chunk, "index": r.Index, "band": r.Band, "findings": len(r.Findings)})
		}
		out["judged"] = judged
		return out, nil
	}
	return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan requires action: start | step")
}

func scanStart(c sdk.Call) (any, error) {
	args := c.Args()
	root, _ := args.String("root")
	if root == "" {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan start requires root (a granted folder's name)")
	}
	base, _ := args.String("path")
	exclude, _ := args.StringArray("exclude")
	languages, _ := args.StringArray("languages")
	tests, _ := args.Bool("include_tests")
	maxFiles := 5000
	if n, ok := args.Int("max_files"); ok && n > 0 && n < 100000 {
		maxFiles = int(n)
	}
	id, named := args.String("scan_id")
	if named && id != "" {
		if !validID(id) {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "scan_id is letters, digits, '_' and '-' only")
		}
		if _, _, exists, _ := sdk.Files.Digest(sdk.PrivateRoot, statePath(id)); exists {
			return nil, sdk.Fail("SCAN_EXISTS", "a scan named "+id+" exists — step it, or choose another name")
		}
	} else {
		now, _ := c.HostNowMillis()
		id = "cq_" + strconv.FormatInt(now, 10)
		if _, _, exists, _ := sdk.Files.Digest(sdk.PrivateRoot, statePath(id)); exists {
			id += "_" + strconv.FormatInt(now%997, 10)
		}
	}
	s, err := cq.NewScan(hostFS{root}, id, root, base, exclude, languages, tests, maxFiles, 131072)
	if err != nil {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", err.Error())
	}
	if _, _, err := sdk.Files.List(root, s.Base); err != nil {
		return failure(err)
	}
	if err := pruneScans(); err != nil {
		return nil, err
	}
	if err := saveScan(s); err != nil {
		return nil, err
	}
	return scanView(s), nil
}

// pruneScans keeps the newest keepScans-1 scans, so the one being started makes keepScans.
func pruneScans() error {
	ids, err := scanIDs()
	if err != nil {
		return err
	}
	for len(ids) >= keepScans {
		for _, p := range []string{statePath(ids[0]), resultsPath(ids[0])} {
			if _, err := sdk.Files.Delete(sdk.PrivateRoot, p); err != nil {
				return err
			}
		}
		ids = ids[1:]
	}
	return nil
}

func scanIDs() ([]string, error) {
	es, _, err := sdk.Files.List(sdk.PrivateRoot, "scans")
	if err != nil {
		return nil, nil // no scans yet
	}
	var ids []string
	for _, e := range es {
		if strings.HasSuffix(e.Name, ".state.json") {
			ids = append(ids, strings.TrimSuffix(e.Name, ".state.json"))
		}
	}
	sort.Strings(ids) // cq_<ms>: oldest first
	return ids, nil
}

// ---- report -----------------------------------------------------------------------------------

func report(c sdk.Call) (any, error) {
	args := c.Args()
	id, _ := args.String("scan_id")
	if id == "" {
		ids, err := scanIDs()
		if err != nil {
			return nil, err
		}
		scans := make([]any, 0, len(ids))
		for _, id := range ids {
			if s, err := loadScan(id); err == nil {
				scans = append(scans, scanView(s))
			}
		}
		return map[string]any{"scans": scans}, nil
	}
	s, err := loadScan(id)
	if err != nil {
		return nil, err
	}
	b, err := readAll(sdk.PrivateRoot, resultsPath(id), 48<<20)
	if err != nil {
		b = nil // nothing judged yet
	}
	recs, err := cq.ParseRecords(b)
	if err != nil {
		return nil, err
	}
	shape, _ := args.String("shape")
	if shape == "" || shape == "summary" {
		worst := 10
		if n, ok := args.Int("worst"); ok && n > 0 && n <= 100 {
			worst = int(n)
		}
		out := cq.Summary(recs, worst)
		out["scan"] = scanView(s)
		out["catalogue"] = cq.CatalogueDigest()
		return out, nil
	}
	cursor, _ := args.Int("cursor")
	pageBytes := 131072
	if n, ok := args.Int("page_bytes"); ok && n >= 4096 && n <= 200000 {
		pageBytes = int(n)
	}
	minSev, _ := args.String("min_severity")
	if minSev == "" {
		minSev = "low"
	}
	lang, _ := args.String("language")
	var filtered []cq.Record
	if glob, _ := args.String("path"); glob != "" {
		for _, r := range recs {
			if ok, _ := path.Match(glob, r.Ref); ok || strings.HasPrefix(r.Ref, strings.TrimSuffix(glob, "*")) {
				filtered = append(filtered, r)
			}
		}
	} else {
		filtered = recs
	}
	page, next := cq.Page(filtered, int(cursor), pageBytes, minSev, lang)
	return map[string]any{"records": page, "next_cursor": next, "total_records": len(filtered)}, nil
}
