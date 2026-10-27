package guest_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func releaseInvoke(t *testing.T, w *worker, op string, args, settings object) (object, []string) {
	t.Helper()
	a := objectOf(t, raw(args))
	a["_host_now_ms"] = w.b.nowMS
	w.seq++
	id := raw(w.seq)
	w.write(raw(object{"jsonrpc": "2.0", "id": w.seq, "method": "invoke.call", "params": object{"operation": op, "arguments": a}}))
	var calls []string
	for {
		p := w.read()
		var m map[string]json.RawMessage
		if err := json.Unmarshal(p, &m); err != nil {
			t.Fatal(err)
		}
		if string(m["jsonrpc"]) != `"2.0"` {
			t.Fatalf("bad JSON-RPC: %s", p)
		}
		if meth, exists := m["method"]; exists {
			var method string
			if err := json.Unmarshal(meth, &method); err != nil {
				t.Fatal(err)
			}
			params := objectOf(t, m["params"])
			operation, _ := params["operation"].(string)
			calls = append(calls, operation)
			res, er := w.b.dispatch(method, m["params"])
			if method == "invoke.call" && operation == "settings.get" && er == nil {
				envelope := objectOf(t, res)
				values := envelope["operation_result"].(map[string]any)["values"].(map[string]any)
				for k, v := range settings {
					values[k] = v
				}
				res = raw(envelope)
			}
			reply := map[string]json.RawMessage{"jsonrpc": raw("2.0"), "id": m["id"]}
			if er != nil {
				reply["error"] = er
			} else {
				reply["result"] = res
			}
			w.write(raw(reply))
			continue
		}
		if !bytes.Equal(m["id"], id) || m["error"] != nil || m["result"] == nil {
			t.Fatalf("invalid reply: %s", p)
		}
		r := objectOf(t, m["result"])
		n := len(raw(r["operation_result"]))
		if op == "report" && n > 262144 {
			t.Fatalf("report exceeds declared result limit: %d", n)
		}
		w.maxResult = max(w.maxResult, n)
		return r, calls
	}
}

func releaseSuccess(t *testing.T, envelope object) object {
	t.Helper()
	if envelope["status"] != "succeeded" {
		t.Fatalf("operation failed: %s", raw(envelope))
	}
	result, ok := envelope["operation_result"].(map[string]any)
	if !ok {
		t.Fatalf("missing object result: %s", raw(envelope))
	}
	return result
}

func TestGuestReleaseScopeJudgeUTF8BatchPreflight(t *testing.T) {
	hash := candidateHash(t)

	const capBytes = 1024
	text := strings.Repeat("界", capBytes/3) + "aa"
	if !utf8.ValidString(text) || len(text) != capBytes+1 || utf8.RuneCountInString(text) >= capBytes {
		t.Fatal("bad UTF-8 boundary fixture")
	}
	b := newBroker()
	b.failDocs = false
	w := start(t, b, hash)
	settings := object{"max_file_bytes": capBytes, "chunk_bytes": 131072}
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversize_later_%t", later), func(t *testing.T) {
			items := []any{object{"ref": "oversize.go", "language": "go", "text": text}}
			if later {
				items = append([]any{object{"ref": "valid.go", "language": "go", "text": source}}, items...)
			}
			before := raw(b.files)
			r, calls := releaseInvoke(t, w, "judge", object{"items": items}, settings)
			encoded := string(raw(r))
			if r["status"] != "failed" || !strings.Contains(encoded, "OPERATION_ARGUMENT_INVALID") || !strings.Contains(encoded, "UTF-8 bytes exceeds max_file_bytes") || !strings.Contains(encoded, "oversize.go") {
				t.Errorf("not the UTF-8 byte guard: %s", encoded)
			}

			if string(raw(calls)) != `["settings.get"]` || !bytes.Equal(before, raw(b.files)) {
				t.Errorf("batch touched host state before refusal: calls=%s", raw(calls))
			}
		})
	}

	prefix := "package p\n// "
	exact := prefix + strings.Repeat("界", (capBytes-len(prefix)-1)/3)
	exact += strings.Repeat("a", capBytes-len(exact)-1) + "\n"
	if len(exact) != capBytes || !utf8.ValidString(exact) {
		t.Fatal("bad exact-cap fixture")
	}
	before := b.methods["http.post"]
	r, _ := releaseInvoke(t, w, "judge", object{"items": []any{object{"ref": "exact.go", "language": "go", "text": exact}}}, settings)
	out := releaseSuccess(t, r)
	if out["complete"] != true || len(out["records"].([]any)) == 0 || b.methods["http.post"] <= before {
		t.Fatalf("exact cap not assessed: %s", raw(out))
	}
	w.close()
}

func releaseSetSavedCap(t *testing.T, b *broker, id string, capBytes int) {
	t.Helper()
	name := "scans/" + id + ".state.json"
	s := objectOf(t, b.files[name])
	s["max_file_bytes"] = capBytes
	b.files[name] = raw(s)
	for slot := 0; slot < 2; slot++ {
		name = fmt.Sprintf("scans/%s.checkpoint.%d", id, slot)
		if len(b.files[name]) == 0 {
			continue
		}
		envelope := objectOf(t, b.files[name])
		state := envelope["state"].(map[string]any)
		state["max_file_bytes"] = capBytes
		envelope["digest"] = fmt.Sprintf("%x", sha256.Sum256(raw(state)))
		b.files[name] = raw(envelope)
	}
}

func releaseScanFiles(b *broker, id string) []byte {
	files := map[string][]byte{}
	for name, data := range b.files {
		if strings.HasPrefix(name, "scans/"+id+".") {
			files[name] = data
		}
	}
	return raw(files)
}

func TestGuestReleaseScopeAdvanceSkipsSavedCap(t *testing.T) {
	hash := candidateHash(t)
	b := newBroker()
	w := start(t, b, hash)
	const oldID, newID = "cq_1800000000000", "cq_1800000000001"
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": oldID})
	partial := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": oldID}))
	if partial["status"] != "judge_unavailable" || len(b.files["scans/"+oldID+".v2.jsonl"]) == 0 {
		t.Fatal("old scan has no retained partial evidence")
	}
	releaseSetSavedCap(t, b, oldID, 1<<20)
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": newID})
	oldBytes := releaseScanFiles(b, oldID)
	newBytes := releaseScanFiles(b, newID)
	b.failDocs = false
	b.nowMS += 120000
	beforeHTTP := b.methods["http.post"]
	r, _ := releaseInvoke(t, w, "advance", object{}, object{"scheduled_steps": true})
	out := releaseSuccess(t, r)
	if out["advanced"] != newID || out["status"] != "done" || out["files_judged"] != float64(1) || b.methods["http.post"] <= beforeHTTP || bytes.Equal(newBytes, releaseScanFiles(b, newID)) {
		t.Fatalf("newer supported scan made no durable assessment progress: %s", raw(out))
	}
	releaseAssertSkipped(t, out, oldID)
	if !bytes.Equal(oldBytes, releaseScanFiles(b, oldID)) {
		t.Fatal("advance mutated old scan state/checkpoints/journal")
	}

	beforeAll := raw(b.files)
	beforeHTTP = b.methods["http.post"]
	r, _ = releaseInvoke(t, w, "advance", object{}, object{"scheduled_steps": true})
	out = releaseSuccess(t, r)
	if out["advanced"] != "" || out["reason"] != "no eligible unfinished scan" {
		t.Fatalf("no-eligible result: %s", raw(out))
	}
	releaseAssertSkipped(t, out, oldID)
	if !bytes.Equal(beforeAll, raw(b.files)) || b.methods["http.post"] != beforeHTTP {
		t.Fatal("report-only advance performed work")
	}

	report := objectOf(t, w.invoke("report", object{"scan_id": oldID, "shape": "summary"}))
	scan := report["scan"].(map[string]any)
	if scan["report_only_reason"] == nil || scan["chunks"] != partial["chunks"] {
		t.Fatalf("old evidence/report-only reason lost: %s", raw(report))
	}
	w.close()
}

func releaseAssertSkipped(t *testing.T, out object, id string) {
	t.Helper()
	skipped, ok := out["report_only"].([]any)
	if !ok || len(skipped) != 1 {
		t.Fatalf("missing skipped scan: %s", raw(out))
	}
	item := skipped[0].(map[string]any)
	reason, _ := item["reason"].(string)
	if item["scan_id"] != id || !strings.Contains(reason, "file cap") {
		t.Fatalf("wrong skip reason: %s", raw(item))
	}
}

func releaseStripEstimates(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "steps_left")
		delete(x, "progress")
		for _, child := range x {
			releaseStripEstimates(child)
		}
	case []any:
		for _, child := range x {
			releaseStripEstimates(child)
		}
	}
}
func releaseNoEstimates(t *testing.T, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == "steps_left" || k == "progress" {
				t.Errorf("unfounded estimate %s: %v", k, child)
			}
			releaseNoEstimates(t, child)
		}
	case []any:
		for _, child := range x {
			releaseNoEstimates(t, child)
		}
	}
}

func TestGuestReleaseScopeReportsInvalidSettings(t *testing.T) {
	hash := candidateHash(t)
	b := newBroker()
	w := start(t, b, hash)
	const id = "retained"
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": id})
	partial := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": id}))
	if partial["status"] != "judge_unavailable" || b.codeCalls != 1 || b.docsCalls != 1 || len(b.files["scans/"+id+".v2.jsonl"]) == 0 {
		t.Fatalf("fixture lacks retained partial assessment: %s", raw(partial))
	}
	shapes := []string{"list", "summary", "records", "review_queue", "coverage"}
	args := map[string]object{}
	baseline := map[string]object{}
	for _, shape := range shapes {
		a := object{"scan_id": id, "shape": shape}
		if shape == "list" {
			a = object{}
		}
		args[shape] = a
		r, _ := releaseInvoke(t, w, "report", a, nil)
		out := releaseSuccess(t, r)
		if out["assessment_configuration_error"] != nil {
			t.Fatal("valid settings warned")
		}

		if shape == "summary" && out["scan"].(map[string]any)["steps_left"] == nil {
			t.Fatal("valid unfinished scan has no estimate")
		}
		switch shape {
		case "list":
			if len(out["scans"].([]any)) != 1 {
				t.Fatal("scan list empty")
			}
		case "records":
			if len(out["records"].([]any)) == 0 {
				t.Fatal("records empty")
			}
		case "review_queue", "coverage":
			if len(out["review"].(map[string]any)["entries"].([]any)) == 0 {
				t.Fatal("review evidence empty")
			}
		}
		releaseStripEstimates(out)
		baseline[shape] = out
	}
	for _, invalid := range []struct {
		key   string
		value any
	}{{"max_file_bytes", 1 << 20}, {"backend", "invalid-backend"}, {"max_calls_per_step", 0}} {
		for _, shape := range shapes {
			t.Run(invalid.key+"/"+shape, func(t *testing.T) {
				before := raw(b.files)
				reads, http := b.sourceReads, b.methods["http.post"]
				r, calls := releaseInvoke(t, w, "report", args[shape], object{invalid.key: invalid.value})
				out := releaseSuccess(t, r)
				warning, _ := out["assessment_configuration_error"].(string)
				if !strings.Contains(warning, invalid.key) {
					t.Errorf("missing setting-specific warning: %s", raw(out))
				}
				releaseNoEstimates(t, out)
				delete(out, "assessment_configuration_error")
				if !bytes.Equal(raw(baseline[shape]), raw(out)) {
					t.Errorf("retained evidence changed: valid=%s invalid=%s", raw(baseline[shape]), raw(out))
				}
				for _, call := range calls {
					if call != "settings.get" && call != "fs.read" && call != "fs.list" {
						t.Errorf("read-only report called %s", call)
					}
				}
				if !bytes.Equal(before, raw(b.files)) || b.sourceReads != reads || b.methods["http.post"] != http {
					t.Error("report mutated storage or assessed source")
				}
			})
		}
	}
	w.close()
}
