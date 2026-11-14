package guest_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func r4Shape(shape string) (string, string) {
	var b strings.Builder
	n := strconv.Itoa
	name := "big.go"
	switch shape {
	case "one-line declarations":
		b.WriteString("package p\n\n")
		for k := 0; b.Len()+40 < 1<<20; k++ {
			b.WriteString("func f" + n(k) + "() {}\n")
		}
	case "one-line statements, Python":
		name = "big.py"
		for k := 0; b.Len()+40 < 1<<20; k++ {
			b.WriteString("def f" + n(k) + "(): return " + n(k) + "\n")
		}
	case "one function":
		b.WriteString("package p\n\nfunc F(x int) int {\n")
		for k := 0; b.Len()+60 < 1<<20; k++ {
			b.WriteString("\tif x == " + n(k) + " {\n\t\treturn " + n(k*k) + "\n\t}\n")
		}
		b.WriteString("\treturn 0\n}\n")
	case "one literal":
		b.WriteString("package p\n\nvar T = map[int]string{\n")
		for k := 0; b.Len()+40 < 1<<20; k++ {
			b.WriteString("\t" + n(k) + ": \"v" + n(k) + "\",\n")
		}
		b.WriteString("}\n")
	}
	return name, b.String()
}

func stepOrEnd(t *testing.T, w *worker, id string, limit time.Duration) (object, string) {
	t.Helper()
	w.seq++
	want := raw(w.seq)
	w.write(raw(object{"jsonrpc": "2.0", "id": w.seq, "method": "invoke.call", "params": object{"operation": "scan",
		"arguments": object{"action": "step", "scan_id": id, "_host_now_ms": w.b.nowMS}}}))
	deadline := time.After(limit)
	for {
		type frame struct {
			p []byte
			e error
		}
		got := make(chan frame, 1)
		go func() { p, e := sdk.ReadFrame(w.out, sdk.MaxControlFrameBytes); got <- frame{p, e} }()
		var f frame
		select {
		case f = <-got:
		case <-deadline:
			return nil, "nothing from the worker within " + limit.String()
		}
		if f.e != nil {
			lines := strings.Split(strings.TrimSpace(w.stderr.text()), "\n")
			return nil, "worker ended: " + lines[len(lines)-1]
		}
		var m map[string]json.RawMessage
		if e := json.Unmarshal(f.p, &m); e != nil {
			t.Fatal(e)
		}
		if meth, exists := m["method"]; exists {
			var method string
			if e := json.Unmarshal(meth, &method); e != nil {
				t.Fatal(e)
			}
			res, er := w.b.dispatch(method, m["params"])
			reply := map[string]json.RawMessage{"jsonrpc": raw("2.0"), "id": m["id"]}
			if er != nil {
				reply["error"] = er
			} else {
				reply["result"] = res
			}
			w.write(raw(reply))
			continue
		}
		if !bytes.Equal(m["id"], want) || m["error"] != nil || m["result"] == nil {
			t.Fatalf("invalid reply %s", f.p)
		}
		r := objectOf(t, m["result"])
		if r["status"] != "succeeded" {
			t.Fatalf("step refused: %s", raw(r))
		}
		return r["operation_result"].(map[string]any), ""
	}
}

func endWorker(w *worker) {
	w.closed = true
	_ = w.in.Close()
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
}

func TestR4AFileTheWorkerCannotFinishIsRefusedByName(t *testing.T) {
	hash := candidateHash(t)
	if memoryLimit != 64<<20 {
		t.Fatalf("the harness's limit is %d, not the host's 64 MiB", memoryLimit)
	}
	const id = "r4_refuse"
	small := func(name string) string {
		return "package p\n\n// " + name + " returns x doubled, for the files on either side of the large one.\nfunc " + name + "(x int) int {\n\treturn x * 2\n}\n"
	}
	for _, shape := range []string{"one-line declarations", "one-line statements, Python", "one function", "one literal"} {
		t.Run(shape, func(t *testing.T) {
			name, src := r4Shape(shape)
			b := newBroker()
			b.failDocs = false
			b.sources = map[string]string{"a_before.go": small("Before"), "b_before.go": small("Second"), name: src, "z_after.go": small("After")}
			b.readAttempts, b.submitted = map[string]int{}, map[string]int{}
			w := start(t, b, hash)
			w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": id})
			ended := 0
			var last object
			began := time.Now()
			for step := 0; step < 40; step++ {
				at := time.Now()
				r, how := stepOrEnd(t, w, id, 100*time.Second)
				if how != "" {
					ended++
					t.Logf("step %d, %.1f s: %s", step, time.Since(at).Seconds(), how)
					endWorker(w)
					if ended > 2 {
						t.Fatalf("the worker was ended %d times and the scan is still at %s: it does not get past the file", ended, name)
					}
					w = start(t, b, hash)
					continue
				}
				last = r
				t.Logf("step %d, %.1f s: status %v, judged %v, seen %v", step, time.Since(at).Seconds(), r["status"], r["files_judged"], r["files_seen"])
				if r["status"] == "done" {
					break
				}
				if r["status"] == "judge_unavailable" {
					b.nowMS += 600_000
				}
			}
			defer w.close()
			if last == nil || last["status"] != "done" {
				t.Fatalf("the scan did not end done: %s", raw(last))
			}
			if last["files_seen"] != 4.0 {
				t.Errorf("files seen %v; want 4", last["files_seen"])
			}
			sum, err := reportOf(t, w, object{"scan_id": id})
			if err != nil {
				t.Fatalf("the scan cannot be reported after %d endings: %v", ended, err)
			}
			view, _ := sum["scan"].(map[string]any)
			nf, _ := view["not_finished"].(map[string]any)
			if ended < 2 {

				if last["files_judged"] != 4.0 || nf != nil {
					t.Fatalf("after %d endings: judged %v, not_finished %s; want all 4 judged and nothing given up", ended, last["files_judged"], raw(nf))
				}
				t.Logf("%s: %d bytes JUDGED after %d endings, %.0f s", shape, len(src), ended, time.Since(began).Seconds())
				return
			}
			files, _ := nf["files"].([]any)
			if nf["count"] != 1.0 || len(files) != 1 || files[0] != name || !strings.Contains(fmt.Sprint(nf["why"]), "not assessed") {
				t.Fatalf("the summary's scan does not name %s as given up, with its reason: %s", name, raw(nf))
			}
			if last["files_judged"] != 3.0 {
				t.Errorf("files judged %v; want the 3 beside it", last["files_judged"])
			}
			t.Logf("%s: %d bytes REFUSED BY NAME after %d endings, %.0f s: %v", shape, len(src), ended, time.Since(began).Seconds(), nf["why"])
		})
	}
}
