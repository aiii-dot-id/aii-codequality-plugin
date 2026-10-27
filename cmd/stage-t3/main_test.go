//go:build !wasm_unknown

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStageDefaultVariant(t *testing.T) {
	for _, def := range []string{"second", ""} {
		t.Run("default="+def, func(t *testing.T) {
			t.Chdir(t.TempDir())
			put := func(p string, b []byte) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, b, 0644); err != nil {
					t.Fatal(err)
				}
			}
			put("plugin.json", []byte(`{"id":"example","version":"1"}`))
			m := map[string]any{"id": "example", "version": "1", "variants": []map[string]string{{"variant_id": "first"}, {"variant_id": "second"}}}
			if def != "" {
				m["default_variant"] = def
			}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			put("dist/pkg/example-1/manifest.json", b)
			put("dist/pkg/example-1/install-root/fixture", []byte("unchanged"))
			if err := run(); err != nil {
				t.Fatal(err)
			}
			b, err = os.ReadFile("dist/t3/devsign.json")
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			value, present := got["default_variant"]
			if def == "" {
				if present {
					t.Fatalf("absent default synthesized: %v", value)
				}
			} else if value != def {
				t.Fatalf("default = %v, want %q", value, def)
			}
			b, err = os.ReadFile("dist/t3/install-root/fixture")
			if err != nil || string(b) != "unchanged" {
				t.Fatalf("file changed: %q %v", b, err)
			}
		})
	}
}
