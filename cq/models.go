package cq

import (
	"encoding/json"
	"fmt"
)

const MaxModelChoices = 256

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
