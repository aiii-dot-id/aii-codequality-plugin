package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
)

func TestR4SARequestStateGrowsNoMoreThanTheRoomKeptForIt(t *testing.T) {
	bound := scanRequestState{Pin: ModelPin{Backend: backendJev, Endpoint: jevBase + systemOne, Requested: "jev"}}
	grown := bound
	grown.Pin.Model = strings.Repeat("&", 128)
	if !validModelName(grown.Pin.Model) || validModelName(grown.Pin.Model+"&") {
		t.Fatal("precondition: 128 bytes is no longer the longest model name a pin accepts")
	}
	grown.RequestKey = strings.Repeat("f", 64)
	grown.Retry = cq.RequestRetry{Attempts: math.MaxUint32, NotBeforeMS: math.MaxInt64}
	grown.Blocked = cq.Clip(strings.Repeat("&", 4096), blockedMost)
	a, err := json.Marshal(bound)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(grown)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("request state %d bytes when bound, %d at its largest: growth %d of %d", len(a), len(b), len(b)-len(a), cq.RuntimeGrowth)
	if len(b)-len(a) > cq.RuntimeGrowth {
		t.Fatalf("the request state can grow by %d bytes, past the %d the walk keeps for it", len(b)-len(a), cq.RuntimeGrowth)
	}
}
