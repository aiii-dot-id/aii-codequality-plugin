package guest_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newestState(t *testing.T, b *broker, id string) map[string]any {
	t.Helper()
	var newest checkpointEnvelope
	for _, slot := range []string{".checkpoint.0", ".checkpoint.1"} {
		var e checkpointEnvelope
		if json.Unmarshal(b.files["scans/"+id+slot], &e) == nil && e.Generation > newest.Generation {
			newest = e
		}
	}
	var state map[string]any
	if err := json.Unmarshal(newest.State, &state); err != nil {
		t.Fatalf("no state for %s: %v", id, err)
	}
	return state
}

func TestR31ALongErrorKeepsItsWalkAndCause(t *testing.T) {
	hash := candidateHash(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.go": "package p\n\nfunc A() {}\n", "sub/b.go": "package sub\n\nfunc B() {}\n",
		"sub/.gitattributes": "* linguist-" + strings.Repeat("<", 45000) + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := newBroker()
	b.failDocs, b.tree = false, dir
	w := start(t, b, hash)
	defer w.close()
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "r31_long"})
	for step := 0; step < 3; step++ {
		w.invokeEnvelope("scan", object{"action": "step", "scan_id": "r31_long"})
	}
	state := newestState(t, b, "r31_long")
	last, _ := state["last_error"].(string)
	t.Logf("status %v; last_error %d bytes: %.160s…", state["status"], len(last), last)
	if state["status"] == "storage_cap" || !strings.HasPrefix(last, "sub") || len(last) > 1100 || !strings.Contains(last, "bytes in all)") {
		t.Fatalf("the long error's scan: status %v, last_error %d bytes", state["status"], len(last))
	}
}

func TestR4AFolderTooWideForTheSavedStateIsLeftOutByName(t *testing.T) {
	hash := candidateHash(t)
	dir := t.TempDir()
	wide := filepath.Join(dir, "wide")
	for _, d := range []string{wide, filepath.Join(dir, "z")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var big strings.Builder
	big.WriteString("package wide\n\n")
	for k := 0; big.Len() < 300<<10; k++ {
		fmt.Fprintf(&big, "// F%d returns %d.\nfunc F%d() int { return %d }\n\n", k, k, k, k)
	}
	if err := os.WriteFile(filepath.Join(wide, "0big.go"), []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 1100; k++ {
		name := fmt.Sprintf("f%04d_%s.go", k, strings.Repeat("n", 220))
		if err := os.WriteFile(filepath.Join(wide, name), []byte("package wide\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"a.go": "package p\n\nfunc A() {}\n", "z/after.go": "package z\n\nfunc After() {}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := newBroker()
	b.failDocs, b.tree = false, dir
	w := start(t, b, hash)
	defer w.close()
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "r4_wide"})
	var r object
	for step := 0; step < 20; step++ {
		env := w.invokeEnvelope("scan", object{"action": "step", "scan_id": "r4_wide"})
		if env["status"] != "succeeded" {
			t.Fatalf("step %d: %s", step, raw(env))
		}
		if r = env["operation_result"].(map[string]any); r["status"] != "running" && r["status"] != "judge_unavailable" {
			break
		}
	}
	state := newestState(t, b, "r4_wide")
	t.Logf("status %v; last_error %v; file_cursor kept %v", state["status"], state["last_error"], state["file_cursor"] != nil)
	t.Logf("the last step: %s", raw(r))
	if state["status"] != "done" || r["status"] != "done" {
		t.Fatalf("the scan did not finish: state %v, result %v, last_error %v", state["status"], r["status"], state["last_error"])
	}
	tooWide, _ := r["too_wide"].(map[string]any)
	excluded, _ := r["excluded"].(map[string]any)
	why, _ := tooWide["why"].(string)
	if fmt.Sprint(tooWide["folders"]) != "[wide]" || tooWide["count"] != 1.0 || excluded["listing_too_wide"] != 1.0 || !strings.HasPrefix(why, "not assessed: ") {
		t.Errorf("the folder is not left out by name with its reason: too_wide %v, excluded %v", r["too_wide"], r["excluded"])
	}
	if r["files_judged"] != 2.0 {
		t.Errorf("files judged %v; want the 2 outside the folder", r["files_judged"])
	}
	stateErr, _ := state["last_error"].(string)
	resultErr, _ := r["last_error"].(string)
	if stateErr != "" || resultErr != "" || state["file_cursor"] != nil {
		t.Errorf("left behind: state last_error %q, result last_error %q, file cursor kept %v", stateErr, resultErr, state["file_cursor"] != nil)
	}
}

func TestR4AFolderWithMoreEntriesThanAreListedIsLeftOutByName(t *testing.T) {
	hash := candidateHash(t)
	dir := t.TempDir()
	many := filepath.Join(dir, "many")
	for _, d := range []string{many, filepath.Join(dir, "z")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for k := 0; k < 16500; k++ {
		if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("f%05d.go", k)), []byte("package many\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"a.go": "package p\n\nfunc A() {}\n", "z/after.go": "package z\n\nfunc After() {}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := newBroker()
	b.failDocs, b.tree = false, dir
	b.listPage = 1024
	w := start(t, b, hash)
	defer w.close()
	w.invoke("scan", object{"action": "start", "path": "fixture", "scan_id": "r4_cut"})
	var r object
	for step := 0; step < 6; step++ {
		env := w.invokeEnvelope("scan", object{"action": "step", "scan_id": "r4_cut"})
		if env["status"] != "succeeded" {
			t.Fatalf("step %d: %s", step, raw(env))
		}
		if r = env["operation_result"].(map[string]any); r["status"] != "running" && r["status"] != "judge_unavailable" {
			break
		}
	}
	state := newestState(t, b, "r4_cut")
	t.Logf("status %v; last_error %v", state["status"], state["last_error"])
	t.Logf("the last step: %s", raw(r))
	if state["status"] != "done" || r["status"] != "done" {
		t.Fatalf("the scan did not finish: state %v, result %v, last_error %v", state["status"], r["status"], state["last_error"])
	}
	cut, _ := r["listing_truncated"].(map[string]any)
	excluded, _ := r["excluded"].(map[string]any)
	why, _ := cut["why"].(string)
	if fmt.Sprint(cut["folders"]) != "[many]" || cut["count"] != 1.0 || excluded["dir_listing_truncated"] != 1.0 || !strings.HasPrefix(why, "not assessed: ") {
		t.Errorf("the folder is not left out by name with its reason: listing_truncated %v, excluded %v", r["listing_truncated"], r["excluded"])
	}
	if r["files_judged"] != 2.0 {
		t.Errorf("files judged %v; want the 2 outside the folder", r["files_judged"])
	}
	stateErr, _ := state["last_error"].(string)
	resultErr, _ := r["last_error"].(string)
	if stateErr != "" || resultErr != "" {
		t.Errorf("left behind: state last_error %q, result last_error %q", stateErr, resultErr)
	}
}
