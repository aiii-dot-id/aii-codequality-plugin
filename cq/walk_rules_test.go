package cq

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func walkFixture() *scopedRulesFS {
	return &scopedRulesFS{
		dirs:    map[string][]Entry{"": {{Name: ".gitignore"}, {Name: "pkg", Dir: true}}, "pkg": {{Name: ".gitignore"}, {Name: "sentinel.go", Size: int64(len(goFile))}}},
		files:   map[string]string{".gitignore": "", "pkg/.gitignore": "*.go\n", "pkg/sentinel.go": goFile},
		readErr: map[string]error{}, listErr: map[string]error{}, truncated: map[string]bool{},
	}
}
func rulesScan(t *testing.T, f FS) *Scan {
	t.Helper()
	s, e := NewScan(f, "rules", "fixture", "", nil, nil, true, 100, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func requireBoundary(t *testing.T, s *Scan, ref, reason string) {
	t.Helper()
	for _, c := range s.Observed {
		if c.Ref == ref && c.Kind == "subtree" && c.Disposition == "unknown" && contains(c.Reasons, reason) && contains(c.Reasons, "children_not_enumerated") {
			return
		}
	}
	t.Fatalf("missing boundary %q/%s: %+v", ref, reason, s.Observed)
}
func TestWalkRulesRootFailClosed(t *testing.T) {
	for _, kind := range []string{"unreadable", "truncated", "unlistable", "unsupported", "symlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			f := walkFixture()
			switch kind {
			case "unreadable":
				f.readErr[".gitignore"] = errors.New("denied")
			case "truncated":
				f.truncated[""] = true
			case "unlistable":
				f.listErr[""] = errors.New("denied")
			case "unsupported":
				f.files[".gitignore"] = "[ab].go\n"
			case "symlink":
				f.dirs[""][0].Symlink = true
			case "oversize":
				f.dirs[""][0].Size = maxScopedRulesBytes + 1
			}
			if s, e := NewScan(f, "rules", "fixture", "", nil, nil, true, 100, 1<<20); e == nil || s != nil {
				t.Fatalf("unsafe root accepted: %+v %v", s, e)
			}
			if contains(f.reads, "pkg/sentinel.go") || contains(f.lists, "pkg") {
				t.Fatalf("unsafe read/list: %v %v", f.reads, f.lists)
			}
		})
	}
}
func TestWalkRulesNestedFailureRetainsCursor(t *testing.T) {
	for _, kind := range []string{"unreadable", "truncated", "unlistable", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			f := walkFixture()
			s := rulesScan(t, f)
			reason := "rules_error"
			switch kind {
			case "unreadable":
				f.readErr["pkg/.gitignore"] = errors.New("denied")
			case "truncated":
				f.truncated["pkg"] = true
				reason = "dir_listing_truncated"
			case "unlistable":
				f.listErr["pkg"] = errors.New("denied")
				reason = "unlistable_dir"
			case "unsupported":
				f.files["pkg/.gitignore"] = "[ab].go\n"
			}
			j := &failJudge{}
			for attempt := 0; attempt < 2; attempt++ {
				if _, e := s.Step(table(t), f, j, nil, 20, 100); e == nil {
					t.Fatal("rules error was swallowed")
				}
				if !reflect.DeepEqual(s.Stack, []string{"pkg"}) || s.Status != "running" || s.LastError == "" || j.calls != 0 || contains(f.reads, "pkg/sentinel.go") {
					t.Fatalf("failure lost boundary: %+v reads=%v", s, f.reads)
				}
				requireBoundary(t, s, "pkg", reason)

				b, e := json.Marshal(s)
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(b, s); e != nil {
					t.Fatal(e)
				}
			}
			delete(f.readErr, "pkg/.gitignore")
			delete(f.listErr, "pkg")
			f.truncated["pkg"] = false
			f.files["pkg/.gitignore"] = "*.go\n"
			if _, e := s.Step(table(t), f, j, nil, 20, 100); e != nil || s.Status != "done" || j.calls != 0 || contains(f.reads, "pkg/sentinel.go") {
				t.Fatalf("repair did not retry safely: %v %+v", e, s)
			}
		})
	}
}
func TestWalkRulesPruneBeforeListing(t *testing.T) {
	for _, kind := range []string{"gitignore", "caller_exclude", "global_dir", "nested_repository", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := walkFixture()
			s := rulesScan(t, f)
			ref := "pkg"
			switch kind {
			case "gitignore":
				f.files[".gitignore"] = "pkg/\n"
				f.files["pkg/.gitignore"] = "!sentinel.go\n"
			case "caller_exclude":
				s.Exclude = []string{"pkg/"}
			case "global_dir":
				f.dirs[""][1].Name = "node_modules"
				ref = "node_modules"
			case "nested_repository":
				f.dirs["pkg"] = append(f.dirs["pkg"], Entry{Name: ".git"})
			case "symlink":
				f.dirs[""][1].Symlink = true
			}
			j := &failJudge{}
			if _, e := s.Step(table(t), f, j, nil, 20, 100); e != nil || s.Status != "done" || j.calls != 0 {
				t.Fatalf("boundary failed: %v %+v", e, s)
			}
			if kind != "nested_repository" && contains(f.lists, ref) {
				t.Fatalf("pruned directory listed: %v", f.lists)
			}
			if contains(f.reads, "pkg/sentinel.go") || contains(f.reads, "pkg/.gitignore") {
				t.Fatalf("boundary read: %v", f.reads)
			}
			requireBoundary(t, s, ref, kind)
		})
	}
}
func TestWalkRulesAttributesDoNotPruneDirectories(t *testing.T) {
	f := walkFixture()
	f.dirs[""] = append(f.dirs[""], Entry{Name: ".gitattributes"})
	f.files[".gitattributes"] = "pkg linguist-vendored\n"
	f.files["pkg/.gitignore"] = ""
	s := rulesScan(t, f)
	j := &fakeJudge{p: 0.1}
	if _, e := s.Step(table(t), f, j, MemCache{}, 20, 100); e != nil || s.Status != "done" || s.FilesJudged != 1 || !contains(f.reads, "pkg/sentinel.go") {
		t.Fatalf("attribute pruned directory: %v %+v", e, s)
	}
}
func TestWalkRulesPendingReloadAndPrunedAncestor(t *testing.T) {
	for _, kind := range []string{"file_exclusion", "rule_failure", "ancestor_exclusion", "ancestor_symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := walkFixture()
			f.files["pkg/.gitignore"] = ""
			s := rulesScan(t, f)
			j := &failJudge{}
			if _, e := s.Step(table(t), f, j, nil, 20, 100); e != nil || s.Status != "judge_unavailable" || len(s.Pending) != 1 {
				t.Fatalf("setup: %v %+v", e, s)
			}
			before := s.ProgressOffset
			switch kind {
			case "file_exclusion":
				f.files["pkg/.gitignore"] = "*.go\n"
			case "rule_failure":
				f.readErr["pkg/.gitignore"] = errors.New("denied")
			case "ancestor_exclusion":
				f.files[".gitignore"] = "pkg/\n"
			case "ancestor_symlink":
				f.dirs[""][1].Symlink = true
			}
			f.reads = nil
			f.lists = nil
			j.calls = 0
			_, e := s.Step(table(t), f, j, nil, 20, 100)
			if j.calls != 0 || contains(f.reads, "pkg/sentinel.go") {
				t.Fatalf("retry submitted excluded source: %v %v", j.calls, f.reads)
			}
			if kind == "rule_failure" {
				if e == nil || len(s.Pending) != 1 || s.ProgressOffset != before {
					t.Fatalf("failed rules advanced pending: %v %+v", e, s)
				}
				requireBoundary(t, s, "pkg", "rules_error")
			} else if e != nil || s.Status != "done" {
				t.Fatalf("retry: %v %+v", e, s)
			}
			if (kind == "ancestor_exclusion" || kind == "ancestor_symlink") && contains(f.lists, "pkg") {
				t.Fatalf("retry listed pruned child: %v", f.lists)
			}
		})
	}
}
func TestWalkRulesBudgetCoverage(t *testing.T) {
	for _, kind := range []string{"file_budget", "step_budget"} {
		t.Run(kind, func(t *testing.T) {
			f := walkFixture()
			s := rulesScan(t, f)

			s.Stack = []string{"pkg"}
			s.Dir = ""
			s.Pending = []Entry{{Name: "pending.go"}}
			if kind == "file_budget" {
				s.MaxFiles = 0
			}
			if _, e := s.Step(table(t), f, &failJudge{}, nil, 0, 0); e != nil {
				t.Fatal(e)
			}
			requireBoundary(t, s, "pkg", kind)
			found := false
			for _, c := range s.Observed {
				if c.Ref == "pending.go" && c.Disposition == "pending" && contains(c.Reasons, kind) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing pending file coverage: %+v", s.Observed)
			}
		})
	}
}

type failingBoundaryProgress struct{ memoryProgress }

func (p *failingBoundaryProgress) Cover(Coverage) error { return errors.New("coverage write failed") }
func TestWalkRulesBoundaryWriteFailureKeepsCursor(t *testing.T) {
	f := walkFixture()
	f.files[".gitignore"] = "pkg/\n"
	s := rulesScan(t, f)
	s.Progress = &failingBoundaryProgress{}
	if _, e := s.Step(table(t), f, &failJudge{}, nil, 20, 100); e == nil || !reflect.DeepEqual(s.Stack, []string{""}) || len(s.Pending) != 0 || s.ProgressOffset != 0 {
		t.Fatalf("coverage failure advanced cursor: %v %+v", e, s)
	}
}
func TestWalkRulesRootCoverageIsJournalValid(t *testing.T) {
	for _, reason := range []string{"rules_error", "unlistable_dir", "dir_listing_truncated", "file_budget", "step_budget"} {
		t.Run(reason, func(t *testing.T) {
			f := walkFixture()
			s := rulesScan(t, f)
			switch reason {
			case "rules_error":
				f.readErr[".gitignore"] = errors.New("denied")
			case "unlistable_dir":
				f.listErr[""] = errors.New("denied")
			case "dir_listing_truncated":
				f.truncated[""] = true
			case "file_budget":
				s.MaxFiles = 0
			case "step_budget":
				if e := s.budgetCoverage(reason); e != nil {
					t.Fatal(e)
				}
			}
			if reason != "step_budget" {
				_, e := s.Step(table(t), f, &failJudge{}, nil, 20, 100)
				if reason != "file_budget" && e == nil {
					t.Fatal("expected fatal root failure")
				}
			}
			requireBoundary(t, s, ".", reason)
			for i := range s.Observed {
				if e := validateJournalEvent(JournalEvent{Coverage: &s.Observed[i]}); e != nil {
					t.Fatalf("coverage cannot persist: %v", e)
				}
			}
		})
	}
}

func TestWalkRulesBudgetKeepsPartialProgressBoundary(t *testing.T) {
	s, tb, f := progressScan(t)
	p := &memoryProgress{}
	s.Progress = p
	if _, e := s.Step(tb, f, &progressJudge{remaining: 1}, nil, 1, 20); e != nil || s.Status != "judge_unavailable" {
		t.Fatalf("setup: %v %+v", e, s)
	}
	offset := s.ProgressOffset
	if _, e := s.Step(tb, f, &progressJudge{remaining: 0}, nil, 0, 0); e != nil || s.ProgressOffset != offset {
		t.Fatalf("budget swallowed partial records: %v before=%d after=%d", e, offset, s.ProgressOffset)
	}
	if _, e := s.Step(tb, f, &progressJudge{remaining: 1}, nil, 1, 20); e != nil || s.Status != "done" {
		t.Fatalf("partial retry: %v %+v", e, s)
	}
}
