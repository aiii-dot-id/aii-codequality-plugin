// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

// A step ends inside the host's 30 s invoke wall whatever Jev does, because a guest has no clock
// to watch: every call may take its whole timeout, and the calls a step makes, each at its
// timeout, leave six seconds for reading files and saving state.
func TestAStepFitsTheInvokeWall(t *testing.T) {
	for calls := 1; calls <= maxCalls; calls++ {
		if worst := calls * callMS(calls); worst > stepMS {
			t.Fatalf("%d calls at %d ms is %d ms, past the %d ms a step may spend on Jev", calls, callMS(calls), worst, stepMS)
		}
	}
	if callMS(maxCalls) != 2000 || callMS(retryCalls) != maxCallMS || callMS(1) != maxCallMS {
		t.Fatalf("a step's calls get 2 s, a retry's and a lone text's 8 s: %d %d %d", callMS(maxCalls), callMS(retryCalls), callMS(1))
	}
}

// A judge makes only the calls it was planned for; the next is refused as the judge's failure,
// before anything is sent, so the step ends in time and the next one resumes from the cache.
func TestAJudgeStopsAtTheCallsPlanned(t *testing.T) {
	j := &hostJudge{handle: "h", model: "m", timeout: callMS(retryCalls), left: 0}
	if _, err := j.Ask(map[string]any{"code": "x"}, map[string]any{}); !errors.Is(err, errCallsSpent) || !errors.Is(err, cq.ErrJudge) {
		t.Fatalf("a call past the planned ones is refused as the judge's failure: %v", err)
	}
	if stepCalls(&cq.Scan{Status: "running"}) != maxCalls || stepCalls(&cq.Scan{Status: "judge_unavailable"}) != retryCalls {
		t.Fatal("a step plans twelve calls, and three after one that found Jev unavailable")
	}
}

// Every error an operation returns leaves named: the kit carries an operation error's words
// and drops the text of any other, so a Jev outage, a refused key, a refused text and an
// unexpected failure each reach the caller with a code and a reason, and an operation error
// already named passes as it is.
func TestEveryErrorLeavesNamed(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("%w: Jev answered 503", cq.ErrJudge), "JUDGE_UNAVAILABLE"},
		{fmt.Errorf("%w (401): paste a key", cq.ErrKey), "JUDGE_KEY_REFUSED"},
		{fmt.Errorf("%w: Jev answered 400", cq.ErrRefused), "JUDGE_REFUSED_TEXT"},
		{errors.New("state file unreadable"), "CODEQUALITY_FAILED"},
		{sdk.Fail("SCAN_EXISTS", "a scan named x exists"), "SCAN_EXISTS"},
	} {
		_, err := named(func(sdk.Call) (any, error) { return nil, c.err })(sdk.Call{})
		var op *sdk.OperationError
		if !errors.As(err, &op) || op.ReasonCode != c.code || op.Reason == "" {
			t.Fatalf("%v left as %#v, want %s with its reason", c.err, err, c.code)
		}
	}
	if out, err := named(func(sdk.Call) (any, error) { return "ok", nil })(sdk.Call{}); err != nil || out != "ok" {
		t.Fatal("a result passes untouched")
	}
}
