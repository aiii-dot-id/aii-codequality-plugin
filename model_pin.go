package main

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/aiii-dot-id/aii-codequality-plugin/cq"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

type ModelPin struct {
	Backend   string `json:"backend"`
	Endpoint  string `json:"endpoint"`
	Requested string `json:"requested"`
	Model     string `json:"model,omitempty"`
}

type ModelPinError struct{ Reason string }

func (e *ModelPinError) Error() string { return "model pin: " + e.Reason }
func pinError(reason string) error     { return &ModelPinError{Reason: reason} }

func NewModelPin(backend, endpoint, requested, concrete string) (ModelPin, error) {
	p := ModelPin{Backend: backend, Endpoint: endpoint, Requested: requested, Model: concrete}
	if backend == backendJev && concrete == "" && versionedJev(requested) {
		p.Model = requested
	}
	if err := p.Validate(); err != nil {
		return ModelPin{}, err
	}
	return p, nil
}

func (p ModelPin) Validate() error {
	if p.Backend != backendJev && p.Backend != backendLocal {
		return pinError("backend_invalid")
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || len(p.Endpoint) > 4096 || strings.TrimSpace(p.Endpoint) != p.Endpoint {
		return pinError("endpoint_invalid")
	}
	if p.Backend == backendJev && p.Endpoint != jevBase+systemOne {
		return pinError("endpoint_mismatch")
	}
	if !validModelName(p.Requested) {
		return pinError("requested_model_invalid")
	}
	if p.Backend == backendLocal {
		if !validModelName(p.Model) || p.Model != p.Requested || movingJevAlias(p.Model) {
			return pinError("concrete_model_required")
		}
		return nil
	}
	if !versionedJev(p.Requested) && !movingJevAlias(p.Requested) {
		return pinError("requested_model_invalid")
	}
	if p.Model == "" && movingJevAlias(p.Requested) {
		return nil
	}
	if !versionedJev(p.Model) {
		return pinError("concrete_model_required")
	}
	if versionedJev(p.Requested) && p.Model != p.Requested {
		return pinError("model_mismatch")
	}
	return nil
}

func (p ModelPin) CheckTarget(backend, endpoint, requested string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if backend != p.Backend {
		return pinError("backend_mismatch")
	}
	if endpoint != p.Endpoint {
		return pinError("endpoint_mismatch")
	}
	if requested != p.Requested {
		return pinError("requested_model_mismatch")
	}
	return nil
}

func (p ModelPin) CacheModel() (string, bool) {
	return p.Model, p.Validate() == nil && p.Model != ""
}

func (p ModelPin) RequestModel() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if p.Model != "" {
		return p.Model, nil
	}
	return p.Requested, nil
}

func (p ModelPin) WithResponse(body []byte) (ModelPin, error) {
	if err := p.Validate(); err != nil {
		return p, err
	}
	if err := sdk.ValidateStrict(body); err != nil {
		return p, pinError("response_identity_invalid")
	}
	actual := cq.ReplyModel(body)
	if !validModelName(actual) {
		return p, pinError("response_identity_missing")
	}
	if p.Model != "" && actual != p.Model {
		return p, pinError("model_mismatch")
	}
	if p.Backend == backendJev && !versionedJev(actual) {
		return p, pinError("concrete_model_required")
	}
	p.Model = actual
	return p, nil
}

func movingJevAlias(s string) bool { return s == "jev-latest" || s == "jev-preview" }
func versionedJev(s string) bool {
	if !strings.HasPrefix(s, "jev-") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(s, "jev-"), ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return validModelName(s)
}
func validModelName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			return false
		}
	}
	return true
}
