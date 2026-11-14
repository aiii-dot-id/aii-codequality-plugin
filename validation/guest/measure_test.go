//go:build measure

package guest_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func manyWindows(file string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport \"os\"\n\n", strings.TrimSuffix(file, ".go"))
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "// F%d closes f; %s.\nfunc F%d(f *os.File) {\n", i, fixtureMarker(file), i)
		for k := 0; k < 40; k++ {
			fmt.Fprintf(&b, "\tx%d := %d * %d // padding line that makes the function about two KiB long\n\t_ = x%d\n", k, i, k, k)
		}
		b.WriteString("\t_ = f.Close()\n}\n\n")
	}
	return b.String()
}

type call struct {
	op   string
	args object
}

func probe(b *broker, hash string, ceiling int, calls []call) bool {
	bin, _ := filepath.Abs("bin/aii-plugin-worker")
	wasm, _ := filepath.Abs("bin/candidate.wasm")
	cmd := exec.Command(bin, "-forward", "-memory-max", fmt.Sprint(ceiling), "-module-sha256", "sha256:"+hash, wasm)
	cmd.Env = []string{}
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	errs, _ := cmd.StderrPipe()
	if cmd.Start() != nil {
		return false
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(errs)
		sent := false
		for sc.Scan() {
			if strings.Contains(sc.Text(), "event=ready") && !sent {
				sent = true
				ready <- true
			}
		}
		if !sent {
			ready <- false
		}
	}()
	select {
	case good := <-ready:
		if !good {
			return false
		}
	case <-time.After(20 * time.Second):
		return false
	}
	type frame struct {
		p []byte
		e error
	}
	for seq, c := range calls {
		args := object{"_host_now_ms": b.nowMS}
		for k, v := range c.args {
			args[k] = v
		}
		if sdk.WriteFrame(in, raw(object{"jsonrpc": "2.0", "id": seq + 1, "method": "invoke.call", "params": object{"operation": c.op, "arguments": args}}), sdk.MaxControlFrameBytes) != nil {
			return false
		}
		for {
			done := make(chan frame, 1)
			go func() { p, e := sdk.ReadFrame(out, sdk.MaxControlFrameBytes); done <- frame{p, e} }()
			var f frame
			select {
			case f = <-done:
			case <-time.After(60 * time.Second):
				return false
			}
			if f.e != nil {
				return false
			}
			var m map[string]json.RawMessage
			if json.Unmarshal(f.p, &m) != nil {
				return false
			}
			if meth, exists := m["method"]; exists {
				var method string
				_ = json.Unmarshal(meth, &method)
				res, er := b.dispatch(method, m["params"])
				reply := map[string]json.RawMessage{"jsonrpc": raw("2.0"), "id": m["id"]}
				if er != nil {
					reply["error"] = er
				} else {
					reply["result"] = res
				}
				if sdk.WriteFrame(in, raw(reply), sdk.MaxControlFrameBytes) != nil {
					return false
				}
				continue
			}
			var r struct {
				Status string `json:"status"`
			}
			if m["error"] != nil || json.Unmarshal(m["result"], &r) != nil || r.Status != "succeeded" {
				return false
			}
			break
		}
	}
	return true
}

func minCeiling(t *testing.T, lo, hi int, calls []call, b *broker, hash string) int {
	t.Helper()
	ok := func(mib int) bool {
		c := *b
		c.files = map[string][]byte{}
		for k, v := range b.files {
			c.files[k] = v
		}
		c.methods = map[string]int{}
		return probe(&c, hash, mib<<20, calls)
	}
	if !ok(hi) {
		t.Fatalf("fails at the %d MiB ceiling", hi)
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if ok(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return hi
}

func TestMeasurePopulatedIntentReport(t *testing.T) {
	hash := candidateHash(t)
	b := newBroker()
	b.failDocs = false
	b.sources, b.readAttempts, b.submitted = map[string]string{}, map[string]int{}, map[string]int{}
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		b.sources[name] = manyWindows(name, 11)
	}
	b.extraSettings = object{"chunk_bytes": 2048, "max_calls_per_step": 64}
	w := start(t, b, hash)
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "m"})
	for i := 0; i < 600; i++ {
		v := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": "m"}))
		if v["status"] != "running" {
			if v["status"] != "done" {
				t.Fatalf("scan ended %v", v["status"])
			}
			break
		}
	}
	w.close()
	answers := strings.Count(string(b.files["scans/m.intent.jsonl"]), "\n")
	records := 0
	for _, line := range strings.Split(string(b.files["scans/m.intent.jsonl"]), "\n") {
		records += strings.Count(line, `"EH-01 @ `) / 2
	}
	t.Logf("intent file: %d records, %d EH-01 answers, %d bytes; journal %d bytes", answers, records, len(b.files["scans/m.intent.jsonl"]), len(b.files["scans/m.v2.jsonl"]))
	if records < 1000 {
		t.Fatalf("%d intent answers, want at least 1000", records)
	}
	reports := []call{{"report", object{"scan_id": "m", "shape": "triage"}}, {"report", object{"scan_id": "m", "shape": "summary"}},
		{"report", object{"scan_id": "m", "shape": "review_queue", "tier": "all"}}}
	t0 := time.Now()
	if !probe(b, hash, 64<<20, reports) {
		t.Fatal("reports fail at 64 MiB")
	}
	took := time.Since(t0)
	t.Logf("MEASURE populated-intent reports: %v for triage+summary+review_queue at 64 MiB, worker start included; lowest passing ceiling %d MiB",
		took, minCeiling(t, 2, 64, reports, b, hash))
}

func loopFixtures(t *testing.T) (a, b, c string) {
	t.Helper()
	p := os.Getenv("CQ_LOOP_GO")
	if p == "" {
		t.Skip("CQ_LOOP_GO names no loop.go")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	a = string(raw)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", a, parser.ParseComments)
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
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					names[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names[n.Name] = true
					}
				}
			}
		}
	}
	var out strings.Builder
	out.WriteString(a)
	for k := 1; ; k++ {
		g, _ := parser.ParseFile(token.NewFileSet(), "", a, 0)
		ast.Inspect(g, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && names[id.Name] {
				id.Name = fmt.Sprintf("%s_copy%d", id.Name, k)
			}
			return true
		})
		var copyText strings.Builder
		for _, d := range g.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				continue
			}
			copyText.WriteString("\n")
			if err := printer.Fprint(&copyText, token.NewFileSet(), d); err != nil {
				t.Fatal(err)
			}
			copyText.WriteString("\n")
		}
		if out.Len()+copyText.Len()+64 > 1<<20 {
			break
		}
		out.WriteString(copyText.String())
	}
	pad := 1<<20 - out.Len() - 5
	out.WriteString("\n// " + strings.Repeat("x", pad) + "\n")
	b = out.String()
	if len(b) != 1<<20 {
		t.Fatalf("fixture B is %d bytes", len(b))
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "", b, parser.SkipObjectResolution); err != nil {
		t.Fatalf("fixture B does not parse: %v", err)
	}
	return a, b, b + "x"
}

func TestMeasureLoopFixtures(t *testing.T) {
	hash := candidateHash(t)
	a, bfix, c := loopFixtures(t)
	for _, fx := range []struct {
		name, src string
		calls     bool
	}{{"A loop.go", a, true}, {"B 1 MiB", bfix, true}, {"C 1 MiB + 1", c, false}} {
		b := newBroker()
		b.failDocs = false
		b.sources, b.readAttempts, b.submitted = map[string]string{"loop.go": fx.src}, map[string]int{}, map[string]int{}
		run := []call{{"scan", object{"action": "start", "path": "fixture", "scan_id": "m"}}}
		for i := 0; i < 80; i++ {
			run = append(run, call{"scan", object{"action": "step", "scan_id": "m"}})
		}
		run = append(run, call{"report", object{"scan_id": "m", "shape": "triage"}}, call{"report", object{"scan_id": "m", "shape": "summary"}})
		counted := *b
		counted.files, counted.methods = map[string][]byte{}, map[string]int{}
		t0 := time.Now()
		if !probe(&counted, hash, 64<<20, run) {
			t.Fatalf("%s: the scan fails at 64 MiB", fx.name)
		}
		took := time.Since(t0)
		state := string(counted.files["scans/m.state.json"])
		if fx.calls && counted.httpSuccess == 0 || !fx.calls && counted.httpSuccess != 0 {
			t.Fatalf("%s: %d judge calls", fx.name, counted.httpSuccess)
		}
		journal := string(counted.files["scans/m.v2.jsonl"])
		if fx.calls {

			expected, completed := 0, 0
			noCode := map[int]bool{}
			for _, line := range strings.Split(journal, "\n") {
				var e struct {
					Record *struct {
						Ref     string `json:"ref"`
						Battery string `json:"battery"`
						Chunk   int    `json:"chunk"`
						Skipped string `json:"skipped"`
					} `json:"record"`
					Coverage *struct {
						Ref       string `json:"ref"`
						Expected  int    `json:"expected_chunks"`
						Completed int    `json:"completed_chunks"`
					} `json:"coverage"`
				}
				if json.Unmarshal([]byte(line), &e) != nil {
					continue
				}
				if e.Record != nil && e.Record.Ref == "loop.go" && e.Record.Battery == "code" && e.Record.Skipped == "too_small" {
					noCode[e.Record.Chunk] = true
				}
				if e.Coverage != nil && e.Coverage.Ref == "loop.go" && e.Coverage.Expected > 0 {
					expected, completed = e.Coverage.Expected, e.Coverage.Completed
				}
			}
			t.Logf("%s: %d windows expected, %d completed, %d with no code to ask", fx.name, expected, completed, len(noCode))
			if expected == 0 || completed+len(noCode) != expected {
				t.Fatalf("%s: not judged whole", fx.name)
			}
		}
		if !fx.calls && !strings.Contains(journal, `"larger_than_1_mib"`) {
			t.Fatalf("%s: not out of scope: %s", fx.name, journal)
		}
		_ = state
		t.Logf("MEASURE %s: %d bytes, %d judge calls (%d code, %d docs, %d choice), %v at 64 MiB; lowest passing ceiling %d MiB",
			fx.name, len(fx.src), counted.httpSuccess, counted.codeCalls, counted.docsCalls, counted.choiceCalls, took, minCeiling(t, 2, 64, run, b, hash))
	}
}

func TestMeasureLoopPhases(t *testing.T) {
	hash := candidateHash(t)
	_, bfix, _ := loopFixtures(t)
	b := newBroker()
	b.failDocs = false
	b.sources, b.readAttempts, b.submitted = map[string]string{"loop.go": bfix}, map[string]int{}, map[string]int{}
	first := []call{{"scan", object{"action": "start", "path": "fixture", "scan_id": "m"}}, {"scan", object{"action": "step", "scan_id": "m"}}}
	scan := append([]call{}, first...)
	for i := 0; i < 80; i++ {
		scan = append(scan, call{"scan", object{"action": "step", "scan_id": "m"}})
	}
	t.Logf("MEASURE phase first step: lowest ceiling %d MiB", minCeiling(t, 2, 64, first, b, hash))
	t.Logf("MEASURE phase whole scan, no reports: lowest ceiling %d MiB", minCeiling(t, 2, 64, scan, b, hash))
	done := *b
	done.files, done.methods = map[string][]byte{}, map[string]int{}
	if !probe(&done, hash, 64<<20, scan) {
		t.Fatal("scan fails at 64 MiB")
	}
	reports := []call{{"report", object{"scan_id": "m", "shape": "triage"}}, {"report", object{"scan_id": "m", "shape": "summary"}}}
	t.Logf("MEASURE phase reports over the finished scan: lowest ceiling %d MiB", minCeiling(t, 2, 64, reports, &done, hash))
}
