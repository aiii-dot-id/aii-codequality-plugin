// Copyright 2026 AIII AI Identity Incorporated <james@aiii.id>
// SPDX-License-Identifier: Apache-2.0

package cq

import (
	"encoding/json"
	"fmt"
)

// MaxModelChoices bounds the choices offered for the model setting: the
// host's bound on a setting's choices.
const MaxModelChoices = 256

// ModelChoices turns Jev's GET /v1/models answer, {"models": [{"name",
// "description"}]}, into the choices the host offers for the model setting:
// every named model once, in Jev's order.
func ModelChoices(body []byte) ([]map[string]any, error) {
	var r struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("Jev's model list is not JSON: %v", err)
	}
	seen := map[string]bool{}
	out := []map[string]any{}
	for _, m := range r.Models {
		if m.Name == "" || seen[m.Name] || len(out) == MaxModelChoices {
			continue
		}
		seen[m.Name] = true
		out = append(out, map[string]any{"value": m.Name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Jev listed no models")
	}
	return out, nil
}
