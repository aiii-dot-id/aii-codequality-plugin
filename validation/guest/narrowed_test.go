package guest_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func fixtureMarker(name string) string { return "fixture_" + strings.ReplaceAll(name, ".", "_") }

func boundedSource(name string, size, generation int) string {
	prefix := fmt.Sprintf("package p\n// f returns synthetic text.\nfunc f() string { return `%s generation-%d\n", fixtureMarker(name), generation)
	suffix := "` }\n"
	n := size - len(prefix) - len(suffix)
	var middle strings.Builder
	for i := 0; ; i++ {
		line := fmt.Sprintf("%s gen-%d row-%08d <>&\"\\\t界\n", fixtureMarker(name), generation, i)
		if middle.Len()+len(line) > n {
			break
		}
		middle.WriteString(line)
	}
	middle.WriteString(strings.Repeat("x", n-middle.Len()))
	return prefix + middle.String() + suffix
}

func fixtureSizes(cap int) map[string]int {
	return map[string]int{"exact.go": cap, "second.go": 64 << 10, "plusone.go": cap + 1, "known155361.go": 155361, "known164364.go": 164364, "known229298.go": 229298}
}

func TestNarrowedFixtureOracle(t *testing.T) {
	for _, cap := range []int{128 << 10, 192 << 10} {
		seen := map[[32]byte]bool{}
		for generation := 0; generation < 2; generation++ {
			for name, n := range fixtureSizes(cap) {
				text := boundedSource(name, n, generation)
				if len(text) != n || !strings.Contains(text, fixtureMarker(name)) {
					t.Fatal("wrong fixture size/marker")
				}
				if _, err := parser.ParseFile(token.NewFileSet(), name, text, 0); err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256([]byte(text))
				if seen[hash] {
					t.Fatal("fixture collision")
				}
				seen[hash] = true
			}
		}
	}
}

func TestGuestNarrowedReleaseSustained(t *testing.T) {
	for _, cap := range []int{128 << 10, 192 << 10} {
		t.Run(fmt.Sprintf("cap_%d", cap), func(t *testing.T) { runNarrowedWorkload(t, cap) })
	}
}

func runNarrowedWorkload(t *testing.T, cap int) {
	hash := candidateHash(t)
	b := newBroker()
	b.failDocs = false
	w := start(t, b, hash)
	pid := w.cmd.Process.Pid
	totalSteps := 0
	t.Cleanup(func() {
		t.Logf("observed pid=%d restarts=0 steps=%d source_reads=%d http=%d max_body=%d escaping=%d max_params=%d max_frame=%d max_result=%d ceiling=%d", pid, totalSteps, b.sourceReads, b.methods["http.post"], b.maxBody, b.maxEscaping, b.maxParams, w.maxFrame, w.maxResult, memoryLimit)
	})
	{
		b.maxFileBytes = 0
		if cap == 192<<10 {
			b.maxFileBytes = cap
		}
		sizes := fixtureSizes(cap)
		for scan := 0; scan < 3; scan++ {
			generation := min(scan, 1)
			b.sources = map[string]string{}
			b.readAttempts = map[string]int{}
			b.submitted = map[string]int{}
			eligible := 0
			for name, n := range sizes {
				b.sources[name] = boundedSource(name, n, generation)
				if n <= cap {
					eligible++
				}
			}
			id := fmt.Sprintf("narrow_%d_%d", cap, scan)
			callsBefore := b.methods["http.post"]
			w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": id})
			var done object
			steps := 0
			for steps < 40 {
				steps++
				totalSteps++
				beforeCalls := b.methods["http.post"]
				beforeJournal := append([]byte(nil), b.files["scans/"+id+".v2.jsonl"]...)
				r := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": id}))
				if b.methods["http.post"] != b.httpSuccess {
					t.Fatal("all-success workload encountered HTTP refusal/failure")
				}
				if w.cmd.Process.Pid != pid || w.closed {
					t.Fatal("worker replaced or closed")
				}
				for _, shape := range []string{"review_queue", "coverage"} {
					narrowReport(t, w, id, shape)
				}
				assertNoOversizeIO(t, b, sizes, cap)
				if r["status"] == "done" {
					done = r
					break
				}
				if isCallBudgetPause(r) {
					if b.methods["http.post"] == beforeCalls || bytes.Equal(beforeJournal, b.files["scans/"+id+".v2.jsonl"]) {
						t.Fatal("call-budget continuation made no HTTP/journal progress")
					}
				} else if r["status"] != "running" {
					t.Fatalf("unexpected status, no retry allowed: %s", raw(r))
				}
			}
			if done == nil || done["files_judged"] != float64(eligible) || done["chunks"].(float64) < float64(eligible*2) {
				t.Fatalf("incomplete scan %s: steps=%d result=%s", id, steps, raw(done))
			}
			delta := b.methods["http.post"] - callsBefore
			if scan < 2 && (steps < 2 || delta < eligible*4) {
				t.Fatalf("not a sustained noncached workload: steps=%d calls=%d", steps, delta)
			}
			if scan == 2 && (delta != 0 || done["cache_hits"].(float64) == 0) {
				t.Fatalf("identical scan missed cache: HTTP=%d result=%s", delta, raw(done))
			}
			for name, n := range sizes {
				if n <= cap && (b.readAttempts["fixture/"+name] == 0 || (scan < 2 && b.submitted[name] == 0)) {
					t.Fatalf("eligible file not read/submitted: %s reads=%d submissions=%d", name, b.readAttempts["fixture/"+name], b.submitted[name])
				}
			}
			q := narrowReport(t, w, id, "review_queue")
			c := narrowReport(t, w, id, "coverage")
			assertNarrowReports(t, q, c, sizes, cap)
			journalName := "scans/" + id + ".v2.jsonl"
			journal := append([]byte(nil), b.files[journalName]...)
			assertNarrowJournal(t, journal, sizes, cap)
			reads := b.sourceReads
			calls := b.methods["http.post"]
			for i := 0; i < 10; i++ {
				if !bytes.Equal(raw(q), raw(narrowReport(t, w, id, "review_queue"))) || !bytes.Equal(raw(c), raw(narrowReport(t, w, id, "coverage"))) {
					t.Fatal("report replay drift")
				}
			}
			w.invoke("scan", object{"action": "step", "scan_id": id})
			if !bytes.Equal(journal, b.files[journalName]) || calls != b.methods["http.post"] || reads != b.sourceReads {
				t.Fatal("reports/completed step mutate journal or perform source/HTTP work")
			}
			if !bytes.Equal(raw(q), raw(narrowReport(t, w, id, "review_queue"))) || !bytes.Equal(raw(c), raw(narrowReport(t, w, id, "coverage"))) {
				t.Fatal("completed-step report drift")
			}
			t.Logf("COMPLETED scan=%s cap=%d setting=%d generation=%d files=%d eligible=%d steps=%d HTTP=%d cache_hits=%v journal_bytes=%d", id, cap, b.maxFileBytes, generation, len(sizes), eligible, steps, delta, done["cache_hits"], len(journal))
		}
	}
	if b.maxEscaping < 1000 || b.maxBody > bodyLimit || b.codeCalls == 0 || b.docsCalls == 0 || b.choiceCalls == 0 {
		t.Fatal("escaping/battery/request exercise missing")
	}
	w.close()
}

func narrowReport(t *testing.T, w *worker, id, shape string) object {
	t.Helper()
	r := objectOf(t, w.invoke("report", object{"scan_id": id, "shape": shape, "limit": 100, "page_bytes": 200000}))["review"].(map[string]any)
	if r["next_page_cursor"] != "" || r["total_entries"] != float64(len(r["entries"].([]any))) {
		t.Fatalf("fixture report unexpectedly paginated: %s", raw(r))
	}
	return r
}

func assertNoOversizeIO(t *testing.T, b *broker, sizes map[string]int, cap int) {
	t.Helper()
	for name, n := range sizes {
		if n > cap && (b.readAttempts["fixture/"+name] != 0 || b.submitted[name] != 0) {
			t.Fatalf("oversize file read/submitted: %s size=%d reads=%d submissions=%d", name, n, b.readAttempts["fixture/"+name], b.submitted[name])
		}
	}
}

func assertNarrowReports(t *testing.T, q, c object, sizes map[string]int, cap int) {
	t.Helper()
	for _, r := range []object{q, c} {
		if r["inventory_complete"] != true || r["report_complete"] != true || r["assessment_complete"] != false {
			t.Fatalf("coverage/completeness overstated or lost: %s", raw(r))
		}
	}
	rows := map[string]object{}
	for _, v := range q["entries"].([]any) {
		r := v.(map[string]any)
		ref := r["ref"].(string)
		if rows[ref] != nil {
			t.Fatal("duplicate review ref")
		}
		rows[ref] = r
	}
	gaps := map[string]object{}
	for _, v := range c["entries"].([]any) {
		r := v.(map[string]any)
		ref := r["ref"].(string)
		if gaps[ref] != nil {
			t.Errorf("duplicate coverage ref %q: coverage=%s", ref, raw(c))
		}
		gaps[ref] = r
	}
	eligible := 0
	for _, n := range sizes {
		if n <= cap {
			eligible++
		}
	}

	if len(rows) != eligible || len(gaps) != len(sizes) {
		t.Fatalf("lost files/coverage: queue=%s coverage=%s", raw(q), raw(c))
	}
	for name, n := range sizes {
		r, g := rows[name], gaps[name]
		if g == nil {
			t.Fatalf("missing coverage %s", name)
		}
		if n > cap {
			if r != nil || g["disposition"] != "unknown" || g["size"] != float64(n) || string(raw(g["reason_codes"])) != `["oversize"]` {
				t.Fatalf("oversize masquerades as assessed/excluded: %s %s", raw(r), raw(g))
			}
		} else {
			if r == nil || (r["assessment"] != "partial" && r["assessment"] != "unknown") || r["coverage"].(map[string]any)["disposition"] != "complete" || r["missing"] != float64(0) || r["answered"].(float64) == 0 || g["disposition"] != r["assessment"] || g["expected_chunks"] != g["completed_chunks"] || g["completed_chunks"].(float64) < 2 || !bytes.Contains(raw(g["reason_codes"]), []byte("context_limited")) {
				t.Fatalf("incomplete admitted file: %s %s", raw(r), raw(g))
			}
			findings := r["findings"].([]any)
			if len(findings) == 0 {
				t.Fatal("lost findings")
			}
			seen := map[string]bool{}
			for _, f := range findings {
				v := f.(map[string]any)
				key := string(raw(v))
				if seen[key] || v["id"] != "EH-01" || v["model"] != model || v["p"] != 0.988 {
					t.Fatalf("duplicate/wrong finding: %s", raw(v))
				}
				seen[key] = true
			}
		}
	}
}

func assertNarrowJournal(t *testing.T, data []byte, sizes map[string]int, cap int) {
	t.Helper()
	if len(data) == 0 {
		t.Fatal("missing journal")
	}
	records := map[string]object{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		e := objectOf(t, line)
		if v, ok := e["record"]; ok {
			r := v.(map[string]any)
			id, ok := r["record_id"].(string)
			if !ok || id == "" {
				t.Fatal("missing record ID")
			}
			records[id] = r
		}
	}
	refs := map[string]int{}
	for _, r := range records {
		ref := r["ref"].(string)
		n, known := sizes[ref]
		if !known || n > cap {
			t.Fatalf("oversize/unknown assessment record: %s", raw(r))
		}
		refs[ref]++
		answers := r["answers"].(map[string]any)
		for _, id := range r["expected"].([]any) {
			p, ok := answers[id.(string)].(float64)
			if !ok || p < 0 || p > 1 {
				t.Fatalf("missing/invalid persisted answer: %s", raw(r))
			}
		}
		if p, ok := answers["EH-01"]; ok && p != 0.987654321 {
			t.Fatal("raw precision lost")
		}
	}
	for name, n := range sizes {
		if n <= cap && refs[name] < 4 {
			t.Fatalf("missing multi-window battery evidence: %s records=%d", name, refs[name])
		}
	}
}
