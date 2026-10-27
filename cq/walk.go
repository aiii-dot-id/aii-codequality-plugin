package cq

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
)

type Entry struct {
	Name    string `json:"n"`
	Dir     bool   `json:"d,omitempty"`
	Size    int64  `json:"s,omitempty"`
	Symlink bool   `json:"l,omitempty"`
}

type FS interface {
	List(dir string) (entries []Entry, truncated bool, err error)
	Read(file string, max int64) ([]byte, error)
}

type Scan struct {
	Runtime        json.RawMessage `json:"runtime,omitempty"`
	Version        int             `json:"version,omitempty"`
	ProgressOffset int64           `json:"progress_offset,omitempty"`
	Progress       ScanProgress    `json:"-"`
	Observed       []Coverage      `json:"-"`

	ID           string         `json:"id"`
	Root         string         `json:"root"`
	Base         string         `json:"base"`
	Stack        []string       `json:"stack"`
	Dir          string         `json:"dir"`
	Pending      []Entry        `json:"pending"`
	Ignore       []string       `json:"ignore,omitempty"`
	Attrs        []string       `json:"attrs,omitempty"`
	Exclude      []string       `json:"exclude,omitempty"`
	Languages    []string       `json:"languages,omitempty"`
	IncludeTests bool           `json:"include_tests,omitempty"`
	MaxFiles     int            `json:"max_files"`
	MaxFileBytes int64          `json:"max_file_bytes"`
	ChunkBytes   int            `json:"chunk_bytes,omitempty"`
	DirsListed   int            `json:"dirs_listed,omitempty"`
	FilesListed  int            `json:"files_listed,omitempty"`
	Status       string         `json:"status"`
	FilesSeen    int            `json:"files_seen"`
	FilesJudged  int            `json:"files_judged"`
	Chunks       int            `json:"chunks"`
	Calls        int            `json:"calls"`
	CacheHits    int            `json:"cache_hits"`
	Excluded     map[string]int `json:"excluded"`
	LangCounts   map[string]int `json:"languages_judged"`
	LastError    string         `json:"last_error,omitempty"`
	Refused      []string       `json:"refused,omitempty"`
}

const maxRulesBytes = 256 << 10

const RulesTooLarge = "%s at the folder scanned is larger than %d KiB; its rules were not read, so the scan was not started"

func NewScan(fs FS, id, root, base string, exclude, languages []string, includeTests bool, maxFiles int, maxFileBytes int64) (*Scan, error) {
	base = strings.Trim(path.Clean("/"+base), "/")
	s := &Scan{Version: RecordVersion, ID: id, Root: root, Base: base, Stack: []string{base}, Exclude: exclude, Languages: languages,
		IncludeTests: includeTests, MaxFiles: maxFiles, MaxFileBytes: maxFileBytes, Status: "running",
		Excluded: map[string]int{}, LangCounts: map[string]int{}}
	for _, g := range exclude {
		if _, err := globRegexp(g); err != nil {
			return nil, err
		}
	}
	if _, _, err := scopedRules(fs, base, base); err != nil {
		return nil, fmt.Errorf("scan root rules: %w", err)
	}
	return s, nil
}

func vendoredAttr(line string) string {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return ""
	}
	marked := false
	for _, a := range parts[1:] {
		switch a {
		case "linguist-vendored", "linguist-generated", "linguist-documentation",
			"linguist-vendored=true", "linguist-generated=true", "linguist-documentation=true":
			marked = true
		}
	}
	if !marked {
		return ""
	}
	return parts[0]
}

func (s *Scan) Step(t *Table, fs FS, j Judge, c Cache, maxCalls, maxExamined int) ([]Record, error) {
	if s.Status != "running" && s.Status != "judge_unavailable" {
		return nil, nil
	}
	s.Status, s.LastError = "running", ""
	excl, err := compileRules(s.Exclude)
	if err != nil {
		return nil, err
	}
	var out []Record
	calls, examined := 0, 0
	var scope walkScope
	loaded := false
	for {
		if len(s.Pending) == 0 && len(s.Stack) == 0 {
			s.Status = "done"
			return out, nil
		}
		if s.FilesSeen >= s.MaxFiles {
			if err := s.budgetCoverage("file_budget"); err != nil {
				return out, err
			}
			s.Status = "budget_exhausted"
			return out, nil
		}
		if len(s.Pending) == 0 {
			dir := s.Stack[len(s.Stack)-1]
			var err error
			scope, err = s.walkRules(t, fs, dir, excl)
			if scope.reason != "" {
				if e := s.scopeBoundary(scope, err); e != nil {
					return out, e
				}
				s.Stack = s.Stack[:len(s.Stack)-1]
				continue
			}
			entries := append([]Entry(nil), scope.entries...)
			sort.Slice(entries, func(a, b int) bool { return entries[a].Name < entries[b].Name })
			var subdirs []string
			var pending []Entry
			for _, e := range entries {
				rel := s.rel(path.Join(dir, e.Name))
				switch {
				case e.Symlink:
					if err := s.boundary(rel, "symlink"); err != nil {
						return out, err
					}
					s.Excluded["symlink"]++
				case e.Dir:
					if why := s.dirExcluded(t, e.Name, rel, scope.ign, excl); why != "" {
						if err := s.boundary(rel, why); err != nil {
							return out, err
						}
						s.Excluded[why]++
					} else {
						subdirs = append(subdirs, path.Join(dir, e.Name))
					}
				default:
					pending = append(pending, e)
				}
			}
			s.DirsListed++
			s.FilesListed += len(pending)
			s.Stack = s.Stack[:len(s.Stack)-1]
			s.Dir, s.Pending, loaded = dir, pending, true
			s.commitProgress()
			for i := len(subdirs) - 1; i >= 0; i-- {
				s.Stack = append(s.Stack, subdirs[i])
			}
			continue
		}
		if !loaded {
			var err error
			scope, err = s.walkRules(t, fs, s.Dir, excl)
			if scope.reason != "" {
				if e := s.scopeBoundary(scope, err); e != nil {
					return out, e
				}
				s.Pending = nil
				continue
			}
			loaded = true
		}
		e := s.Pending[0]
		if examined >= maxExamined || maxCalls <= 0 || calls > 0 && calls+EstimatedCallsAt(e.Size, s.chunk()) > maxCalls {
			return out, s.budgetCoverage("step_budget")
		}
		file := path.Join(s.Dir, e.Name)
		examined++
		recs, used, err := s.file(t, fs, j, c, e, file, scope.ign, scope.attrs, excl)
		calls += used
		out = append(out, recs...)
		if err != nil {
			if errors.Is(err, ErrJudge) {
				s.Status = "judge_unavailable"
				s.LastError = s.rel(file) + ": " + err.Error() + " — the next step retries this file"
				return out, nil
			}
			return out, err
		}
		s.Pending = s.Pending[1:]
		s.commitProgress()
	}
}

type walkScope struct {
	ign, attrs  []rule
	entries     []Entry
	ref, reason string
}

type ruleListings struct {
	FS
	dirs map[string][]Entry
}

func (f ruleListings) List(dir string) ([]Entry, bool, error) {
	entries, ok := f.dirs[dir]
	if !ok {
		return nil, false, fmt.Errorf("unvalidated rule directory %q", dir)
	}
	return entries, false, nil
}

func (s *Scan) walkRules(t *Table, fs FS, dir string, excl []rule) (scope walkScope, err error) {
	chain := []string{s.Base}
	if dir != s.Base {
		p := s.Base
		for _, part := range strings.Split(s.rel(dir), "/") {
			p = path.Join(p, part)
			chain = append(chain, p)
		}
	}
	view := ruleListings{FS: fs, dirs: map[string][]Entry{}}
	for _, d := range chain {
		scope.ref = path.Clean(s.rel(d))
		if d != s.Base {
			if why := s.dirExcluded(t, path.Base(d), scope.ref, scope.ign, excl); why != "" {
				scope.reason = why
				return scope, nil
			}
			found := false
			for _, entry := range scope.entries {
				if entry.Name != path.Base(d) {
					continue
				}
				if entry.Symlink {
					scope.reason = "symlink"
					return scope, nil
				}
				found = entry.Dir
				break
			}
			if !found {
				scope.reason = "rules_error"
				return scope, fmt.Errorf("directory %q is absent or no longer a directory", d)
			}
		}
		entries, truncated, e := fs.List(d)
		if e != nil {
			scope.reason = "unlistable_dir"
			return scope, fmt.Errorf("list %q before reading rules: %w", d, e)
		}
		if truncated {
			scope.reason = "dir_listing_truncated"
			return scope, fmt.Errorf("listing %q truncated; rules cannot be established", d)
		}
		if d != s.Base && hasGit(entries) {
			scope.reason = "nested_repository"
			return scope, nil
		}
		view.dirs[d] = entries
		scope.ign, scope.attrs, e = scopedRules(view, s.Base, d)
		if e != nil {
			scope.reason = "rules_error"
			return scope, e
		}
		scope.entries = entries
	}
	return scope, nil
}

func (s *Scan) commitProgress() {
	if s.Progress != nil {
		s.ProgressOffset = s.Progress.End()
	}
}

func (s *Scan) scopeBoundary(scope walkScope, cause error) error {
	if err := s.boundary(scope.ref, scope.reason); err != nil {
		return errors.Join(cause, err)
	}
	if cause != nil {
		s.LastError = scope.ref + ": " + cause.Error() + " — rules must validate before retry"
		return cause
	}
	s.Excluded[scope.reason]++
	s.commitProgress()
	return nil
}

func (s *Scan) budgetCoverage(reason string) error {
	for _, e := range s.Pending {
		if err := s.cover(Coverage{Ref: s.rel(path.Join(s.Dir, e.Name)), Kind: "file", Disposition: "pending", Reasons: []string{reason}, Size: e.Size}); err != nil {
			return err
		}
	}
	for _, dir := range s.Stack {
		if err := s.boundary(path.Clean(s.rel(dir)), reason); err != nil {
			return err
		}
	}

	return nil
}

func (s *Scan) chunk() int {
	if s.ChunkBytes > 0 {
		return s.ChunkBytes
	}
	return ChunkBytes
}

func (s *Scan) StepsLeft(callsPerStep, filesPerStep int) int {
	if s.Status != "running" && s.Status != "judge_unavailable" {
		return 0
	}
	perDir := 1.0
	if s.DirsListed > 0 {
		perDir = float64(s.FilesListed) / float64(s.DirsListed)
	}
	files := float64(len(s.Pending)) + float64(len(s.Stack))*perDir
	if left := float64(s.MaxFiles - s.FilesSeen); files > left {
		files = max(left, 0)
	}
	judged, calls := 0.5, 2.0
	if s.FilesSeen > 0 {
		judged = float64(s.FilesJudged) / float64(s.FilesSeen)
	}
	if s.FilesJudged > 0 {
		calls = float64(s.Calls) / float64(s.FilesJudged)
	}
	byCalls := files * judged * calls / float64(max(callsPerStep, 1))
	byFiles := files / float64(max(filesPerStep, 1))
	n := int(math.Ceil(max(byCalls, byFiles)))
	return max(n, 1)
}

func hasGit(entries []Entry) bool {
	for _, e := range entries {
		if e.Name == ".git" {
			return true
		}
	}
	return false
}

func (s *Scan) rel(p string) string {
	if s.Base == "" {
		return p
	}
	return strings.TrimPrefix(strings.TrimPrefix(p, s.Base), "/")
}

func (s *Scan) dirExcluded(t *Table, name, rel string, ign, excl []rule) string {

	switch {
	case t.GlobalDirs != nil && t.GlobalDirs.MatchString(name):
		return "global_dir"
	case langDirExcluded(t, s.Languages, name):
		return "language_dir"
	case matchRules(ign, rel, true):
		return "gitignore"
	case matchRules(excl, rel, true):
		return "caller_exclude"
	}
	return ""
}

func langDirExcluded(t *Table, only []string, name string) bool {
	keys := only
	if len(keys) == 0 {
		keys = t.order
	}
	for _, k := range keys {
		if L := t.Langs[k]; L != nil && L.ExcludeDirs != nil && L.ExcludeDirs.MatchString(name) {
			return true
		}
	}
	return false
}

func (s *Scan) file(t *Table, fs FS, j Judge, c Cache, e Entry, file string, ign, attrs, excl []rule) ([]Record, int, error) {
	s.FilesSeen++
	rel := s.rel(file)
	name := e.Name
	skip := func(why string) ([]Record, int, error) {
		disposition := "excluded"
		if why == "oversize" || why == "unreadable" || why == "no_language" {
			disposition = "unknown"
		}
		if err := s.cover(Coverage{Ref: rel, Kind: "file", Disposition: disposition, Reasons: []string{why}, Size: e.Size}); err != nil {
			s.FilesSeen--
			return nil, 0, err
		}
		s.Excluded[why]++
		return nil, 0, nil
	}
	switch {
	case SecretNamed.MatchString(name):
		return skip("secret_named")
	case t.Binary != nil && t.Binary.MatchString(name):
		return skip("binary_extension")
	case t.GlobalFiles != nil && t.GlobalFiles.MatchString(name):
		return skip("global_file")
	case matchRules(ign, rel, false):
		return skip("gitignore")
	case matchRules(attrs, rel, false):
		return skip("gitattributes")
	case matchRules(excl, rel, false):
		return skip("caller_exclude")

	case e.Size > s.MaxFileBytes:
		return skip("oversize")
	}
	raw, err := fs.Read(file, s.MaxFileBytes)
	if errors.Is(err, ErrTooLarge) {
		return skip("oversize")
	}
	if err != nil {
		return skip("unreadable")
	}
	if bytes.IndexByte(raw[:min(len(raw), 8192)], 0) >= 0 {
		return skip("nul_bytes")
	}
	lang := t.Detect(rel, raw[:min(len(raw), 4096)])
	switch {
	case lang == "":
		return skip("no_language")
	case len(s.Languages) > 0 && !contains(s.Languages, lang):
		return skip("language_not_selected")
	}
	L := t.Langs[lang]
	switch {
	case L.ExcludeFiles != nil && L.ExcludeFiles.MatchString(name):
		return skip("language_file")
	case t.IsGenerated(lang, raw):
		return skip("generated")
	case minified(lang, raw):
		return skip("minified")
	case !s.IncludeTests && t.IsTest(lang, rel):
		return skip("test")
	}

	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var prior []Record
	var save func(Record) error
	if s.Progress != nil {
		prior, err = s.Progress.Load(rel)
		if err != nil {
			s.FilesSeen--
			return nil, 0, err
		}
		save = s.Progress.Save
		for _, r := range prior {
			if r.SourceDigest != digest {
				err = s.cover(Coverage{Ref: rel, SourceDigest: r.SourceDigest, Kind: "file", Disposition: "stale", Reasons: []string{"source_changed", "fresh_assessment_required"}, Size: e.Size})
				if err != nil {
					s.FilesSeen--
					return nil, 0, err
				}
				s.Excluded["source_changed"]++
				return nil, 0, nil
			}
		}
	}
	recs, err := JudgeTextProgress(t, j, c, rel, lang, string(raw), s.chunk(), prior, save)
	if errors.Is(err, ErrInput) {
		err = s.cover(Coverage{Ref: rel, SourceDigest: digest, Kind: "file", Disposition: "unknown", Reasons: []string{"input_unassessable"}, Size: e.Size})
		if err != nil {
			s.FilesSeen--
			return nil, 0, err
		}
		s.Excluded["input_unassessable"]++
		return nil, 0, nil
	}
	expected := 0
	if len(recs) > 0 {
		expected = recs[0].WindowCount
	}
	used := 0
	for _, r := range recs {
		used += r.Calls
		s.Calls += r.Calls
		s.CacheHits += r.Cached
	}
	completed := map[int]bool{}
	incomplete := map[int]bool{}
	for _, r := range recs {
		if batteryAnswered(r) {
			completed[r.Chunk] = true
		} else {
			incomplete[r.Chunk] = true
		}
	}
	for n := range incomplete {
		delete(completed, n)
	}
	cov := Coverage{Ref: rel, SourceDigest: digest, Kind: "file", Disposition: "complete", Reasons: []string{}, Size: e.Size, ExpectedChunks: expected, CompletedChunks: len(completed)}
	if err != nil {
		cov.Disposition = "pending"
		cov.Reasons = append(cov.Reasons, "assessment_interrupted")
	}
	if errors.Is(err, ErrRefused) {
		cov.Disposition = "unknown"
		cov.Reasons = []string{"refused_by_judge"}
	}
	if errors.Is(err, ErrProgress) {
		s.FilesSeen--
		return recs, used, err
	}
	if cerr := s.cover(cov); cerr != nil {
		s.FilesSeen--
		return recs, used, cerr
	}
	if errors.Is(err, ErrRefused) {
		if len(s.Refused) < 200 {
			s.Refused = append(s.Refused, rel)
		}
		s.Excluded["refused_by_judge"]++
		return recs, used, nil
	}
	if err != nil {
		s.FilesSeen--
		return recs, used, err
	}
	s.FilesJudged++
	s.LangCounts[lang]++
	s.Chunks += recordWindows(recs)
	return recs, used, nil
}

func EstimatedCalls(size int64) int { return EstimatedCallsAt(size, ChunkBytes) }

func EstimatedCallsAt(size int64, chunk int) int {
	if chunk <= 0 {
		chunk = ChunkBytes
	}
	return 3 * (int(size/int64(chunk)) + 1)
}

func minified(lang string, raw []byte) bool {
	switch lang {
	case "javascript", "typescript", "css", "scss", "json", "html":
	default:
		return false
	}
	lines := bytes.Count(raw, []byte{'\n'}) + 1
	return len(raw)/lines > 110
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

type rule struct {
	neg, dirOnly bool
	rx           *regexp.Regexp
}

func compileRules(lines []string) ([]rule, error) {
	out := make([]rule, 0, len(lines))
	for _, ln := range lines {
		neg := strings.HasPrefix(ln, "!")
		p := strings.TrimPrefix(ln, "!")
		rx, err := globRegexp(p)
		if err != nil {
			return nil, err
		}
		out = append(out, rule{neg: neg, dirOnly: strings.HasSuffix(p, "/"), rx: rx})
	}
	return out, nil
}

func matchRules(rules []rule, rel string, isDir bool) bool {
	hit := false
	for _, r := range rules {
		m := false
		if r.dirOnly && !isDir {
			for i := 0; i < len(rel); i++ {
				if rel[i] == '/' && r.rx.MatchString(rel[:i]) {
					m = true
					break
				}
			}
		} else {
			m = r.rx.MatchString(rel)
		}
		if m {
			hit = !r.neg
		}
	}
	return hit
}

func globRegexp(p string) (*regexp.Regexp, error) {
	p = strings.TrimSuffix(p, "/")
	anchored := strings.HasPrefix(p, "/") || strings.Contains(p, "/")
	p = strings.TrimPrefix(p, "/")
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("^(?:.*/)?")
	}
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		case p[i] == '[':
			if j := strings.IndexByte(p[i:], ']'); j > 0 {
				b.WriteString(p[i : i+j+1])
				i += j
			} else {
				b.WriteString(`\[`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	b.WriteString("(?:/.*)?$")
	return regexp.Compile(b.String())
}
