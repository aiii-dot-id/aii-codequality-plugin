package guest_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

func candidateHash(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("bin/candidate.wasm")
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	t.Logf("candidate sha256=%s bytes=%d", hash, len(data))
	return hash
}

func TestGuestModelPinAndEndpointCache(t *testing.T) {
	hash := candidateHash(t)
	t.Run("configuration_drift", func(t *testing.T) {
		b := newBroker()
		b.failDocs = false
		w := start(t, b, hash)
		w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "pin"})
		w.close()
		w = start(t, b, hash)
		for _, drift := range []string{"endpoint", "model", "clock"} {
			before := b.methods["http.post"]
			switch drift {
			case "endpoint":
				b.baseURL += "/other"
			case "model":
				b.configuredModel = "other-model"
			case "clock":
				b.nowMS = 0
			}
			r := w.invokeEnvelope("scan", object{"action": "step", "scan_id": "pin"})
			if r["status"] == "succeeded" || b.methods["http.post"] != before {
				t.Fatalf("drift %s was admitted: %s", drift, raw(r))
			}
			t.Logf("%s refused before HTTP: %s", drift, raw(r))
			b.baseURL = "https://fixture.invalid"
			b.configuredModel = model
			b.nowMS = 1800000000000
		}
		w.close()
	})
	t.Run("response_mismatch_persists", func(t *testing.T) {
		b := newBroker()
		b.failDocs = false
		b.responseModel = "wrong-model"
		w := start(t, b, hash)
		w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "guest_fixture"})
		r := w.invokeEnvelope("scan", object{"action": "step", "scan_id": "guest_fixture"})
		if r["status"] != "failed" || !bytes.Contains(raw(r), []byte("model_mismatch")) {
			t.Fatalf("response mismatch not blocked: %s", raw(r))
		}
		q := report(t, w, "review_queue")
		if q["total_entries"] != float64(0) {
			t.Fatalf("wrong-model findings accepted: %s", raw(q))
		}
		for name := range b.files {
			if strings.HasPrefix(name, "cache/") {
				t.Fatalf("wrong-model reply cached: %s", name)
			}
		}
		before := b.methods["http.post"]
		w.close()
		w = start(t, b, hash)
		b.responseModel = model
		r = w.invokeEnvelope("scan", object{"action": "step", "scan_id": "guest_fixture"})
		if r["status"] == "succeeded" || !bytes.Contains(raw(r), []byte("SCAN_REQUEST_BLOCKED")) || b.methods["http.post"] != before {
			t.Fatalf("block lost on restart: %s", raw(r))
		}
		t.Logf("wrong-model response rejected, no findings/cache; persisted refusal=%s", raw(r))
		w.close()
	})
	t.Run("complete_endpoint_cache_namespace", func(t *testing.T) {
		b := newBroker()
		b.failDocs = false
		w := start(t, b, hash)
		for i, base := range []string{"https://fixture.invalid/a", "https://fixture.invalid/a", "https://fixture.invalid/b", "http://fixture.invalid/b"} {
			b.baseURL = base
			id := fmt.Sprintf("namespace_%d", i)
			before := b.methods["http.post"]
			w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": id})
			r := objectOf(t, w.invoke("scan", object{"action": "step", "scan_id": id}))
			delta := b.methods["http.post"] - before
			if r["status"] != "done" || (i == 1 && delta != 0) || (i != 1 && delta == 0) {
				t.Fatalf("namespace %s calls=%d result=%s", base, delta, raw(r))
			}
			t.Logf("endpoint=%s new HTTP calls=%d cache_hits=%v", base, delta, r["cache_hits"])
			w.close()
			w = start(t, b, hash)
		}
		w.close()
	})
}
