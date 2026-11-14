package guest_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestR31EveryFileToOneMiBAt64MiB(t *testing.T) {
	hash := candidateHash(t)
	if memoryLimit != 64<<20 {
		t.Fatalf("the harness's limit is %d, not the host's 64 MiB", memoryLimit)
	}
	for name, src := range map[string]string{"dense 576 KB": bigGo(3 * segmentBytes), "dense 1 MiB": bigGo(1<<20 - 300)} {
		t.Run(name, func(t *testing.T) {
			b := newBroker()
			b.failDocs = false
			b.sources, b.readAttempts, b.submitted = map[string]string{"big.go": src}, map[string]int{}, map[string]int{}
			w := start(t, b, hash)
			defer w.close()
			done := scanToDone(t, w, b, "r31", nil)
			if done["files_judged"] != 1.0 {
				t.Fatalf("%d bytes: judged %v", len(src), done["files_judged"])
			}
			t.Logf("%s: %d bytes judged at 64 MiB in %v windows", name, len(src), done["chunks"])
		})
	}
	t.Run("edited between steps", func(t *testing.T) {
		real, _ := realSources(t)
		src := realLarge(t, real, 2*segmentBytes+segmentBytes/2)
		marker := regexp.MustCompile(`(?m)^// [A-Za-z].*$`).FindString(src)
		if marker == "" || strings.Index(src, marker) > segmentBytes/2 {
			t.Fatalf("no comment early in the file to edit: %q", marker)
		}
		b := newBroker()
		b.failDocs = false
		b.sources, b.readAttempts, b.submitted = map[string]string{"big.go": src}, map[string]int{}, map[string]int{}
		w := start(t, b, hash)
		defer w.close()
		const id = "r31_edited"
		final, edited := src, false
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
				}
			}
			if !edited && state.File != nil && state.File.Next >= 1 {
				edited, final = true, strings.Replace(src, marker, marker+" (edited)", 1)
				b.sources["big.go"] = final
			}
		})
		cov, err := reportOf(t, w, object{"scan_id": id, "shape": "coverage", "limit": 100})
		if err != nil {
			t.Fatal(err)
		}
		want, current := fmt.Sprintf("%x", sha256.Sum256([]byte(final))), 0
		for _, e := range cov["review"].(map[string]any)["entries"].([]any) {
			if m := e.(map[string]any); !strings.Contains(fmt.Sprint(m["reason_codes"]), "source_changed") {
				current++
				if m["source_digest"] != want {
					t.Fatalf("finished under %v; the file reads %s", m["source_digest"], want)
				}
			}
		}
		if !edited || current != 1 {
			t.Fatalf("edited %v, %d current coverages", edited, current)
		}
		t.Logf("edited between steps: %d bytes, judged at 64 MiB under its new digest %.8s", len(final), want)
	})
}
