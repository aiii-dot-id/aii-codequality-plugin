package guest_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

const segmentBytes = 192 << 10

func realSources(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := os.Getenv("AII_CQ_REAL_DIR")
	if dir == "" {
		dir = "../.."
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	pkg := ""
	clause := regexp.MustCompile(`(?m)^package (\w+)`)
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(n)] = string(b)
		if m := clause.FindStringSubmatch(string(b)); m != nil && pkg == "" {
			pkg = m[1]
		}
	}
	if len(out) == 0 || pkg == "" {
		t.Fatalf("no Go files in %s", dir)
	}
	t.Logf("real files: %s, package %s, %d files", dir, pkg, len(out))
	return out, pkg
}

func planted(pkg string) map[string]string {
	var notes strings.Builder
	fmt.Fprintf(&notes, "package %s\n\n// zzR3Notes returns one.\nfunc zzR3Notes() int {\n\treturn 1\n}\n\n", pkg)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&notes, "// Note %d: the table below is read once at start and never written again.\n", i)
	}
	return map[string]string{

		"zz_r3_unused.go": fmt.Sprintf("package %s\n\nimport \"os\"\n\n// zzR3Unused writes data to name and closes it; nothing calls it.\nfunc zzR3Unused(name string, data []byte) error {\n\tf, err := os.Create(name)\n\tif err != nil {\n\t\treturn err\n\t}\n\tif _, err := f.Write(data); err != nil {\n\t\tf.Close()\n\t\treturn err\n\t}\n\tf.Close()\n\treturn nil\n}\n", pkg),

		"zz_r3_small.go": fmt.Sprintf("package %s\n\nfunc zzR3Small(a int) int {\n\treturn a + 1\n}\n", pkg),
		"zz_r3_headed.go": fmt.Sprintf("// Copyright 2026 Example\n// SPDX-License-Identifier: Apache-2.0\n\n//go:build !never\n\n// Package %s is planted for r3.\npackage %s\n\n"+
			"func zzR3Deep(n int) int {\n\tif n > 0 {\n\t\tfor i := 0; i < n; i++ {\n\t\t\tif i%%2 == 0 {\n\t\t\t\tswitch i {\n\t\t\t\tcase 2:\n\t\t\t\t\tif n > 3 {\n\t\t\t\t\t\treturn i\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t}\n\t\t}\n\t}\n\treturn 0\n}\n\n"+
			"func zzR3Many(a, b, c, d, e, f, g int) int {\n\treturn a + b + c + d + e + f + g + zzR3Deep(a)\n}\n", pkg, pkg),
		"zz_r3_notes.go": notes.String(),
	}
}

func bigGo(n int) string {
	var b strings.Builder
	b.WriteString("package big\n\n")
	for k := 0; b.Len()+200 < n; k++ {
		fmt.Fprintf(&b, "// F%d returns x plus %d.\nfunc F%d(x int) int {\n\tif x < 0 {\n\t\treturn -x + %d\n\t}\n\treturn x + %d\n}\n\n", k, k, k, k, k)
	}
	return b.String()
}

func realLarge(t *testing.T, real map[string]string, n int) string {
	t.Helper()
	src := ""
	for _, text := range real {
		if len(text) > len(src) {
			src = text
		}
	}
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				names[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					names[sp.Name.Name] = true
				case *ast.ValueSpec:
					for _, id := range sp.Names {
						names[id.Name] = true
					}
				}
			}
		}
	}
	var out strings.Builder
	out.WriteString(src)
	for k := 1; out.Len() < n; k++ {
		g, _ := parser.ParseFile(token.NewFileSet(), "", src, 0)
		ast.Inspect(g, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok && names[id.Name] {
				id.Name = fmt.Sprintf("%s_copy%d", id.Name, k)
			}
			return true
		})
		for _, d := range g.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				continue
			}
			out.WriteString("\n")
			if err := printer.Fprint(&out, token.NewFileSet(), d); err != nil {
				t.Fatal(err)
			}
			out.WriteString("\n")
		}
	}
	return out.String()
}

func countLines(s string) int {
	n := strings.Count(s, "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func scanToDone(t *testing.T, w *worker, b *broker, id string, each func(step int, r object)) object {
	t.Helper()
	posts := b.methods["http.post"]
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": id})
	for step := 0; step < 2000; step++ {
		env := w.invokeEnvelope("scan", object{"action": "step", "scan_id": id})
		if env["status"] != "succeeded" {
			t.Fatalf("step %d of %s: %s", step, id, raw(env))
		}
		r := env["operation_result"].(map[string]any)
		if each != nil {
			each(step, r)
		}
		switch r["status"] {
		case "done":
			t.Logf("scan %s done in %d steps: %s", id, step+1, raw(r))

			if sent := b.methods["http.post"] - posts; r["calls"] != float64(sent) {
				t.Errorf("scan %s reports %v calls; the judge received %d", id, r["calls"], sent)
			}
			return r
		case "judge_unavailable":
			b.nowMS += 600_000
		}
	}
	t.Fatalf("scan %s did not end", id)
	return nil
}

func reportOf(t *testing.T, w *worker, args object) (object, error) {
	t.Helper()
	env := w.invokeEnvelope("report", args)
	if env["status"] != "succeeded" {
		return nil, fmt.Errorf("refused: %v %v", env["reasonCode"], env["reason"])
	}
	return env["operation_result"].(map[string]any), nil
}

func save(t *testing.T, name string, v any) {
	if dir := os.Getenv("AII_CQ_OUT"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), raw(v), 0o644); err != nil {
			t.Error(err)
		}
	}
}

func schemaLanes(t *testing.T) []string {
	b, err := os.ReadFile("../../schemas/report_in.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties struct {
			Lane struct {
				Enum []string `json:"enum"`
			} `json:"lane"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil || len(s.Properties.Lane.Enum) == 0 {
		t.Fatalf("schema lanes: %v", err)
	}
	return s.Properties.Lane.Enum
}

func entryOf(review object, ref string) map[string]any {
	es, _ := review["entries"].([]any)
	for _, e := range es {
		if m := e.(map[string]any); m["ref"] == ref {
			return m
		}
	}
	return nil
}

func slopIDs(entry map[string]any) []string {
	var ids []string
	if entry == nil {
		return nil
	}
	ss, _ := entry["slop"].([]any)
	for _, s := range ss {
		ids = append(ids, s.(map[string]any)["id"].(string))
	}
	sort.Strings(ids)
	return ids
}

func TestR3EveryItemThroughTheEntryPoint(t *testing.T) {
	hash := candidateHash(t)

	t.Run("real files and planted", func(t *testing.T) {
		real, pkg := realSources(t)
		b := newBroker()
		b.failDocs = false
		b.sources, b.readAttempts, b.submitted = map[string]string{}, map[string]int{}, map[string]int{}
		for k, v := range real {
			b.sources[k] = v
		}
		for k, v := range planted(pkg) {
			b.sources[k] = v
		}
		w := start(t, b, hash)
		defer w.close()
		const id = "r3_entry_a"
		done := scanToDone(t, w, b, id, nil)

		type ask struct {
			name string
			args object
		}
		asks := []ask{{"summary", object{"shape": "summary"}}, {"findings", object{"shape": "findings"}}, {"coverage", object{"shape": "coverage", "limit": 100}}}
		for _, shape := range []string{"review_queue", "triage"} {
			asks = append(asks, ask{shape, object{"shape": shape, "limit": 100}}, ask{shape + "_tier_all", object{"shape": shape, "tier": "all", "limit": 100}})
			for _, lane := range schemaLanes(t) {
				asks = append(asks, ask{shape + "_" + lane, object{"shape": shape, "lane": lane, "limit": 100}})
			}
		}
		got := map[string]object{}
		for _, a := range asks {
			a.args["scan_id"] = id
			out, err := reportOf(t, w, a.args)
			if err != nil {
				t.Errorf("report %s: %v", a.name, err)
				continue
			}
			got[a.name] = out
			save(t, a.name, out)
			if rv, ok := out["review"].(map[string]any); ok {
				t.Logf("report %-28s entries %v of %v, gaps %v", a.name, len(rv["entries"].([]any)), rv["total_entries"], rv["coverage_gap_count"])
			} else {
				t.Logf("report %-28s %d bytes", a.name, len(raw(out)))
			}
		}
		sum := got["summary"]
		if sum == nil {
			t.Fatal("no summary")
		}

		headed, err := reportOf(t, w, object{"scan_id": id, "shape": "review_queue", "path": "zz_r3_headed.go", "tier": "all"})
		if err != nil {
			t.Errorf("case 2: %v", err)
		} else if ids := slopIDs(entryOf(headed["review"].(map[string]any), "zz_r3_headed.go")); !slices.Contains(ids, "SL-NEST") || !slices.Contains(ids, "SL-PARAMS") {
			t.Errorf("case 2: the headed file's slop is %v, want SL-NEST and SL-PARAMS", ids)
		} else {
			t.Logf("case 2: review_queue path=zz_r3_headed.go slop %v", ids)
		}
		save(t, "detail_headed", headed)
		if slop := got["review_queue_slop"]; slop == nil {
			t.Error("case 2: no slop lane to read the real files' unchecked ranges from")
		} else {
			rv := slop["review"].(map[string]any)
			for _, e := range rv["entries"].([]any) {
				if nc := e.(map[string]any)["not_checked"]; nc != nil {
					t.Errorf("case 2: %v has lines not checked: %v", e.(map[string]any)["ref"], nc)
				}
			}
			if notes := raw(rv["package_notes"]); bytes.Contains(notes, []byte("could not read")) {
				t.Errorf("case 2: package notes %s", notes)
			}
			t.Logf("slop lane package_notes: %s", raw(rv["package_notes"]))
		}

		want := map[string]float64{"large_file": 0, "very_large_file": 0}
		for name, text := range b.sources {
			switch n := countLines(text); {
			case n > 2000:
				want["very_large_file"]++
				t.Logf("size: %s %d lines, very large", name, n)
			case n > 1000:
				want["large_file"]++
				t.Logf("size: %s %d lines, large", name, n)
			}
		}
		if sf := sum["size_findings"].(map[string]any); sf["large_file"] != want["large_file"] || sf["very_large_file"] != want["very_large_file"] {
			t.Errorf("case 4: summary size_findings %v, the files have %v", sf, want)
		}

		detail, err := reportOf(t, w, object{"scan_id": id, "shape": "review_queue", "path": "zz_r3_unused.go", "tier": "all"})
		if err != nil {
			t.Errorf("case 6: %v", err)
		} else if ids := slopIDs(entryOf(detail["review"].(map[string]any), "zz_r3_unused.go")); !slices.Contains(ids, "SL-UNUSEDFUNC") {
			t.Errorf("case 6: review_queue path=zz_r3_unused.go carries slop %v, want SL-UNUSEDFUNC", ids)
		} else {
			t.Logf("case 6: review_queue path=zz_r3_unused.go slop %v", ids)
		}
		save(t, "detail_unused", detail)

		small, err := reportOf(t, w, object{"scan_id": id, "shape": "review_queue", "lane": "slop", "path": "zz_r3_small.go"})
		if err != nil {
			t.Errorf("case 6 slop-only file: %v", err)
		} else if ids := slopIDs(entryOf(small["review"].(map[string]any), "zz_r3_small.go")); !slices.Contains(ids, "SL-UNUSEDFUNC") {
			t.Errorf("case 6: review_queue lane=slop path=zz_r3_small.go carries slop %v, want SL-UNUSEDFUNC", ids)
		} else {
			t.Logf("case 6: review_queue lane=slop path=zz_r3_small.go slop %v", ids)
		}
		row, err := reportOf(t, w, object{"scan_id": id, "shape": "triage", "path": "zz_r3_unused.go", "tier": "all"})
		if err != nil {
			t.Errorf("case 6 triage: %v", err)
		} else if e := entryOf(row["review"].(map[string]any), "zz_r3_unused.go"); e == nil || e["slop"] == nil {
			t.Errorf("case 6: the triage row carries no slop: %s", raw(row["review"]))
		} else {
			t.Logf("case 6: triage path=zz_r3_unused.go slop %v %v", e["slop"], e["slop_top"])
		}

		notes, err := reportOf(t, w, object{"scan_id": id, "shape": "review_queue", "path": "zz_r3_notes.go", "tier": "all"})
		if err != nil {
			t.Errorf("no code: %v", err)
		} else if e := entryOf(notes["review"].(map[string]any), "zz_r3_notes.go"); e == nil || e["no_code_windows"] != 1.0 || strings.Contains(fmt.Sprint(e["reason_codes"]), "too_small") {
			t.Errorf("no code: the notes file's entry %s", raw(e))
		} else {
			t.Logf("no code: zz_r3_notes.go no_code_windows %v, reasons %v", e["no_code_windows"], e["reason_codes"])
		}
		if n, _ := sum["no_code_windows"].(float64); n < 1 {
			t.Errorf("no code: summary no_code_windows %v", sum["no_code_windows"])
		}

		lines := 0.0
		for _, text := range b.sources {
			lines += float64(countLines(text))
		}
		if sum["chunks_judged"] != done["chunks"] || sum["lines"] != lines {
			t.Errorf("A: summary chunks_judged %v lines %v; the scan judged %v windows of %v lines", sum["chunks_judged"], sum["lines"], done["chunks"], lines)
		}

		if s := raw(sum["slop"]); bytes.Contains(s, []byte("intent")) || !bytes.Contains(s, []byte(`"questions":{"answered":`)) {
			t.Errorf("B: summary slop %s", s)
		}

		c := done["calls_per_judged_file"].(map[string]any)
		est, spent := c["estimated_before_caching"].(float64), c["with_cache_hits"].(float64)-c["intent"].(float64)
		if est < spent {
			t.Errorf("C: estimated %v a file before caching; the files cost %v besides intent", est, spent)
		}
		t.Logf("C: estimated_before_caching %v; with_cache_hits %v, of which intent %v", est, c["with_cache_hits"], c["intent"])

		ign, err := reportOf(t, w, object{"scan_id": id, "shape": "summary", "lane": "slop"})
		if err != nil || fmt.Sprint(ign["ignored_arguments"]) != "[lane]" {
			t.Errorf("D: summary lane=slop: %v ignored_arguments %v", err, ign["ignored_arguments"])
		}
	})

	t.Run("case 1: a newline-free segment that is not the last", func(t *testing.T) {
		b := newBroker()
		b.failDocs = false
		raw := "package p\n\nconst big = `" + strings.Repeat("界", 70000) + "`\n\nfunc F() int { return 1 }\n"
		b.sources, b.readAttempts, b.submitted = map[string]string{"raw.go": raw}, map[string]int{}, map[string]int{}
		w := start(t, b, hash)
		defer w.close()
		done := scanToDone(t, w, b, "r3_entry_b", nil)
		if done["files_judged"] != 1.0 {
			t.Errorf("case 1: %d bytes, files judged %v", len(raw), done["files_judged"])
		}
	})

	t.Run("case 5: a large file edited between steps", func(t *testing.T) {
		b := newBroker()
		b.failDocs = false
		real, _ := realSources(t)
		src := realLarge(t, real, 2*segmentBytes+segmentBytes/2)
		marker := regexp.MustCompile(`(?m)^// [A-Za-z].*$`).FindString(src)
		if marker == "" || strings.Index(src, marker) > segmentBytes/2 {
			t.Fatalf("no comment early in the file to edit: %q", marker)
		}
		b.sources, b.readAttempts, b.submitted = map[string]string{"big.go": src}, map[string]int{}, map[string]int{}
		w := start(t, b, hash)
		defer w.close()
		const id = "r3_entry_c"
		final, edited, checkpointMost := src, false, 0
		scanToDone(t, w, b, id, func(step int, r object) {

			var state struct {
				File *struct {
					Next int `json:"next"`
				} `json:"file_cursor"`
			}
			newest := uint64(0)
			for _, slot := range []string{".checkpoint.0", ".checkpoint.1"} {
				var env struct {
					Generation uint64          `json:"generation"`
					State      json.RawMessage `json:"state"`
				}
				if json.Unmarshal(b.files["scans/"+id+slot], &env) == nil && env.Generation >= newest && env.State != nil {
					newest = env.Generation
					_ = json.Unmarshal(env.State, &state)
					checkpointMost = max(checkpointMost, len(b.files["scans/"+id+slot]))
				}
			}
			if !edited && state.File != nil && state.File.Next >= 1 {
				edited, final = true, strings.Replace(src, marker, marker+" (edited)", 1)
				b.sources["big.go"] = final
				t.Logf("case 5: edited segment 0 after step %d, cursor at segment %d", step, state.File.Next)
			}
		})
		if !edited {
			t.Fatal("case 5: the file was never part judged at a step's end")
		}
		t.Logf("case 5: largest checkpoint %d bytes (bound %d)", checkpointMost, 250<<10)
		cov, err := reportOf(t, w, object{"scan_id": id, "shape": "coverage", "limit": 100})
		if err != nil {
			t.Fatal(err)
		}
		save(t, "case5_coverage", cov)
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(final)))
		current := 0
		for _, e := range cov["review"].(map[string]any)["entries"].([]any) {
			m := e.(map[string]any)
			t.Logf("case 5: coverage %v %.8s %v", m["ref"], m["source_digest"], m["reason_codes"])
			if strings.Contains(fmt.Sprint(m["reason_codes"]), "source_changed") {
				continue
			}
			current++
			if m["source_digest"] != want {
				t.Errorf("case 5: finished under %v; the file reads %s", m["source_digest"], want)
			}
		}
		if current != 1 {
			t.Errorf("case 5: %d coverages not stale", current)
		}
	})

	t.Run("outline question: an outline question stopped once", func(t *testing.T) {
		b := newBroker()
		b.failDocs = false
		b.failOutline = 1
		real, _ := realSources(t)
		b.sources, b.readAttempts, b.submitted = map[string]string{"two.go": realLarge(t, real, segmentBytes+segmentBytes/2)}, map[string]int{}, map[string]int{}
		w := start(t, b, hash)
		defer w.close()
		const id = "r3_entry_d"
		scanToDone(t, w, b, id, nil)
		if b.failOutline != 0 {
			t.Fatal("the outline was never asked")
		}
		var answered, failed int
		for _, line := range bytes.Split(b.files["scans/"+id+".intent.jsonl"], []byte("\n")) {
			var ev struct {
				Record *struct {
					Battery string             `json:"battery"`
					Answers map[string]float64 `json:"answers"`
					Reasons []string           `json:"reason_codes"`
				} `json:"record"`
			}
			if json.Unmarshal(line, &ev) != nil || ev.Record == nil || ev.Record.Battery != "outline" {
				continue
			}
			if len(ev.Record.Answers) > 0 && !slices.Contains(ev.Record.Reasons, "intent_failed") {
				answered++
			} else {
				failed++
			}
		}
		t.Logf("outline question: outline records answered %d, failed %d", answered, failed)
		if answered+failed == 0 {
			t.Error("outline question: the outline question was lost: no record")
		}
	})
}
