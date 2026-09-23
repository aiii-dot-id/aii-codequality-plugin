package cq

import (
	"bytes"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Entry is one directory entry.
type Entry struct {
	Name    string `json:"n"`
	Dir     bool   `json:"d,omitempty"`
	Size    int64  `json:"s,omitempty"`
	Symlink bool   `json:"l,omitempty"`
}

// FS reads a tree: List one directory (truncated when it holds more entries than the reader
// returns), Read one file of at most max bytes.
type FS interface {
	List(dir string) (entries []Entry, truncated bool, err error)
	Read(file string, max int64) ([]byte, error)
}

// Scan is a resumable walk: everything a step needs to continue is in this value, which the
// plugin stores between invokes.
type Scan struct {
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
	Status       string         `json:"status"` // running | done | budget_exhausted | judge_unavailable
	FilesSeen    int            `json:"files_seen"`
	FilesJudged  int            `json:"files_judged"`
	Chunks       int            `json:"chunks"`
	Calls        int            `json:"calls"`
	CacheHits    int            `json:"cache_hits"`
	Excluded     map[string]int `json:"excluded"`
	LangCounts   map[string]int `json:"languages_judged"`
	LastError    string         `json:"last_error,omitempty"`
	Refused      []string       `json:"refused,omitempty"` // files the judge's service refused to take
}

// NewScan starts a walk of base inside a granted root. The root's .gitignore and .gitattributes
// (at base) are read once, here.
func NewScan(fs FS, id, root, base string, exclude, languages []string, includeTests bool, maxFiles int, maxFileBytes int64) (*Scan, error) {
	base = strings.Trim(path.Clean("/"+base), "/")
	s := &Scan{ID: id, Root: root, Base: base, Stack: []string{base}, Exclude: exclude, Languages: languages,
		IncludeTests: includeTests, MaxFiles: maxFiles, MaxFileBytes: maxFileBytes, Status: "running",
		Excluded: map[string]int{}, LangCounts: map[string]int{}}
	for _, g := range exclude {
		if _, err := globRegexp(g); err != nil {
			return nil, err
		}
	}
	for name, dst := range map[string]*[]string{".gitignore": &s.Ignore, ".gitattributes": &s.Attrs} {
		b, err := fs.Read(path.Join(base, name), 256<<10)
		if err != nil {
			continue // absent or unreadable: no rules
		}
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" || strings.HasPrefix(ln, "#") {
				continue
			}
			if name == ".gitattributes" {
				if p := vendoredAttr(ln); p != "" {
					*dst = append(*dst, p)
				}
				continue
			}
			*dst = append(*dst, ln)
		}
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

// Step advances the walk until it has used maxCalls judge calls, examined maxFiles files in this
// step, or finished. It returns the records of the files judged in this step.
func (s *Scan) Step(t *Table, fs FS, j Judge, c Cache, maxCalls, maxExamined int) ([]Record, error) {
	if s.Status != "running" && s.Status != "judge_unavailable" {
		return nil, nil
	}
	s.Status = "running"
	ign, err := compileRules(s.Ignore)
	if err != nil {
		return nil, err
	}
	attrs, err := compileRules(s.Attrs)
	if err != nil {
		return nil, err
	}
	excl, err := compileRules(s.Exclude)
	if err != nil {
		return nil, err
	}
	var out []Record
	calls, examined := 0, 0
	for {
		if s.FilesSeen >= s.MaxFiles {
			s.Status = "budget_exhausted"
			return out, nil
		}
		if len(s.Pending) == 0 {
			if len(s.Stack) == 0 {
				s.Status = "done"
				return out, nil
			}
			dir := s.Stack[len(s.Stack)-1]
			s.Stack = s.Stack[:len(s.Stack)-1]
			entries, truncated, err := fs.List(dir)
			if err != nil {
				s.Excluded["unlistable_dir"]++
				continue
			}
			if truncated {
				s.Excluded["dir_listing_truncated"]++
			}
			if dir != s.Base && hasGit(entries) {
				s.Excluded["nested_repository"]++ // another repository, as git itself treats it
				continue
			}
			sort.Slice(entries, func(a, b int) bool { return entries[a].Name < entries[b].Name })
			var subdirs []string
			s.Dir = dir
			s.Pending = s.Pending[:0]
			for _, e := range entries {
				rel := s.rel(path.Join(dir, e.Name))
				switch {
				case e.Symlink:
					s.Excluded["symlink"]++
				case e.Dir:
					if why := s.dirExcluded(t, e.Name, rel, ign, attrs, excl); why != "" {
						s.Excluded[why]++
					} else {
						subdirs = append(subdirs, path.Join(dir, e.Name))
					}
				default:
					s.Pending = append(s.Pending, e)
				}
			}
			for i := len(subdirs) - 1; i >= 0; i-- { // lexical depth-first
				s.Stack = append(s.Stack, subdirs[i])
			}
			continue
		}
		e := s.Pending[0]
		if examined >= maxExamined || calls > 0 && calls+EstimatedCalls(e.Size) > maxCalls {
			return out, nil
		}
		file := path.Join(s.Dir, e.Name)
		examined++
		recs, used, err := s.file(t, fs, j, c, e, file, ign, attrs, excl, maxCalls)
		calls += used
		if err != nil {
			if errors.Is(err, ErrJudge) {
				s.Status = "judge_unavailable"
				s.LastError = s.rel(file) + ": " + err.Error()
				return out, nil // the file stays pending: the next step retries it
			}
			return out, err
		}
		s.Pending = s.Pending[1:]
		out = append(out, recs...)
	}
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

func (s *Scan) dirExcluded(t *Table, name, rel string, ign, attrs, excl []rule) string {
	switch {
	case t.GlobalDirs != nil && t.GlobalDirs.MatchString(name):
		return "global_dir"
	case langDirExcluded(t, s.Languages, name):
		return "language_dir"
	case matchRules(ign, rel, true):
		return "gitignore"
	case matchRules(attrs, rel, true):
		return "gitattributes"
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

// file judges one file, or records why it was not judged. It returns the judge calls it used.
func (s *Scan) file(t *Table, fs FS, j Judge, c Cache, e Entry, file string, ign, attrs, excl []rule, maxCalls int) ([]Record, int, error) {
	s.FilesSeen++
	rel := s.rel(file)
	name := e.Name
	skip := func(why string) ([]Record, int, error) { s.Excluded[why]++; return nil, 0, nil }
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
	case e.Size > s.MaxFileBytes || EstimatedCalls(e.Size) > maxCalls:
		return skip("oversize")
	}
	raw, err := fs.Read(file, s.MaxFileBytes)
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
	recs, err := JudgeText(t, j, c, rel, lang, string(raw))
	used := 0
	for _, r := range recs {
		used += r.Calls
	}
	if errors.Is(err, ErrRefused) {
		if len(s.Refused) < 200 {
			s.Refused = append(s.Refused, rel)
		}
		s.Excluded["refused_by_judge"]++
		return nil, used, nil
	}
	if err != nil {
		s.FilesSeen-- // not seen until judged: the step that judges it counts it
		return nil, used, err
	}
	s.FilesJudged++
	s.LangCounts[lang]++
	s.Chunks += len(recs)
	for _, r := range recs {
		s.Calls += r.Calls
		s.CacheHits += r.Cached
	}
	return recs, used, nil
}

// EstimatedCalls bounds the judge calls a file of size bytes can need: one code call per chunk
// plus the comment call.
func EstimatedCalls(size int64) int { return int(size/ChunkBytes) + 2 }

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

// rule is one gitignore-syntax pattern.
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

// matchRules applies gitignore semantics: the last matching rule wins; a directory-only rule
// matches a file only through one of its parent directories.
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

// globRegexp converts one gitignore pattern to a regular expression over a slash path.
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
