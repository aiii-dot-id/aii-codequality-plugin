package cq

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type scopedRulesFS struct {
	dirs      map[string][]Entry
	files     map[string]string
	listErr   map[string]error
	readErr   map[string]error
	truncated map[string]bool
	lists     []string
	reads     []string
	limits    []int64
	ignoreCap bool
}

func (f *scopedRulesFS) List(dir string) ([]Entry, bool, error) {
	f.lists = append(f.lists, dir)
	if err := f.listErr[dir]; err != nil {
		return nil, false, err
	}
	es, ok := f.dirs[dir]
	if !ok {
		return nil, false, fmt.Errorf("unexpected listing %q", dir)
	}
	return es, f.truncated[dir], nil
}

func (f *scopedRulesFS) Read(file string, max int64) ([]byte, error) {
	f.reads = append(f.reads, file)
	f.limits = append(f.limits, max)
	if err := f.readErr[file]; err != nil {
		return nil, err
	}
	b, ok := f.files[file]
	if !ok {
		return nil, fmt.Errorf("forbidden non-rule or absent-file read %q", file)
	}
	if int64(len(b)) > max && !f.ignoreCap {
		return nil, ErrTooLarge
	}
	return []byte(b), nil
}

func TestScopedRulesNestedExcludesAndNegation(t *testing.T) {
	f := &scopedRulesFS{
		dirs: map[string][]Entry{
			"repo":          {{Name: ".gitignore"}, {Name: "pkg", Dir: true}, {Name: "unrelated", Dir: true}},
			"repo/pkg":      {{Name: ".gitignore"}, {Name: "deep", Dir: true}, {Name: "sentinel.go"}},
			"repo/pkg/deep": {{Name: ".gitignore"}, {Name: "sentinel.go"}},
		},
		files: map[string]string{
			"repo/.gitignore":          "# comment\n\n*.go\n!keep.go\ncache/\n/root.txt\na/b.txt\n",
			"repo/pkg/.gitignore":      "!*.go\nsentinel.go\n/local.txt\nlogs/\n",
			"repo/pkg/deep/.gitignore": "!sentinel.go\n/again.go\n",
			"repo/pkg/sentinel.go":     "SENTINEL_SECRET_DO_NOT_READ",
		},
	}
	ign, attrs, err := scopedRules(f, "repo", "repo/pkg")
	if err != nil || len(attrs) != 0 {
		t.Fatalf("load: %v attrs=%v", err, attrs)
	}
	for _, tc := range []struct {
		name      string
		dir, want bool
	}{
		{"other.go", false, true}, {"keep.go", false, false},
		{"pkg/normal.go", false, false}, {"pkg/sentinel.go", false, true},
		{"pkg/deep/sentinel.go", false, true}, {"pkg/local.txt", false, true},
		{"pkg/deep/local.txt", false, false}, {"local.txt", false, false},
		{"other/local.txt", false, false}, {"other/normal.go", false, true},
		{"root.txt", false, true}, {"pkg/root.txt", false, false},
		{"a/b.txt", false, true}, {"pkg/a/b.txt", false, false},
		{"pkg/logs", true, true}, {"pkg/logs", false, false},
		{"pkg/logs/x.txt", false, true}, {"other/logs/x.txt", false, false},
		{"pkg/cache/x.txt", false, true},
	} {
		if got := matchRules(ign, tc.name, tc.dir); got != tc.want {
			t.Errorf("%q directory=%v: got %v want %v", tc.name, tc.dir, got, tc.want)
		}
	}

	s := &Scan{Base: "repo", Excluded: map[string]int{}}
	records, calls, err := s.file(&Table{}, f, nil, nil, Entry{Name: "sentinel.go"}, "repo/pkg/sentinel.go", ign, attrs, nil)
	if err != nil || len(records) != 0 || calls != 0 || s.Excluded["gitignore"] != 1 {
		t.Fatalf("sentinel reached submission boundary: records=%v calls=%d exclusions=%v err=%v", records, calls, s.Excluded, err)
	}
	if !reflect.DeepEqual(f.lists, []string{"repo", "repo/pkg"}) ||
		!reflect.DeepEqual(f.reads, []string{"repo/.gitignore", "repo/pkg/.gitignore"}) {
		t.Fatalf("unexpected FS effects: lists=%v reads=%v", f.lists, f.reads)
	}
	ign, _, err = scopedRules(f, "repo", "repo/pkg/deep")
	if err != nil || matchRules(ign, "pkg/deep/sentinel.go", false) ||
		!matchRules(ign, "pkg/deep/again.go", false) || !matchRules(ign, "pkg/sentinel.go", false) {
		t.Fatalf("deep override/order failed: %v", err)
	}
}

func TestScopedRulesAttributesAndGlobGrammar(t *testing.T) {
	f := &scopedRulesFS{
		dirs: map[string][]Entry{"": {{Name: ".gitattributes"}}, "pkg[1]": {{Name: ".gitattributes"}, {Name: ".gitignore"}}},
		files: map[string]string{
			".gitattributes":        "# header\nthird/** linguist-vendored\n*.gen linguist-generated=true linguist-documentation\n",
			"pkg[1]/.gitattributes": "/doc.md linguist-documentation=true\nlocal linguist-vendored=true\n",
			"pkg[1]/.gitignore":     "**/cache?.txt\n/path/**/secret.*\nfile*  \r\n",
		},
	}
	ign, attrs, err := scopedRules(f, ".", "pkg[1]")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"third/x.go": true, "pkg[1]/x.gen": true, "pkg[1]/doc.md": true,
		"doc.md": false, "pkg[1]/deep/doc.md": false, "pkg1/doc.md": false,
		"pkg[1]/local": true, "pkg[1]/local/child.go": false,
	} {
		if got := matchAttrs(attrs, name); got != want {
			t.Errorf("attribute %q = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"pkg[1]/cache1.txt": true, "pkg[1]/a/cachex.txt": true,
		"cache1.txt": false, "pkg[1]/cache11.txt": false,
		"pkg[1]/path/secret.go": true, "pkg[1]/path/a/secret.go": true,
		"pkg[1]/a/path/secret.go": false, "pkg[1]/file1": true,
	} {
		if got := matchRules(ign, name, false); got != want {
			t.Errorf("ignore %q = %v, want %v", name, got, want)
		}
	}
}

func TestScopedRulesFailuresReturnNoPartialRules(t *testing.T) {
	denied := errors.New("permission denied")
	for _, mode := range []string{"list", "read", "vanished", "truncated", "symlink", "directory", "size", "read-size", "bad-fs-size", "aggregate"} {
		t.Run(mode, func(t *testing.T) {
			f := &scopedRulesFS{
				dirs:    map[string][]Entry{"": {{Name: ".gitignore"}}, "sub": {{Name: ".gitignore"}}},
				files:   map[string]string{".gitignore": "*.go\n", "sub/.gitignore": "*.secret\n"},
				listErr: map[string]error{}, readErr: map[string]error{}, truncated: map[string]bool{},
			}
			var want error
			switch mode {
			case "list":
				f.listErr["sub"], want = denied, denied
			case "read":
				f.readErr["sub/.gitignore"], want = denied, denied
			case "vanished":
				f.readErr["sub/.gitignore"], want = fs.ErrNotExist, fs.ErrNotExist
			case "truncated":
				f.truncated["sub"] = true
			case "symlink":
				f.dirs["sub"][0].Symlink = true
			case "directory":
				f.dirs["sub"][0].Dir = true
			case "size":
				f.dirs["sub"][0].Size, want = maxRulesBytes+1, ErrTooLarge
			case "read-size", "bad-fs-size":
				f.files["sub/.gitignore"] = strings.Repeat("#", maxRulesBytes+1)
				f.ignoreCap, want = mode == "bad-fs-size", ErrTooLarge
			case "aggregate":
				f.files[".gitignore"] = "#" + strings.Repeat("a", maxScopedRulesBytes/2)
				f.files["sub/.gitignore"] = "#" + strings.Repeat("b", maxScopedRulesBytes/2)
				want = ErrTooLarge
			}
			ign, attrs, err := scopedRules(f, "", "sub")
			if err == nil || ign != nil || attrs != nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("expected closed failure %v, got ign=%v attrs=%v err=%v", want, ign, attrs, err)
			}
			if len(f.reads) > 2 {
				t.Fatalf("unexpected reads: %v", f.reads)
			}
		})
	}
}

func TestScopedRulesExactAggregateBudgetAndAbsentFiles(t *testing.T) {
	f := &scopedRulesFS{
		dirs:  map[string][]Entry{"": {{Name: ".gitignore"}, {Name: ".gitattributes"}}, "sub": {}},
		files: map[string]string{".gitignore": "#" + strings.Repeat("x", maxScopedRulesBytes/2-1), ".gitattributes": "#" + strings.Repeat("y", maxScopedRulesBytes/2-1)},
	}
	if _, _, err := scopedRules(f, "", "sub"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.reads, []string{".gitignore", ".gitattributes"}) ||
		!reflect.DeepEqual(f.limits, []int64{maxScopedRulesBytes, maxScopedRulesBytes / 2}) {
		t.Fatalf("read bounds/absence: %v %v", f.reads, f.limits)
	}
}

func TestScopedRulesRefuseUnsupportedGrammar(t *testing.T) {
	for _, attr := range []bool{false, true} {
		lines := []string{"!", "/", "[z-a]", "[]", "[!]", "a[b/c]d", "[[:alpha:]]", "a]b", "[ab", "\\#literal", "name\\ with-space", "a/**b", "a/***/b", "a//b", "../secret", "a/./b", "\"quoted\"", "é.go", "bad\x00name", "tab\tname"}
		name := ".gitignore"
		if attr {

			name = ".gitattributes"
			lines = []string{"*.go linguist-vendored=yes", "*.go -linguist-vendored=true", "*.go linguist-unknown", "[attr]vendor linguist-vendored", "[attr]docs -linguist-documentation", "!*.go linguist-vendored", "dir/ linguist-vendored", "\"a b\" linguist-vendored", "é.go linguist-generated", "a/**b linguist-generated", "[z-a] linguist-vendored"}
		}
		for _, line := range lines {
			t.Run(name+"/"+line, func(t *testing.T) {
				f := &scopedRulesFS{dirs: map[string][]Entry{"": {{Name: name}}}, files: map[string]string{name: line}}
				ign, attrs, err := scopedRules(f, "", "")
				if err == nil || ign != nil || attrs != nil || !strings.Contains(err.Error(), "line 1") {
					t.Fatalf("unsupported grammar accepted: %v %v %v", ign, attrs, err)
				}
			})
		}
	}
}

func TestScopedRulesRejectPathsOutsideBaseBeforeIO(t *testing.T) {
	for _, paths := range [][2]string{{"repo", "other"}, {"repo", "repo2/sub"}, {"repo", ""}, {"/repo", "/repo"}, {"", "../escape"}, {"", "a/../b"}, {"", "a\\b"}} {
		f := &scopedRulesFS{}
		if _, _, err := scopedRules(f, paths[0], paths[1]); err == nil || len(f.lists) != 0 || len(f.reads) != 0 {
			t.Fatalf("paths %v: err=%v lists=%v reads=%v", paths, err, f.lists, f.reads)
		}
	}
}

func TestScopedRulesIgnoreAttributesWithoutExclusionMeaning(t *testing.T) {
	bodies := map[string]string{
		"extra": "* -text\n*.patch whitespace=-blank-at-eol\n*.png binary\n*.bin -diff -merge\n" +
			"[attr]crlf text eol=crlf\n*.bat crlf\n*.go\ndir/ -text\n!neg -text\n\"a b\" text\n" +
			"*.psd filter=lfs diff=lfs merge=lfs -text\n*.c linguist-language=C\ndocs/** linguist-detectable\n" +
			"*.md -linguist-detectable\n  *.txt text=auto\n",
	}
	for _, fixture := range []string{"aii-os.gitattributes", "aii-plugin-sdk.gitattributes"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "rules", fixture))
		if err != nil {
			t.Fatal(err)
		}
		bodies[fixture] = string(raw)
	}
	for name, body := range bodies {
		f := &scopedRulesFS{dirs: map[string][]Entry{"": {{Name: ".gitattributes"}}}, files: map[string]string{".gitattributes": body}}
		ign, attrs, err := scopedRules(f, "", "")
		if err != nil || len(ign) != 0 || len(attrs) != 0 {
			t.Fatalf("%s: attributes without exclusion meaning: ign=%v attrs=%v err=%v", name, ign, attrs, err)
		}
	}
}

func TestScopedRulesLinguistAttributesResolvePerName(t *testing.T) {
	f := &scopedRulesFS{dirs: map[string][]Entry{"": {{Name: ".gitattributes"}}}, files: map[string]string{".gitattributes": "" +
		"vendor/** linguist-vendored -diff\n" +
		"vendor/keep.go -linguist-vendored\n" +
		"gen/** linguist-generated=true\n" +
		"gen/*.go linguist-vendored=false\n" +
		"docs/** linguist-documentation\n" +
		"docs/api.md !linguist-documentation\n" +
		"*.pb.go linguist-generated linguist-vendored text\n" +
		"x.pb.go linguist-generated=false\n" +
		"y.pb.go -linguist-generated -linguist-vendored\n"}}
	_, attrs, err := scopedRules(f, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"vendor/a.go": true, "vendor/keep.go": false, "gen/a.go": true, "docs/x.md": true, "docs/api.md": false,
		"a.pb.go": true, "x.pb.go": true, "y.pb.go": false, "src/main.go": false,
	} {
		if got := matchAttrs(attrs, name); got != want {
			t.Errorf("attribute %q = %v, want %v", name, got, want)
		}
	}
}

func TestScopedRulesIgnoreCharacterClasses(t *testing.T) {
	f := &scopedRulesFS{dirs: map[string][]Entry{"": {{Name: ".gitignore"}}}, files: map[string]string{".gitignore": "" +
		"*.py[cod]\n[Bb]uild/\nlog[0-9].txt\n[!a]x.c\ndir/a[!b]c\nx[^y]z\nMy Folder/\n"}}
	ign, _, err := scopedRules(f, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		dir, want bool
	}{
		{"a.pyc", false, true}, {"pkg/a.pyo", false, true}, {"a.pyx", false, false}, {"a.py", false, false},
		{"Build", true, true}, {"sub/build", true, true}, {"rebuild", true, false},
		{"log5.txt", false, true}, {"logx.txt", false, false},
		{"bx.c", false, true}, {"d/ax.c", false, false}, {"d/bx.c", false, true},
		{"dir/aqc", false, true}, {"dir/abc", false, false}, {"dir/a/c", false, false},
		{"xaz", false, true}, {"xyz", false, false},
		{"My Folder", true, true},
	} {
		if got := matchRules(ign, tc.name, tc.dir); got != tc.want {
			t.Errorf("%q directory=%v: got %v want %v", tc.name, tc.dir, got, tc.want)
		}
	}
}

func TestScanAcceptsRealRepositoryRules(t *testing.T) {
	tb := table(t)
	for _, files := range []map[string]string{
		{".gitattributes": "aii-os.gitattributes", ".gitignore": "aii-os.gitignore"},
		{".gitattributes": "aii-plugin-sdk.gitattributes"},
	} {
		root := t.TempDir()
		for name, fixture := range files {
			raw, err := os.ReadFile(filepath.Join("testdata", "rules", fixture))
			if err != nil {
				t.Fatal(err)
			}
			write(t, root, name, string(raw))
		}
		write(t, root, "shells/android/gradlew.bat", "@echo off\r\n")
		write(t, root, "a.go", goFile)
		write(t, root, "data/secret.go", goFile)
		fs := &OSFS{Root: root}
		s, err := NewScan(fs, "cq_rules", "src", "", nil, nil, false, 100, 131072)
		if err != nil {
			t.Fatalf("%v: scan start refused: %v", files, err)
		}
		j := &fakeJudge{p: 0.1}
		ignored := 0
		if files[".gitignore"] != "" {
			ignored = 1
		}
		if _, err := s.Step(tb, fs, j, MemCache{}, 64, 50); err != nil || s.Status != "done" || s.FilesJudged != 2-ignored ||
			s.Excluded["gitignore"] != ignored || s.Excluded["gitattributes"] != 0 || j.calls == 0 {
			t.Fatalf("%v: err %v status %s judged %d excluded %v", files, err, s.Status, s.FilesJudged, s.Excluded)
		}
	}
}
