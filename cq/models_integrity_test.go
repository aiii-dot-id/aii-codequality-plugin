package cq

import (
	"fmt"
	"strings"
	"testing"
)

func TestModelListRefusesPartialOrAmbiguousSuccess(t *testing.T) {
	for _, raw := range []string{
		`{"models":[{"name":"valid"},{}]}`,
		`{"models":[{"name":"valid"},{"name":"  "}]}`,
		`{"models":[{"name":"first","name":"second"}]}`,
		`{"models":[{"name":"valid"},null]}`,
	} {
		if _, err := ModelChoices([]byte(raw)); err == nil {
			t.Errorf("accepted incomplete/ambiguous list %s", raw)
		}
	}
	names := make([]string, MaxModelChoices)
	for i := range names {
		names[i] = fmt.Sprintf(`{"name":"model-%d"}`, i)
	}
	raw := `{"models":[` + strings.Join(names, ",") + `]}`
	if got, err := ModelChoices([]byte(raw)); err != nil || len(got) != MaxModelChoices {
		t.Fatalf("boundary: %d %v", len(got), err)
	}
	raw = `{"models":[` + strings.Join(names, ",") + `,{"name":"extra"}]}`
	if _, err := ModelChoices([]byte(raw)); err == nil {
		t.Fatal("oversized list silently trimmed")
	}
}
