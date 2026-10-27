package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
)

func TestModelPinAndAliasMove(t *testing.T) {
	memo := cq.LoadMemo(nil)
	memo.Learn(backendJev, jevDefault, "jev-1.13.0")
	if memo.Resolve(backendJev, jevDefault) != "jev-1.13.0" {
		t.Fatal("fixture did not establish old memo")
	}
	pin, err := NewModelPin(backendJev, jevBase+systemOne, jevDefault, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pin.CacheModel(); ok {
		t.Fatal("fresh alias allowed cached identity")
	}
	if got, err := pin.RequestModel(); err != nil || got != jevDefault {
		t.Fatalf("request = %q, %v", got, err)
	}
	pin, err = pin.WithResponse([]byte(`{"model":"jev-1.14.0","answers":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	var restored ModelPin
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.CheckTarget(backendJev, jevBase+systemOne, jevDefault); err != nil {
		t.Fatal(err)
	}
	if got, ok := restored.CacheModel(); !ok || got != "jev-1.14.0" {
		t.Fatalf("cache = %q, %v", got, ok)
	}
	if got, err := restored.RequestModel(); err != nil || got != "jev-1.14.0" {
		t.Fatalf("request = %q, %v", got, err)
	}
	for _, tc := range []struct{ body, reason string }{
		{`{"model":"jev-1.15.0"}`, "model_mismatch"},
		{`{"answers":{}}`, "response_identity_missing"},
		{`{"model":"jev-latest"}`, "model_mismatch"},
		{`{"model":"jev-1.14.0","model":"jev-1.15.0"}`, "response_identity_invalid"},
	} {
		next, err := restored.WithResponse([]byte(tc.body))
		requirePinReason(t, err, tc.reason)
		if next != restored {
			t.Fatal("rejected response changed pin")
		}
	}
	explicit, err := NewModelPin(backendJev, jevBase+systemOne, "jev-1.14.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := explicit.CacheModel(); !ok || got != restored.Model {
		t.Fatal("explicit pin lost cache eligibility")
	}
}

func TestModelPinStrictIdentity(t *testing.T) {
	for _, requested := range []string{"", "jev-1.14", "jev-01.14.0", "jev-1.14.0-beta", "jev-1.14.0\n", strings.Repeat("x", 129)} {
		if _, err := NewModelPin(backendJev, jevBase+systemOne, requested, ""); err == nil {
			t.Fatalf("accepted %q", requested)
		}
	}
	fresh, err := NewModelPin(backendJev, jevBase+systemOne, jevDefault, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"model":"jev-latest"}`, `{"model":"jev-preview"}`, `{"model":"unknown"}`, `{"model":"jev-1.14.0\n"}`, `{}`} {
		if _, err := fresh.WithResponse([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}

	if _, err := fresh.WithResponse([]byte(`{"models":[{"name":"jev-latest","description":"jev-1.14.0"}]}`)); err == nil {
		t.Fatal("model listing resolved alias")
	}
	if _, err := NewModelPin(backendJev, jevBase+systemOne, "jev-1.14.0", "jev-1.13.0"); err == nil {
		t.Fatal("explicit mismatch accepted")
	}
}

func TestModelPinTargetAndCredentials(t *testing.T) {
	endpoint := "http://localhost:9000/first/v1/systemone"
	p, err := NewModelPin(backendLocal, endpoint, "local-build-42", "local-build-42")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.WithResponse([]byte(`{"model":"local-build-42"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewModelPin(backendLocal, endpoint, "local-alias", ""); err == nil {
		t.Fatal("local alias accepted without explicit identity")
	}
	if _, err := NewModelPin(backendLocal, endpoint, "local-alias", "local-build-42"); err == nil {
		t.Fatal("local alias was silently resolved")
	}
	for _, raw := range []string{"http://user:secret@localhost/v1/systemone", endpoint + "?key=secret", endpoint + "#secret", "file:///tmp/model", "http:///v1/systemone"} {
		_, err := NewModelPin(backendLocal, raw, "local-build-42", "local-build-42")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe endpoint/error: %q %v", raw, err)
		}
	}
	requirePinReason(t, p.CheckTarget(backendJev, endpoint, p.Requested), "backend_mismatch")
	requirePinReason(t, p.CheckTarget(backendLocal, strings.Replace(endpoint, "/first/", "/second/", 1), p.Requested), "endpoint_mismatch")
	requirePinReason(t, p.CheckTarget(backendLocal, endpoint, "local-build-43"), "requested_model_mismatch")
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "auth") || strings.Contains(string(raw), "handle") {
		t.Fatalf("credential fields in %s", raw)
	}
	corrupt := p
	corrupt.Model = ""
	if _, ok := corrupt.CacheModel(); ok {
		t.Fatal("invalid persisted pin allowed cache")
	}
}

func requirePinReason(t *testing.T, err error, want string) {
	t.Helper()
	var pe *ModelPinError
	if !errors.As(err, &pe) || pe.Reason != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
