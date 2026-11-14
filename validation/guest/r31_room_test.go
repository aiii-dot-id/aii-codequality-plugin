package guest_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const scanRoomBytes = 64<<20 - 16<<20 - (2*250<<10 + 1<<20) - (16<<20)/256

func TestR31AScanUsesItsWholeRoom(t *testing.T) {
	hash := candidateHash(t)
	tree := os.Getenv("AII_CQ_TREE")
	if tree == "" {
		tree = filepath.Join(runtime.GOROOT(), "src", "go")
	}
	const id = "r31_room"
	run := func(filler int) (object, *broker) {
		b := newBroker()
		b.failDocs, b.tree = false, tree
		b.extraSettings = object{"max_calls_per_step": 64, "retry_calls": 64}
		if filler > 0 {
			b.files["scans/cq_1.state.json"] = []byte("{}")
			b.files["scans/cq_1.v2.jsonl"] = make([]byte, filler)
		}
		w := start(t, b, hash)
		defer w.close()
		if env := w.invokeEnvelope("scan", object{"action": "start", "path": "fixture", "scan_id": id}); env["status"] != "succeeded" {
			t.Fatalf("start: %s", raw(env))
		}
		var r object
		for step := 0; ; step++ {
			env := w.invokeEnvelope("scan", object{"action": "step", "scan_id": id})
			if env["status"] != "succeeded" {
				t.Fatalf("step %d: %s", step, raw(env))
			}
			r = env["operation_result"].(map[string]any)
			if r["status"] == "judge_unavailable" {
				b.nowMS += 600_000
			} else if r["status"] != "running" || step > 20000 {
				break
			}
		}
		summary, err := reportOf(t, w, object{"scan_id": id, "shape": "summary"})
		if err != nil {
			t.Fatal(err)
		}
		r["summary"] = summary
		return r, b
	}
	sizes := func(b *broker) (int, int) {
		return len(b.files["scans/"+id+".v2.jsonl"]), len(b.files["scans/"+id+".intent.jsonl"])
	}

	fresh, b := run(0)
	if fresh["status"] != "done" {
		t.Fatalf("a fresh slot: %s", raw(fresh))
	}
	journal, side := sizes(b)
	t.Logf("%s in a fresh slot: %v files judged, journal %d bytes, side file %d bytes", tree, fresh["files_judged"], journal, side)

	room := (journal + side + 8*side) / 2
	if 8*side <= journal+side || room-room/8 <= journal || room < 2<<20 || room > scanRoomBytes {
		t.Fatalf("this tree cannot tell a shared room from a fixed eighth: journal %d, side %d (choose a larger tree)", journal, side)
	}
	full, b := run(scanRoomBytes - room)
	journal2, side2 := sizes(b)
	t.Logf("in a slot an earlier scan holds %d bytes of (room about %d, an eighth %d): status %v, %v files judged, journal %d, side file %d; last_error %v",
		scanRoomBytes-room, room, room/8, full["status"], full["files_judged"], journal2, side2, full["last_error"])
	t.Logf("summary: %s", raw(full["summary"]))
	if full["status"] != "done" || full["files_judged"] != fresh["files_judged"] {
		t.Fatalf("the scan did not use its whole room: %s", raw(full))
	}
}

func TestR31AScanAnEarlierVersionStoppedIsContinued(t *testing.T) {
	hash := candidateHash(t)
	b := newBroker()
	b.failDocs, b.tree = false, filepath.Join(runtime.GOROOT(), "src", "go")
	w := start(t, b, hash)
	defer w.close()
	const id = "r31_stopped"
	if env := w.invokeEnvelope("scan", object{"action": "start", "path": "fixture", "scan_id": id}); env["status"] != "succeeded" {
		t.Fatalf("start: %s", raw(env))
	}
	for step := 0; step < 2; step++ {
		if env := w.invokeEnvelope("scan", object{"action": "step", "scan_id": id}); env["status"] != "succeeded" {
			t.Fatalf("step %d: %s", step, raw(env))
		}
	}

	newest, slot := checkpointEnvelope{}, ""
	for _, name := range []string{"scans/" + id + ".checkpoint.0", "scans/" + id + ".checkpoint.1"} {
		var e checkpointEnvelope
		if json.Unmarshal(b.files[name], &e) == nil && e.Generation > newest.Generation {
			newest, slot = e, name
		}
	}
	var state map[string]any
	if err := json.Unmarshal(newest.State, &state); err != nil {
		t.Fatal(err)
	}
	stack, _ := state["stack"].([]any)
	pending, _ := state["pending"].([]any)
	if state["status"] != "running" || len(stack)+len(pending) == 0 {
		t.Fatalf("the scan is not mid-walk after two steps: %v, %d dirs and %d files to go", state["status"], len(stack), len(pending))
	}
	judged := state["files_judged"].(float64)
	state["status"], state["last_error"] = "storage_cap", "the intent answers' file reached its share of the scan's room; retained results remain reportable; start a narrower scan"
	newest.State, _ = json.Marshal(state)
	sum := sha256.Sum256(newest.State)
	newest.Generation++
	newest.Digest = hex.EncodeToString(sum[:])
	b.files[slot], _ = json.Marshal(newest)
	side := "scans/" + id + ".intent.jsonl"
	eighth := int(state["journal_cap"].(float64)) / 7
	first := b.files[side][:bytes.IndexByte(b.files[side], '\n')+1]
	for len(b.files[side]) < eighth {
		b.files[side] = append(b.files[side], first...)
	}
	stopped := w.invokeEnvelope("report", object{"scan_id": id, "shape": "summary"})
	t.Logf("as an earlier version left it: %s", raw(stopped["operation_result"].(map[string]any)["scan"]))

	var r object
	for step := 0; step < 2000; step++ {
		env := w.invokeEnvelope("scan", object{"action": "step", "scan_id": id})
		if env["status"] != "succeeded" {
			t.Fatalf("step %d after the stop: %s", step, raw(env))
		}
		r = env["operation_result"].(map[string]any)
		if r["status"] == "judge_unavailable" {
			b.nowMS += 600_000
		} else if r["status"] != "running" {
			break
		}
	}
	t.Logf("continued: status %v, %v files judged (%v when it stopped), side file %d bytes (its old eighth %d)", r["status"], r["files_judged"], judged, len(b.files[side]), eighth)
	if r["status"] != "done" || r["files_judged"].(float64) <= judged || len(b.files[side]) <= eighth {
		t.Fatalf("the stopped scan was not continued: %s", raw(r))
	}
}

type checkpointEnvelope struct {
	Generation uint64          `json:"generation"`
	Digest     string          `json:"digest"`
	State      json.RawMessage `json:"state"`
}
