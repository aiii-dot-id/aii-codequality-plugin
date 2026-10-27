package guest_test

import (
	"strings"
	"testing"
)

func isCallBudgetPause(r object) bool {
	reason, _ := r["last_error"].(string)
	return r["status"] == "judge_unavailable" && strings.HasSuffix(reason, ": judge unavailable: the calls this step planned are spent; the next step continues from the answers kept — the next step retries this file")
}

func TestCallBudgetPauseOracle(t *testing.T) {
	const reason = "exact.go: judge unavailable: the calls this step planned are spent; the next step continues from the answers kept — the next step retries this file"
	if !isCallBudgetPause(object{"status": "judge_unavailable", "last_error": reason}) {
		t.Fatal("budget continuation rejected")
	}
	for _, r := range []object{
		{"status": "running", "last_error": reason},
		{"status": "judge_unavailable", "last_error": "HTTP 503"},
		{"status": "judge_unavailable", "last_error": reason + "; unexpected error"},
		{"status": "judge_unavailable"},
	} {
		if isCallBudgetPause(r) {
			t.Fatalf("real/unknown failure admitted as continuation: %s", raw(r))
		}
	}
}
