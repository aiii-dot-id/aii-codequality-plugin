//go:build experimental

package guest_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func largeSource() string {
	prefix := "package p\n// f returns synthetic text.\nfunc f() string { return `"
	suffix := "` }\n"
	n := (1 << 20) - len(prefix) - len(suffix)
	var middle strings.Builder
	for i := 0; ; i++ {
		unit := fmt.Sprintf("<>&\"\\\t界 synthetic-window-%08d\n", i)
		if middle.Len()+len(unit) > n {
			break
		}
		middle.WriteString(unit)
	}
	middle.WriteString(strings.Repeat("x", n-middle.Len()))
	return prefix + middle.String() + suffix
}

func TestGuestOneMiBRawSource(t *testing.T) {
	runOneMiB(t, 3)
}

func TestGuestOneMiBRestartEveryStep(t *testing.T) {
	runOneMiB(t, 1)
}

func runOneMiB(t *testing.T, restartEvery int) {
	hash := candidateHash(t)
	b := newBroker()
	b.failDocs = false
	b.source = largeSource()
	if len(b.source) != 1<<20 {
		t.Fatal("fixture is not exactly 1 MiB")
	}
	t.Logf("raw fixture bytes=%d sha256=%x; enforced guest linear-memory ceiling=%d; no heap/RSS plateau measurement", len(b.source), sha256.Sum256([]byte(b.source)), memoryLimit)
	w := start(t, b, hash)
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "guest_fixture"})
	steps, restarts, maxFrame, maxResult := 0, 0, 0, 0
	t.Cleanup(func() {
		t.Logf("1MiB observed even on failure: attempted_steps=%d restarts=%d source_reads=%d max_body=%d encoded_string_overhead=%d max_params=%d max_frame=%d max_result=%d ceiling=%d", steps, restarts, b.sourceReads, b.maxBody, b.maxEscaping, b.maxParams, max(maxFrame, w.maxFrame), max(maxResult, w.maxResult), memoryLimit)
	})
	done := false
	for steps < 160 {
		steps++
		r := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": "guest_fixture"}))
		report(t, w, "review_queue")
		report(t, w, "coverage")
		t.Logf("1MiB step=%d status=%v chunks=%v calls=%v source_reads=%d", steps, r["status"], r["chunks"], r["calls"], b.sourceReads)
		if r["status"] == "done" {
			if r["files_judged"] != float64(1) || r["chunks"].(float64) < 2 || b.sourceReads < 16 {
				t.Fatalf("large file not really assessed: %s", raw(r))
			}
			done = true
			break
		}
		if r["status"] != "running" && r["status"] != "judge_unavailable" {
			t.Fatalf("large file stopped: %s", raw(r))
		}
		if steps%restartEvery == 0 {
			maxFrame = max(maxFrame, w.maxFrame)
			maxResult = max(maxResult, w.maxResult)
			w.close()
			w = start(t, b, hash)
			restarts++
		}
	}
	if !done || steps < 2 {
		t.Fatalf("large scan did not finish with multiple steps: steps=%d done=%t", steps, done)
	}
	baseline := string(raw(report(t, w, "review_queue")))
	coverage := string(raw(report(t, w, "coverage")))
	for i := 0; i < 20; i++ {
		if string(raw(report(t, w, "review_queue"))) != baseline || string(raw(report(t, w, "coverage"))) != coverage {
			t.Fatal("large report drift")
		}
	}
	if b.maxEscaping < 1000 || b.maxBody > bodyLimit {
		t.Fatalf("escaping/request limit not exercised: escaping=%d body=%d", b.maxEscaping, b.maxBody)
	}
	t.Logf("PASS 1MiB workflow: steps=%d restarts=%d final_repeated_reports=40 max_body=%d max_encoded_string_overhead=%d max_params=%d max_frame=%d max_result=%d; ceiling=%d (not plateau)", steps, restarts, b.maxBody, b.maxEscaping, b.maxParams, max(maxFrame, w.maxFrame), max(maxResult, w.maxResult), memoryLimit)
	w.close()
}
