package cq

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
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

	RangeFS
}

type Scan struct {
	Runtime        json.RawMessage `json:"runtime,omitempty"`
	Version        int             `json:"version,omitempty"`
	ProgressOffset int64           `json:"progress_offset,omitempty"`
	IntentOffset   int64           `json:"intent_offset,omitempty"`
	Progress       ScanProgress    `json:"-"`
	AskIntent      bool            `json:"-"`
	Observed       []Coverage      `json:"-"`
	Attempts       AttemptStore    `json:"-"`

	Checkpoint func() error `json:"-"`
	unsaved    bool
	attempt    *attempts
	metadata   int

	ID           string   `json:"id"`
	Root         string   `json:"root"`
	Base         string   `json:"base"`
	Stack        []string `json:"stack"`
	Dir          string   `json:"dir"`
	Pending      []Entry  `json:"pending"`
	Ignore       []string `json:"ignore,omitempty"`
	Attrs        []string `json:"attrs,omitempty"`
	Exclude      []string `json:"exclude,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	IncludeTests bool     `json:"include_tests,omitempty"`

	IncludeGenerated bool           `json:"include_generated,omitempty"`
	MaxFiles         int            `json:"max_files"`
	MaxFileBytes     int64          `json:"max_file_bytes"`
	ChunkBytes       int            `json:"chunk_bytes,omitempty"`
	JournalCap       int64          `json:"journal_cap,omitempty"`
	DirsListed       int            `json:"dirs_listed,omitempty"`
	FilesListed      int            `json:"files_listed,omitempty"`
	Status           string         `json:"status"`
	FilesSeen        int            `json:"files_seen"`
	FilesJudged      int            `json:"files_judged"`
	Chunks           int            `json:"chunks"`
	Calls            int            `json:"calls"`
	SlopCalls        int            `json:"slop_calls,omitempty"`
	IntentCalls      int            `json:"intent_calls,omitempty"`
	CacheHits        int            `json:"cache_hits"`
	Excluded         map[string]int `json:"excluded"`
	LangCounts       map[string]int `json:"languages_judged"`
	LastError        string         `json:"last_error,omitempty"`
	File             *FileCursor    `json:"file_cursor,omitempty"`
	Refused          []string       `json:"refused,omitempty"`
	NotFinished      []string       `json:"not_finished,omitempty"`
	TooWide          []string       `json:"too_wide,omitempty"`
	Truncated        []string       `json:"listing_truncated,omitempty"`
	PlanTooLarge     []string       `json:"plan_too_large,omitempty"`
}

const (
	StatusFileCap     = "file_cap_reached"
	StatusExaminedCap = "examined_cap_reached"
	legacyFileCap     = "budget_exhausted"
)

const ExaminedPerFile = 20

func (s *Scan) MaxExamined() int { return ExaminedPerFile * s.MaxFiles }

func CurrentStatus(status string) string {
	if status == legacyFileCap {
		return StatusFileCap
	}
	return status
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

const StepFiles = 32

func (s *Scan) Step(t *Table, fs FS, j Judge, c Cache, maxCalls, maxExamined int) ([]Record, error) {
	if s.Status != "running" && s.Status != "judge_unavailable" {
		return nil, nil
	}
	a, err := loadAttempts(s.Attempts)
	if err != nil {
		return nil, err
	}
	s.attempt = a
	out, err := s.step(t, fs, j, c, maxCalls, maxExamined, a)

	if s.Checkpoint == nil {
		if cerr := s.Settle(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return out, err
}

func (s *Scan) Settle() error {
	a := s.attempt
	s.attempt = nil
	if a == nil {
		return nil
	}
	return a.settle()
}

func (s *Scan) step(t *Table, fs FS, j Judge, c Cache, maxCalls, maxExamined int, a *attempts) ([]Record, error) {
	s.Status, s.LastError = "running", ""
	s.unsaved = false
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
		if s.FilesJudged >= s.MaxFiles {
			if err := s.budgetCoverage("file_budget"); err != nil {
				return out, err
			}
			s.Status = StatusFileCap
			return out, nil
		}
		if s.FilesSeen >= s.MaxExamined() {
			if err := s.budgetCoverage("examined_budget"); err != nil {
				return out, err
			}
			s.Status = StatusExaminedCap
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
				s.dropUnder(scope.ref)
				s.unsaved = true
				continue
			}
			entries := append([]Entry(nil), scope.entries...)
			sort.Slice(entries, func(a, b int) bool { return entries[a].Name < entries[b].Name })

			type edge struct{ rel, why string }
			var edges []edge
			var subdirs []string
			var pending []Entry
			var linked []string
			unrecordable := 0
			for _, e := range entries {
				rel := s.rel(path.Join(dir, e.Name))
				switch {
				case strings.ContainsAny(e.Name, "\x00\r\n"):

					unrecordable++
				case e.Symlink:
					edges = append(edges, edge{rel, "symlink"})
					linked = append(linked, e.Name)
				case e.Dir:
					if why := s.dirExcluded(t, e.Name, rel, scope.ign, excl); why != "" {
						edges = append(edges, edge{rel, why})
					} else {
						subdirs = append(subdirs, path.Join(dir, e.Name))
					}
				default:
					pending = append(pending, e)
				}
			}
			if !s.frontierFits(dir, subdirs, pending) {
				if err := s.tooWide(dir); err != nil {
					return out, err
				}
				s.Stack = s.Stack[:len(s.Stack)-1]
				continue
			}
			if unrecordable > 0 {
				s.Excluded[UnrecordableName] += unrecordable
			}

			if err := s.markLeft(dir, linked, "symlink"); err != nil {
				return out, err
			}
			for _, b := range edges {
				if err := s.boundary(b.rel, b.why); err != nil {
					return out, err
				}
				s.Excluded[b.why]++
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

				if err == nil {

					if e := s.leavePackage(scope.reason); e != nil {
						return out, e
					}
					if e := s.leaveFile(scope.reason); e != nil {
						return out, e
					}
				} else if s.onFile() {

					s.LastError = scope.ref + ": " + err.Error() + rulesRetry
					return out, err
				}
				if e := s.scopeBoundary(scope, err); e != nil {
					return out, e
				}
				s.Pending = nil
				s.dropUnder(scope.ref)
				s.unsaved = true
				continue
			}
			loaded = true
		}
		e := s.Pending[0]
		if examined >= maxExamined || maxCalls <= 0 || calls > 0 && calls+s.estimatedCalls(e.Size) > maxCalls {
			return out, nil
		}
		file := path.Join(s.Dir, e.Name)
		examined++

		if stop, err := s.durable(); err != nil || stop {
			return out, err
		}

		giveUp, err := a.begin(s.rel(file))
		if err != nil {
			return out, err
		}
		if giveUp {
			if err := s.notFinished(e, s.rel(file)); err != nil {
				return out, err
			}
			s.Pending = s.Pending[1:]
			s.commitProgress()
			a.finished()

			if s.Checkpoint != nil {
				if _, err := s.durable(); err != nil {
					return out, err
				}
				if err := a.done(); err != nil {
					return out, err
				}
			}
			return out, nil
		}
		recs, used, err := s.file(t, fs, j, c, e, file, scope.ign, scope.attrs, excl)
		calls += used
		out = append(out, recs...)
		if err != nil {
			if errors.Is(err, ErrFileChanged) {

				if old := s.File; old != nil {
					cerr := s.cover(Coverage{Ref: old.Ref, SourceDigest: old.Digest, Kind: "file", Disposition: "stale", Reasons: []string{"source_changed", "fresh_assessment_required"}, Size: old.Size})
					if cerr != nil {
						return out, cerr
					}
				}
				s.File = nil
				s.LastError = s.rel(file) + ": " + fileChanged
				return out, nil
			}
			if errors.Is(err, ErrStepCalls) {

				s.LastError = s.rel(file) + ": " + continuesNextStep
				return out, nil
			}
			if errors.Is(err, ErrJudge) {
				s.Status = "judge_unavailable"
				s.LastError = s.rel(file) + ": " + err.Error() + " — the next step retries this file"
				return out, nil
			}
			return out, err
		}
		s.Pending = s.Pending[1:]
		s.commitProgress()
		a.finished()
		if s.Checkpoint != nil {
			stop, err := s.durable()
			if err != nil {
				return out, err
			}

			if err := a.done(); err != nil || stop {
				return out, err
			}
		}
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

			scope.reason = ListingTruncated
			return scope, nil
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

func (s *Scan) durable() (stop bool, err error) {
	if s.Checkpoint == nil || !s.unsaved {
		return false, nil
	}
	s.commitProgress()
	if err := s.Checkpoint(); err != nil {
		return false, err
	}
	s.unsaved = false
	return s.Status != "running", nil
}

func (s *Scan) commitProgress() {
	s.unsaved = true
	if s.Progress != nil {
		s.ProgressOffset = s.Progress.End()
		if p, ok := s.Progress.(interface{ IntentEnd() int64 }); ok {
			s.IntentOffset = p.IntentEnd()
		}
	}
}

func (s *Scan) scopeBoundary(scope walkScope, cause error) error {
	if err := s.boundary(scope.ref, scope.reason); err != nil {
		return errors.Join(cause, err)
	}
	if cause != nil {
		s.LastError = scope.ref + ": " + cause.Error() + rulesRetry
		return cause
	}
	if scope.reason == ListingTruncated {
		s.listingTruncated(scope.ref)
	} else {
		s.Excluded[scope.reason]++
	}
	s.commitProgress()
	return nil
}

func (s *Scan) dropUnder(ref string) {
	kept := s.Stack[:0]
	for _, d := range s.Stack {
		r := path.Clean(s.rel(d))
		if ref == "." || r == ref || strings.HasPrefix(r, ref+"/") {
			continue
		}
		kept = append(kept, d)
	}
	s.Stack = kept
}

func (s *Scan) leaveFile(reason string) error {
	old := s.File
	if old == nil {
		return nil
	}
	if !s.onFile() {

		s.File = nil
		return nil
	}
	disposition := "unknown"
	if chosenSkips[reason] {
		disposition = "excluded"
	}
	if err := s.cover(Coverage{Ref: old.Ref, SourceDigest: old.Digest, Kind: "file", Disposition: disposition, Reasons: []string{reason}, Size: old.Size, ExpectedChunks: old.Windows, CompletedChunks: min(old.Done, old.Windows)}); err != nil {
		return err
	}
	s.File = nil
	s.attempt.left(old.Ref)
	return nil
}

const PackageLeftListed = 8

func (s *Scan) leavePackage(reason string) error {
	names := make([]string, len(s.Pending))
	for i, e := range s.Pending {
		names[i] = e.Name
	}
	return s.markLeft(s.Dir, names, reason)
}

func (s *Scan) markLeft(dir string, names []string, reason string) error {
	var left []string
	for _, name := range names {
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			left = append(left, name)
		}
	}
	for i, name := range left {
		if i == PackageLeftListed {
			break
		}
		why := reason
		if i == PackageLeftListed-1 && len(left) > PackageLeftListed {
			why += fmt.Sprintf(", and %d more Go files of this folder not read", len(left)-PackageLeftListed)
		}
		if err := s.skippedGo(s.rel(path.Join(dir, name)), name, why); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scan) onFile() bool {
	return s.File != nil && len(s.Pending) > 0 && s.File.Ref == s.rel(path.Join(s.Dir, s.Pending[0].Name))
}

func (s *Scan) Frontier() []Coverage {
	var out []Coverage
	for _, e := range s.Pending {
		out = append(out, Coverage{Ref: s.rel(path.Join(s.Dir, e.Name)), Kind: "file", Disposition: "pending", Reasons: []string{"not_yet_assessed"}, Size: e.Size})
	}
	for _, dir := range s.Stack {
		out = append(out, Coverage{Ref: path.Clean(s.rel(dir)), Kind: "subtree", Disposition: "unknown", Reasons: []string{"not_yet_listed", "children_not_enumerated"}})
	}
	return out
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

const UnrecordableName = "name_not_recordable"

const IgnoredDir = "ignored_dir"

func underUnderscore(rel string) bool {
	dirs := strings.Split(rel, "/")
	for _, d := range dirs[:len(dirs)-1] {
		if strings.HasPrefix(d, "_") {
			return true
		}
	}
	return false
}

func (s *Scan) dirExcluded(t *Table, name, rel string, ign, excl []rule) string {

	switch {
	case t.GlobalDirs != nil && t.GlobalDirs.MatchString(name):
		return "global_dir"
	case name == "testdata" || strings.HasPrefix(name, "."):
		return IgnoredDir
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
		if why == "unreadable" || why == "input_unassessable" || why == PlanTooLarge {
			disposition = "unknown"
		}
		row := Coverage{Ref: rel, Kind: "file", Disposition: disposition, Reasons: []string{why}, Size: e.Size}

		left := s.File != nil && s.File.Ref == rel
		if left {
			row.SourceDigest, row.ExpectedChunks, row.CompletedChunks = s.File.Digest, s.File.Windows, min(s.File.Done, s.File.Windows)
		}
		if err := s.cover(row); err != nil {
			s.FilesSeen--
			return nil, 0, err
		}
		if err := s.skippedGo(rel, name, why); err != nil {
			s.FilesSeen--
			return nil, 0, err
		}
		if left {
			s.File = nil
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
	case matchAttrs(attrs, rel):
		return skip("gitattributes")
	case matchRules(excl, rel, false):
		return skip("caller_exclude")
	case e.Size > MaxSourceBytes:
		return skip(LargerThanSource)
	case strings.HasSuffix(name, ".go") && underUnderscore(rel):
		return skip(IgnoredDir)
	}

	if s.File != nil && s.File.Ref == rel && s.File.Lang != "" {
		return s.largeFile(t, fs, j, c, e, file, rel, s.File.Lang, s.File.Langs, skip)
	}

	large := e.Size > SegmentBytes || s.File != nil && s.File.Ref == rel
	var raw []byte
	var err error
	if large {
		raw, _, err = fs.ReadAt(file, 0, SegmentBytes)
	} else {
		raw, err = fs.Read(file, SegmentBytes)
		if errors.Is(err, ErrTooLarge) {
			large = true
			raw, _, err = fs.ReadAt(file, 0, SegmentBytes)
		}
	}
	if err != nil {
		return skip("unreadable")
	}

	if bytes.IndexByte(raw[:min(len(raw), 8192)], 0) >= 0 || !large && !utf8.Valid(raw) {
		return skip("not_text")
	}
	lang := t.Detect(rel, raw[:min(len(raw), 4096)])
	switch {
	case lang == "":
		return skip("language_not_recognised")
	case len(s.Languages) > 0 && !contains(s.Languages, lang):
		return skip("language_not_selected")
	}
	L := t.Langs[lang]
	switch {
	case L.ExcludeFiles != nil && L.ExcludeFiles.MatchString(name):
		return skip("language_file")
	case !s.IncludeGenerated && t.IsGenerated(lang, raw):
		return skip("generated")
	case !s.IncludeGenerated && minified(lang, raw):
		return skip("minified")
	case !s.IncludeTests && t.IsTest(lang, rel):
		return skip("test")
	}

	langs := t.FileLanguages(lang, raw)
	if large {
		return s.largeFile(t, fs, j, c, e, file, rel, lang, langs, skip)
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
	recs, err := JudgeTextProgress(t, j, c, rel, lang, string(raw), s.chunk(), prior, save, s.AskIntent, langs...)
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
		if r.Battery == IntentBattery {
			s.IntentCalls += r.Calls
		}
		if r.Battery == SlopBattery {
			s.SlopCalls += r.Calls
		}
	}
	completed, _ := windowsJudged(recs)
	if errors.Is(err, ErrKey) || errors.Is(err, ErrCache) {

		s.FilesSeen--
		return recs, used, err
	}
	cov := Coverage{Ref: rel, SourceDigest: digest, Kind: "file", Disposition: "complete", Reasons: []string{}, Size: e.Size, ExpectedChunks: expected, CompletedChunks: completed}
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
		s.Refused = keepName(s.Refused, rel, 200)
		s.Excluded["refused_by_judge"]++
		return recs, used, nil
	}
	if err != nil {
		s.FilesSeen--
		return recs, used, err
	}
	extra, err := s.recordShape(t, j, c, lang, rel, digest, raw, nil, prior)
	recs = append(recs, extra...)
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

const CallsPerChunk = 3

func EstimatedCallsAt(size int64, chunk int) int {
	if chunk <= 0 {
		chunk = ChunkBytes
	}
	return CallsPerChunk * (int(size/int64(chunk)) + 1)
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
	attr         string
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

func matchAttrs(attrs []rule, rel string) bool {
	set := map[string]bool{}
	for _, r := range attrs {
		if r.rx.MatchString(rel) {
			set[r.attr] = !r.neg
		}
	}
	for _, on := range set {
		if on {
			return true
		}
	}
	return false
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

				class := p[i+1 : i+j]
				if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
					class = "^/" + class[1:]
				}
				b.WriteString("[" + class + "]")
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

func (s *Scan) largeFile(t *Table, fs RangeFS, j Judge, c Cache, e Entry, file, rel, lang string, langs []string, skip func(string) ([]Record, int, error)) ([]Record, int, error) {
	if s.File != nil && s.File.Ref == rel {
		if err := s.File.unchanged(fs, file); err != nil {
			s.FilesSeen--
			return nil, 0, err
		}
	} else {
		cur, shape, err := PlanFile(t, fs, file, rel, lang, s.chunk(), t.IsTest(lang, rel))
		over := err == nil && !s.cursorFits(cur, lang, langs)
		runtime.GC()
		if errors.Is(err, ErrNotText) {
			return skip("not_text")
		}
		if errors.Is(err, ErrTooLarge) {
			return skip(LargerThanSource)
		}
		if errors.Is(err, ErrInput) {
			return skip("input_unassessable")
		}
		if err != nil {
			return skip("unreadable")
		}
		if over {

			recs, n, err := skip(PlanTooLarge)
			if err == nil {
				s.namePlanTooLarge(rel)
			}
			return recs, n, err
		}
		cur.Lang, cur.Langs = lang, langs
		s.File = cur
		if s.Progress != nil && s.AskIntent {
			if err := s.Progress.Save(ShapeRecord(rel, cur.Digest, *shape)); err != nil {
				s.FilesSeen--
				return nil, 0, err
			}
		}
	}
	cur := s.File
	var prior []Record
	var save func(Record) error
	if s.Progress != nil {
		var err error
		prior, err = s.Progress.Load(rel)
		if err != nil {
			s.FilesSeen--
			return nil, 0, err
		}
		save = s.Progress.Save
	}
	before := cur.Calls
	recs, err := JudgeFileProgress(t, j, c, fs, file, lang, cur, s.chunk(), prior, save, s.AskIntent, func() error { s.commitProgress(); return nil }, langs...)
	used := cur.Calls - before
	for _, r := range recs {
		s.Calls += r.Calls
		s.CacheHits += r.Cached
		if r.Battery == IntentBattery {
			s.IntentCalls += r.Calls
		}
		if r.Battery == SlopBattery {
			s.SlopCalls += r.Calls
		}
	}
	if err == nil {
		err = cur.unchanged(fs, file)
	}
	if err == nil {

		shape := FileShape{Outline: cur.Outline, Lines: cur.Lines, Test: t.IsTest(lang, rel)}
		_, err = s.recordShape(t, j, c, lang, rel, cur.Digest, nil, &shape, prior)
	}
	if err != nil {
		s.FilesSeen--
		return recs, used, err
	}
	cov := Coverage{Ref: rel, SourceDigest: cur.Digest, Kind: "file", Disposition: "complete", Reasons: []string{}, Size: cur.Size, ExpectedChunks: cur.Windows, CompletedChunks: cur.Done}
	if cerr := s.cover(cov); cerr != nil {
		s.FilesSeen--
		return recs, used, cerr
	}
	s.File = nil
	s.FilesJudged++
	s.LangCounts[lang]++
	s.Chunks += cur.Windows
	return recs, used, nil
}

func (s *Scan) recordShape(t *Table, j Judge, c Cache, lang, rel, digest string, raw []byte, shape *FileShape, prior []Record) ([]Record, error) {
	if s.Progress == nil || !s.AskIntent {
		return nil, nil
	}
	test := t.IsTest(lang, rel)
	if shape == nil {
		b := newShapeBuilder(t, lang)
		b.add(string(raw), 1)
		top, _ := TopLevelDecls(string(raw), t.Langs[lang], 0)
		sh := b.finish(CountLines(string(raw)), top, test)
		sh.Tagged = lang == "go" && platformTagged(rel, string(raw))
		shape = &sh
		if err := s.Progress.Save(ShapeRecord(rel, digest, *shape)); err != nil {
			return nil, err
		}
	}
	if test || !OutlineAsked(shape) {
		return nil, nil
	}
	for _, r := range prior {
		if r.Battery == OutlineBattery && r.SourceDigest == digest && r.Catalogue == OutlineDigest() {
			return nil, nil
		}
	}
	or, err := AskOutline(j, c, rel, digest, lang, shape)
	switch {
	case err == nil:
	case errors.Is(err, errIntentPartial):
		or = failedIntent(or, "answered_in_part")
	case errors.Is(err, ErrRetryExhausted):
		or = failedIntent(or, "retry_exhausted")
	case errors.Is(err, ErrJudge) || errors.Is(err, ErrKey) || errors.Is(err, ErrCache):
		s.Calls += or.Calls
		s.SlopCalls += or.Calls
		return nil, err
	default:
		or = failedIntent(or, "refused")
	}
	s.Calls += or.Calls
	s.CacheHits += or.Cached
	s.SlopCalls += or.Calls
	return []Record{or}, s.Progress.Save(or)
}

func (s *Scan) skippedGo(rel, name, why string) error {
	if s.Progress == nil || !s.AskIntent || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || why == "test" {
		return nil
	}
	return s.Progress.Save(ShapeRecord(rel, "", FileShape{Skipped: why}))
}

const ScanCallsPerChunk = CallsPerChunk + 1

func (s *Scan) estimatedCalls(size int64) int {
	if !s.AskIntent {
		return EstimatedCallsAt(size, s.chunk())
	}
	chunk := s.chunk()
	return ScanCallsPerChunk*(int(size/int64(chunk))+1) + 1
}
