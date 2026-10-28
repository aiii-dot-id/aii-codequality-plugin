package guest_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func regressionSource(name string) string {
	return fmt.Sprintf("package p\n\n// %s returns synthetic text.\nfunc f() string {\n\treturn \"work-item-long-enough-for-code-assessment and more padding\"\n}\n", fixtureMarker(name))
}

func TestGuestRegressionsOf0114(t *testing.T) {
	hash := candidateHash(t)
	b := newBroker()
	b.failDocs = false
	b.sources, b.readAttempts, b.submitted = map[string]string{}, map[string]int{}, map[string]int{}
	for name, fixture := range map[string]string{".gitattributes": "aii-os.gitattributes", ".gitignore": "aii-os.gitignore"} {
		data, err := os.ReadFile("../../cq/testdata/rules/" + fixture)
		if err != nil {
			t.Fatal(err)
		}
		b.sources[name] = string(data)
	}
	for _, name := range []string{"a.go", "b.go"} {
		b.sources[name] = regressionSource(name)
	}
	b.refuseMarker = fixtureMarker("b.go")
	w := start(t, b, hash)
	defer w.close()
	r := w.invokeEnvelope("scan", object{"action": "start", "path": "fixture", "scan_id": "rules"})
	if r["status"] != "succeeded" {
		t.Fatalf("scan start refused aii-os's root rules: %s", raw(r))
	}
	var out map[string]any
	for step := 0; step < 10; step++ {
		r = w.invokeEnvelope("scan", object{"action": "step", "scan_id": "rules"})
		if r["status"] != "succeeded" {
			t.Fatalf("step %d failed: %s", step, raw(r))
		}
		out = r["operation_result"].(map[string]any)
		if out["status"] != "running" {
			break
		}
	}
	refused, _ := out["refused"].([]any)
	if out["status"] != "done" || len(refused) != 1 || refused[0] != "b.go" || b.submitted["a.go"] == 0 {
		t.Fatalf("refused text ended or misrecorded the scan: %s submitted=%v", raw(out), b.submitted)
	}

	r = w.invokeEnvelope("report", object{"scan_id": "rules", "shape": "summary"})
	if sum, _ := r["operation_result"].(map[string]any); sum == nil || sum["refused_by_judge"] != float64(1) || !strings.Contains(fmt.Sprint(sum["refused_note"]), "1 were judged") {
		t.Fatalf("summary does not state the refusals: %.400s", raw(r))
	}

	b.files["scans/orphan.v2.jsonl"] = []byte("{\"torn\n")
	if r = w.invokeEnvelope("scan", object{"action": "start", "path": "fixture", "scan_id": "orphan"}); r["status"] != "succeeded" {
		t.Fatalf("orphaned journal blocked the name: %s", raw(r))
	}
	for slot := 0; slot < 2; slot++ {
		b.files[fmt.Sprintf("scans/orphan.checkpoint.%d", slot)] = []byte("not a checkpoint")
	}
	r = w.invokeEnvelope("report", object{})
	listed := map[string]map[string]any{}
	for _, s := range r["operation_result"].(map[string]any)["scans"].([]any) {
		v := s.(map[string]any)
		listed[v["scan_id"].(string)] = v
	}
	if v := listed["orphan"]; v == nil || v["status"] != "unreadable" || !strings.Contains(fmt.Sprint(v["route"]), "keep_scans") || listed["rules"]["status"] != "done" {
		t.Fatalf("unreadable scan not listed with its route: %s", raw(r))
	}
}

func growJournal(t *testing.T, b *broker, id string, target int) int {
	t.Helper()
	name := "scans/" + id + ".v2.jsonl"
	seed := strings.Split(strings.TrimSuffix(string(b.files[name]), "\n"), "\n")
	grown := append([]byte(nil), b.files[name]...)
	for i := 0; len(grown) < target; i++ {
		event := objectOf(t, []byte(seed[i%len(seed)]))
		ref := fmt.Sprintf("pkg%03d/file%06d.go", i%300, i/len(seed))
		if r, ok := event["record"].(map[string]any); ok {
			r["record_id"], r["ref"] = fmt.Sprintf("%064x", i), ref
		} else if c, ok := event["coverage"].(map[string]any); ok {
			c["ref"] = ref
		}
		grown = append(append(grown, raw(event)...), '\n')
	}
	b.files[name] = grown
	for slot := 0; slot < 2; slot++ {
		cp := fmt.Sprintf("scans/%s.checkpoint.%d", id, slot)
		if len(b.files[cp]) == 0 {
			continue
		}
		envelope := objectOf(t, b.files[cp])
		state := envelope["state"].(map[string]any)
		state["progress_offset"] = len(grown)
		envelope["digest"] = fmt.Sprintf("%x", sha256.Sum256(raw(state)))
		b.files[cp] = raw(envelope)
	}
	return len(grown)
}

func TestGuestReportAnswersAboveFiveMiB(t *testing.T) {
	sizes := []int{8 << 20}
	if s := os.Getenv("GUEST_JOURNAL_MIB"); s != "" {
		sizes = nil
		for _, f := range strings.Split(s, ",") {
			var n int
			fmt.Sscan(f, &n)
			sizes = append(sizes, n<<20)
		}
	}
	hash := candidateHash(t)
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dMiB", size>>20), func(t *testing.T) {
			b := newBroker()
			b.failDocs = false
			w := start(t, b, hash)
			defer w.close()
			if r := w.invokeEnvelope("scan", object{"action": "start", "path": "fixture", "scan_id": "big"}); r["status"] != "succeeded" {
				t.Fatal(raw(r))
			}
			if r := w.invokeEnvelope("scan", object{"action": "step", "scan_id": "big"}); r["status"] != "succeeded" {
				t.Fatal(raw(r))
			}
			n := growJournal(t, b, "big", size)
			for _, shape := range []string{"review_queue", "coverage", "summary", "review_queue", "review_queue"} {
				began := time.Now()
				r, _ := releaseInvoke(t, w, "report", object{"scan_id": "big", "shape": shape}, object{})
				out := releaseSuccess(t, r)
				incomplete := out["complete"] == false
				if review, ok := out["review"].(map[string]any); ok {
					incomplete = review["report_complete"] == false
				}
				if !incomplete {
					t.Fatalf("%s on a %d-byte journal claims a complete report: %.300s", shape, n, raw(out))
				}
				if note, _ := out["note"].(string); !strings.Contains(note, "narrower scan") {
					t.Fatalf("%s ranks a prefix without the note and its route: %.300s", shape, raw(out))
				}
				t.Logf("%s on a %d-byte journal: %v", shape, n, time.Since(began).Round(time.Millisecond))
			}
			began := time.Now()
			if r := w.invokeEnvelope("scan", object{"action": "step", "scan_id": "big"}); r["status"] != "succeeded" {
				t.Fatalf("step: %s", raw(r))
			}
			t.Logf("step on a %d-byte journal: %v", n, time.Since(began).Round(time.Millisecond))
		})
	}
}
