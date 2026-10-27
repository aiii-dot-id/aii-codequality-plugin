package guest_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const source = "package p\n\n// f processes work; sample contract only.\nfunc f() {\n println(\"work-item-long-enough-for-code-assessment\")\n}\n\nfunc g() { println(\"second-unit-for-localization\") }\n"
const model = "jev-1.0.0"
const endpoint = "https://fixture.invalid/v1/systemone"
const bodyLimit = 256 << 10
const memoryLimit = 64 << 20

type object = map[string]any

func raw(v any) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func ok(v any) json.RawMessage { return raw(object{"status": "succeeded", "operation_result": v}) }
func failed(code string) json.RawMessage {
	return raw(object{"status": "failed", "reasonCode": code, "reason": code})
}
func denied() json.RawMessage {
	return raw(object{"code": -32000, "message": "synthetic broker denied", "data": object{"reasonCode": "POLICY_DENY"}})
}

type broker struct {
	files                                                 map[string][]byte
	sources                                               map[string]string
	readAttempts                                          map[string]int
	submitted                                             map[string]int
	maxFileBytes                                          int
	failDocs                                              bool
	source, baseURL, configuredModel, responseModel       string
	nowMS                                                 int64
	maxEscaping, sourceReads                              int
	httpSuccess                                           int
	codeCalls, docsCalls, choiceCalls, maxBody, maxParams int
	methods                                               map[string]int
}

func newBroker() *broker {
	return &broker{files: map[string][]byte{}, failDocs: true, methods: map[string]int{}, source: source, baseURL: "https://fixture.invalid", configuredModel: model, responseModel: model, nowMS: 1800000000000}
}

func (b *broker) dispatch(method string, params json.RawMessage) (json.RawMessage, json.RawMessage) {
	if method != "invoke.call" {
		return nil, denied()
	}
	var p struct {
		Operation string            `json:"operation"`
		Target    map[string]string `json:"target"`
		Arguments struct {
			Body        string `json:"body"`
			ContentType string `json:"content_type"`
			Auth        string `json:"auth_profile"`
			Data        string `json:"data_b64"`
			Append      bool   `json:"append"`
			Offset      int    `json:"offset"`
			Length      int    `json:"length"`
			Digest      bool   `json:"digest"`
		} `json:"arguments"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil, denied()
	}
	b.maxParams = max(b.maxParams, len(params))
	b.methods[p.Operation]++
	switch p.Operation {
	case "settings.get":
		values := object{"backend": "local", "local_judge_url": b.baseURL, "model": b.configuredModel, "scheduled_steps": false, "max_calls_per_step": 12, "retry_calls": 12}
		if b.maxFileBytes != 0 {
			values["max_file_bytes"] = b.maxFileBytes
		}
		return ok(object{"values": values}), nil
	case "http.post":
		if p.Target["url"] != b.baseURL+"/v1/systemone" || p.Arguments.Auth != "" || p.Arguments.ContentType != "application/json" {
			return nil, denied()
		}
		if len(p.Arguments.Body) > bodyLimit {
			return failed("NET_REQUEST_TOO_BIG"), nil
		}
		b.maxBody = max(b.maxBody, len(p.Arguments.Body))
		b.maxEscaping = max(b.maxEscaping, len(raw(p.Arguments.Body))-len(p.Arguments.Body))
		var req struct {
			Model     string                    `json:"model"`
			State     map[string]any            `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if json.Unmarshal([]byte(p.Arguments.Body), &req) != nil || req.Model != b.configuredModel || len(req.Questions) == 0 {
			return nil, denied()
		}

		state := string(raw(req.State))
		for name := range b.sources {
			if strings.Contains(state, fixtureMarker(name)) {
				b.submitted[name]++
			}
		}
		docs := req.State["source"] != nil
		choices := false
		for _, q := range req.Questions {
			if q["type"] == "Choice" || q["type"] == "choice" || q["options"] != nil {
				choices = true
			}
		}
		if choices {
			b.choiceCalls++
		} else if docs {
			b.docsCalls++
		} else {
			b.codeCalls++
		}
		if docs && b.failDocs {
			return raw(object{"status": "failed", "reasonCode": "NET_REMOTE_OUTCOME_FAILED", "operation_result": object{"http_status": 503, "retry_after_s": 120, "body": object{"error": "injected docs failure"}}}), nil
		}
		answers := object{}
		for id := range req.Questions {
			if choices {
				answers[id] = object{"choice": "cannot_localize", "confidence": 0.99}
			} else {
				p := 0.123456789
				if id == "EH-01" {
					p = 0.987654321
				}
				answers[id] = object{"type": "Noul", "noul": p}
			}
		}
		b.httpSuccess++
		return ok(object{"http_status": 200, "content_type": "application/json", "body": object{"model": b.responseModel, "answers": answers}}), nil
	case "fs.list", "fs.read", "fs.write", "fs.delete":
		root, name := p.Target["root"], p.Target["path"]
		if path.IsAbs(name) || strings.Contains(name, "\\") || strings.Contains(name, "\x00") || (name != "" && path.Clean(name) != name) || name == ".." || strings.HasPrefix(name, "../") {
			return nil, denied()
		}
		if root == "sandbox" {
			if b.sources != nil {
				if p.Operation == "fs.list" && name == "fixture" {
					keys := make([]string, 0, len(b.sources))
					for key := range b.sources {
						keys = append(keys, key)
					}
					sort.Strings(keys)
					entries := []any{}
					for _, key := range keys {
						entries = append(entries, object{"name": key, "dir": false, "size": len(b.sources[key])})
					}
					return ok(object{"entries": entries, "truncated": false}), nil
				}
				if p.Operation == "fs.read" {
					b.readAttempts[name]++
					if text, exists := b.sources[strings.TrimPrefix(name, "fixture/")]; exists && strings.HasPrefix(name, "fixture/") {
						b.sourceReads++
						return fileRead([]byte(text), p.Arguments.Offset, p.Arguments.Length, p.Arguments.Digest), nil
					}
				}
				return nil, denied()
			}
			if p.Operation == "fs.list" && name == "fixture" {
				return ok(object{"entries": []any{object{"name": "f.go", "dir": false, "size": len(b.source)}}, "truncated": false}), nil
			}
			if p.Operation == "fs.read" && name == "fixture/f.go" {
				b.sourceReads++
				return fileRead([]byte(b.source), p.Arguments.Offset, p.Arguments.Length, p.Arguments.Digest), nil
			}
			return nil, denied()
		}
		if root != "private" || !(name == "scans" || strings.HasPrefix(name, "scans/") || strings.HasPrefix(name, "cache/")) {
			return nil, denied()
		}
		switch p.Operation {
		case "fs.read":
			data, exists := b.files[name]
			if !exists {
				return failed("FS_NOT_FOUND"), nil
			}
			return fileRead(data, p.Arguments.Offset, p.Arguments.Length, p.Arguments.Digest), nil
		case "fs.write":
			data, e := base64.StdEncoding.DecodeString(p.Arguments.Data)
			if e != nil || len(data) > bodyLimit {
				return nil, denied()
			}
			if p.Arguments.Append {
				data = append(append([]byte{}, b.files[name]...), data...)
			}
			b.files[name] = data
			return ok(object{"size": len(data)}), nil
		case "fs.delete":
			_, exists := b.files[name]
			delete(b.files, name)
			return ok(object{"deleted": exists}), nil
		case "fs.list":
			keys := []string{}
			for key := range b.files {
				if path.Dir(key) == name {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			entries := []any{}
			for _, key := range keys {
				entries = append(entries, object{"name": path.Base(key), "size": len(b.files[key]), "dir": false})
			}
			return ok(object{"entries": entries, "truncated": false}), nil
		}
	}
	return nil, denied()
}
func fileRead(data []byte, off, n int, digest bool) json.RawMessage {
	if off < 0 || off > len(data) || n < 0 {
		return failed("FS_INVALID_RANGE")
	}
	if n == 0 || n > 64<<10 {
		n = 64 << 10
	}
	end := min(len(data), off+n)
	out := object{"data_b64": base64.StdEncoding.EncodeToString(data[off:end]), "size": len(data), "eof": end == len(data)}
	if digest {
		out["sha256"] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	return ok(out)
}

type safeBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *safeBuffer) text() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }

type worker struct {
	t                        *testing.T
	cmd                      *exec.Cmd
	in                       io.WriteCloser
	out                      io.ReadCloser
	stderr                   safeBuffer
	b                        *broker
	seq, maxFrame, maxResult int
	closed                   bool
}

func start(t *testing.T, b *broker, hash string) *worker {
	t.Helper()
	return startAtMemory(t, b, hash, memoryLimit, "bin/candidate.wasm")
}

func startAtMemory(t *testing.T, b *broker, hash string, ceiling int, artifact string) *worker {
	t.Helper()
	bin, e := filepath.Abs("bin/aii-plugin-worker")
	if e != nil {
		t.Fatal(e)
	}
	wasm, e := filepath.Abs(artifact)
	if e != nil {
		t.Fatal(e)
	}
	w := &worker{t: t, b: b}
	w.cmd = exec.Command(bin, "-forward", "-memory-max", fmt.Sprint(ceiling), "-module-sha256", "sha256:"+hash, wasm)
	w.cmd.Env = []string{}
	w.in, e = w.cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	w.out, e = w.cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	errpipe, e := w.cmd.StderrPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = w.cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if !w.closed {
			w.close()
		}
	})
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(errpipe)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			w.stderr.Write([]byte(line + "\n"))
			if strings.Contains(line, "event=ready") && !sent {
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
			t.Fatalf("no ready: %s", w.stderr.text())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("ready timeout: %s", w.stderr.text())
	}
	t.Log(strings.TrimSpace(w.stderr.text()))
	return w
}
func (w *worker) close() {
	w.t.Helper()
	w.closed = true
	_ = w.in.Close()
	done := make(chan error, 1)
	go func() { done <- w.cmd.Wait() }()
	select {
	case e := <-done:
		if e != nil {
			w.t.Errorf("worker close: %v %s", e, w.stderr.text())
		}
	case <-time.After(5 * time.Second):
		_ = w.cmd.Process.Kill()
		<-done
		w.t.Error("worker close timeout")
	}
}
func (w *worker) read() []byte {
	w.t.Helper()
	type result struct {
		p []byte
		e error
	}
	done := make(chan result, 1)
	go func() { p, e := sdk.ReadFrame(w.out, sdk.MaxControlFrameBytes); done <- result{p, e} }()
	select {
	case r := <-done:
		if r.e != nil {
			w.t.Fatalf("read: %v %s", r.e, w.stderr.text())
		}
		w.maxFrame = max(w.maxFrame, len(r.p))
		return r.p
	case <-time.After(35 * time.Second):
		_ = w.cmd.Process.Kill()
		w.t.Fatalf("invoke timeout %s", w.stderr.text())
		return nil
	}
}
func (w *worker) write(p []byte) {
	w.t.Helper()
	w.maxFrame = max(w.maxFrame, len(p))
	if e := sdk.WriteFrame(w.in, p, sdk.MaxControlFrameBytes); e != nil {
		w.t.Fatal(e)
	}
}
func (w *worker) invoke(op string, args any) json.RawMessage {
	w.t.Helper()
	r := w.invokeEnvelope(op, args)
	if r["status"] != "succeeded" {
		w.t.Fatalf("%s failed: %s", op, raw(r))
	}
	return raw(r["operation_result"])
}

func (w *worker) invokeEnvelope(op string, args any) object {
	w.t.Helper()
	a := objectOf(w.t, raw(args))
	a["_host_now_ms"] = w.b.nowMS
	args = a
	w.seq++
	id := raw(w.seq)
	w.write(raw(object{"jsonrpc": "2.0", "id": w.seq, "method": "invoke.call", "params": object{"operation": op, "arguments": args}}))
	for {
		p := w.read()
		var m map[string]json.RawMessage
		if e := json.Unmarshal(p, &m); e != nil {
			w.t.Fatal(e)
		}
		if string(m["jsonrpc"]) != `"2.0"` {
			w.t.Fatalf("bad jsonrpc %s", p)
		}
		if meth, exists := m["method"]; exists {
			var method string
			if e := json.Unmarshal(meth, &method); e != nil {
				w.t.Fatal(e)
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
		if !bytes.Equal(m["id"], id) || m["error"] != nil || m["result"] == nil {
			w.t.Fatalf("invalid reply %s", p)
		}
		r := objectOf(w.t, m["result"])
		result := raw(r["operation_result"])
		if op == "report" && len(result) > 262144 {
			w.t.Fatal("report exceeds declared 262144-byte result limit")
		}
		w.maxResult = max(w.maxResult, len(result))
		return r
	}
}
func objectOf(t *testing.T, p []byte) object {
	t.Helper()
	var o object
	if e := json.Unmarshal(p, &o); e != nil {
		t.Fatal(e)
	}
	return o
}
func report(t *testing.T, w *worker, shape string) object {
	return objectOf(t, w.invoke("report", object{"scan_id": "guest_fixture", "shape": shape}))["review"].(map[string]any)
}
func finding(t *testing.T, review object) {
	t.Helper()
	rows := review["entries"].([]any)
	if len(rows) != 1 {
		t.Fatalf("want one file: %s", raw(review))
	}
	row := rows[0].(map[string]any)
	fs := row["findings"].([]any)
	if row["ref"] != "f.go" || len(fs) != 1 {
		t.Fatalf("duplicated/missing finding %s", raw(row))
	}
	f := fs[0].(map[string]any)
	if f["id"] != "EH-01" || f["p"] != 0.988 || f["model"] != model {
		t.Fatalf("finding evidence changed %s", raw(f))
	}
}
func TestGuestHostContracts(t *testing.T) {
	data, e := os.ReadFile("bin/candidate.wasm")
	if e != nil {
		t.Fatal("run bash run.sh first: ", e)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	t.Logf("UNSIGNED mutable-candidate snapshot sha256=%s bytes=%d", hash, len(data))
	b := newBroker()
	w := start(t, b, hash)
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "guest_fixture"})
	partial := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": "guest_fixture"}))
	t.Logf("partial scan=%s", raw(partial))
	if partial["status"] != "judge_unavailable" || b.codeCalls != 1 || b.docsCalls != 1 {
		t.Fatalf("failure not exercised: %s code=%d docs=%d", raw(partial), b.codeCalls, b.docsCalls)
	}
	q := report(t, w, "review_queue")
	finding(t, q)
	gap := report(t, w, "coverage")
	if gap["total_entries"].(float64) < 1 || gap["assessment_complete"] != false {
		t.Fatalf("missing partial coverage %s", raw(gap))
	}
	if !bytes.Contains(raw(gap), []byte("missing_answers")) {
		t.Fatalf("failed docs not visible in coverage: %s", raw(gap))
	}
	assertJournal(t, b.files["scans/guest_fixture.v2.jsonl"], false)
	firstMaxFrame, firstMaxResult := w.maxFrame, w.maxResult
	w.close()
	removed := 0
	for key := range b.files {
		if strings.HasPrefix(key, "cache/") {
			delete(b.files, key)
			removed++
		}
	}
	if removed == 0 {
		t.Fatal("no cache existed to remove")
	}
	b.failDocs = false
	w = start(t, b, hash)
	finding(t, report(t, w, "review_queue"))
	waiting := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": "guest_fixture"}))
	due, valid := waiting["retry_not_before_ms"].(float64)
	if !valid || int64(due) < b.nowMS+120000 || b.codeCalls != 1 || b.docsCalls != 1 {
		t.Fatalf("persisted Retry-After ignored: %s", raw(waiting))
	}
	b.nowMS = int64(due) - 1
	w.invoke("scan", object{"action": "step", "scan_id": "guest_fixture"})
	if b.codeCalls != 1 || b.docsCalls != 1 {
		t.Fatal("retry sent before deadline")
	}
	b.nowMS++
	t.Logf("persisted Retry-After blocked through deadline-1; advanced synthetic clock to %d", b.nowMS)
	done := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": "guest_fixture"}))
	t.Logf("resumed scan=%s", raw(done))
	if done["status"] != "done" || done["files_judged"] != float64(1) || done["chunks"] != float64(1) || b.codeCalls != 1 || b.docsCalls != 2 {
		t.Fatalf("retry re-asked code or did not finish: %s code=%d docs=%d", raw(done), b.codeCalls, b.docsCalls)
	}
	q = report(t, w, "review_queue")
	finding(t, q)
	gap = report(t, w, "coverage")
	if q["inventory_complete"] != true || q["report_complete"] != true {
		t.Fatalf("completed inventory not reported %s", raw(q))
	}
	if gap["total_entries"] != float64(1) || gap["assessment_complete"] != false {
		t.Fatalf("context-limited coverage lost or duplicated: %s", raw(gap))
	}
	gapRows := gap["entries"].([]any)
	if len(gapRows) != 1 || string(raw(gapRows[0].(map[string]any)["reason_codes"])) != `["context_limited"]` {
		t.Fatalf("docs failure remained after successful retry: %s", raw(gap))
	}
	assertJournal(t, b.files["scans/guest_fixture.v2.jsonl"], true)
	t.Logf("final coverage=%s", raw(gap))
	baseline := string(raw(q))
	coverageBaseline := string(raw(gap))
	journal := append([]byte{}, b.files["scans/guest_fixture.v2.jsonl"]...)
	if len(journal) == 0 {
		t.Fatal("no durable journal")
	}
	calls := b.codeCalls + b.docsCalls + b.choiceCalls
	for i := 0; i < 100; i++ {
		if got := string(raw(report(t, w, "review_queue"))); got != baseline {
			t.Fatalf("report drift at %d", i)
		}
		if got := string(raw(report(t, w, "coverage"))); got != coverageBaseline {
			t.Fatalf("coverage drift at %d", i)
		}
	}
	w.invoke("scan", object{"action": "step", "scan_id": "guest_fixture"})
	finding(t, report(t, w, "review_queue"))
	if calls != b.codeCalls+b.docsCalls+b.choiceCalls || !bytes.Equal(journal, b.files["scans/guest_fixture.v2.jsonl"]) {
		t.Fatal("reports/done replay changed journal or invoked judge")
	}
	t.Logf("PASS restart with %d cache files removed; 200 repeated report invokes survived real %d-byte WASM cap (not memory plateau); max_body=%d max_params=%d max_frame=%d max_result=%d; methods=%s", removed, memoryLimit, b.maxBody, b.maxParams, max(firstMaxFrame, w.maxFrame), max(firstMaxResult, w.maxResult), raw(b.methods))
	w.close()
}

func assertJournal(t *testing.T, data []byte, complete bool) {
	t.Helper()
	records := map[string]object{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		event := objectOf(t, line)
		if r, exists := event["record"]; exists {
			record := r.(map[string]any)
			id, valid := record["record_id"].(string)
			if !valid || id == "" {
				t.Fatal("journal record lacks ID")
			}
			records[id] = record
		}
	}
	if len(records) != 2 {
		t.Fatalf("want two stable battery IDs, got %d", len(records))
	}
	found := false
	missing := 0
	for _, r := range records {
		answers, _ := r["answers"].(map[string]any)
		if p, exists := answers["EH-01"]; exists {
			found = true
			if p != 0.987654321 {
				t.Fatalf("raw answer lost precision: %v", p)
			}
		}
		for _, id := range r["expected"].([]any) {
			if _, exists := answers[id.(string)]; !exists {
				missing++
			}
		}
	}
	if !found || (complete && missing != 0) || (!complete && missing == 0) {
		t.Fatalf("journal coverage mismatch: found=%t missing=%d complete=%t", found, missing, complete)
	}
}

func TestRealWorkerRejectsOversizeFrame(t *testing.T) {
	bin, e := filepath.Abs("bin/aii-plugin-worker")
	if e != nil {
		t.Fatal(e)
	}
	wasm, e := filepath.Abs("bin/candidate.wasm")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(bin, "-memory-max", fmt.Sprint(memoryLimit), wasm)
	cmd.Env = []string{}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], sdk.MaxControlFrameBytes+1)
	cmd.Stdin = bytes.NewReader(header[:])
	var output safeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("oversize test timed out")
	}
	ee, valid := e.(*exec.ExitError)
	if !valid || ee.ExitCode() != 4 || !strings.Contains(output.text(), "stage=stream") {
		t.Fatalf("wrong refusal: %v %s", e, output.text())
	}
	t.Logf("real worker rejected 1048577-byte declared frame, exit=4: %s", strings.TrimSpace(output.text()))
}

func TestSyntheticBoundaryNegativeControls(t *testing.T) {
	b := newBroker()
	for _, p := range []object{{"operation": "http.get", "target": object{"url": endpoint}}, {"operation": "http.post", "target": object{"url": "https://example.com"}}, {"operation": "fs.read", "target": object{"root": "sandbox", "path": "/etc/passwd"}}, {"operation": "fs.read", "target": object{"root": "private", "path": "../config.json"}}, {"operation": "fs.write", "target": object{"root": "sandbox", "path": "fixture/f.go"}}, {"operation": "shell"}} {
		_, er := b.dispatch("invoke.call", raw(p))
		if er == nil {
			t.Fatalf("not denied %s", raw(p))
		}
	}
	if _, er := b.dispatch("unknown", raw(object{})); er == nil {
		t.Fatal("unknown method admitted")
	}
	p := object{"operation": "http.post", "target": object{"url": endpoint}, "arguments": object{"content_type": "application/json", "body": strings.Repeat("x", bodyLimit+1)}}
	res, er := b.dispatch("invoke.call", raw(p))
	if er != nil || !bytes.Contains(res, []byte("NET_REQUEST_TOO_BIG")) {
		t.Fatal("body cap missing")
	}
	var buf bytes.Buffer
	if e := sdk.WriteFrame(&buf, bytes.Repeat([]byte("x"), sdk.MaxControlFrameBytes+1), sdk.MaxControlFrameBytes); e != sdk.ErrFrameTooLarge || buf.Len() != 0 {
		t.Fatal("SDK frame write cap missing")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], sdk.MaxControlFrameBytes+1)
	if _, e := sdk.ReadFrame(bytes.NewReader(header[:]), sdk.MaxControlFrameBytes); e != sdk.ErrFrameTooLarge {
		t.Fatal("SDK read cap missing")
	}
}
