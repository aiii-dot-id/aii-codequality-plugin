//go:build !wasm_unknown

// stage-t3 turns the tree 'aiisdk package' staged into the staging directory the T3 signing
// path reads: devsign.json plus install-root/**. The spec is derived from the staged
// manifest.json, so the platform-signed package declares exactly what the T0 package declared.
//
//	go tool aiisdk build && go tool aiisdk package
//	go run ./cmd/stage-t3                                   # writes dist/t3/
//	aii-devsign -staging dist/t3 -payload-out dist/t3/ceremony-payload.json
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stage-t3:", err)
		os.Exit(1)
	}
}

func run() error {
	var author struct {
		ID, Version string
	}
	b, err := os.ReadFile("plugin.json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &author); err != nil {
		return err
	}
	staged := filepath.Join("dist", "pkg", author.ID+"-"+author.Version)
	b, err = os.ReadFile(filepath.Join(staged, "manifest.json"))
	if err != nil {
		return fmt.Errorf("%w (run 'go tool aiisdk package' first)", err)
	}
	var m struct {
		ID, Version, Title, Description, Publisher, License, Homepage string
		PluginFamily                                                  string             `json:"plugin_family"`
		AiiosMin                                                      string             `json:"aiios_min_version"`
		AiiosMax                                                      string             `json:"aiios_max_exclusive_version"`
		Envelope                                                      []string           `json:"capability_envelope"`
		Interfaces                                                    map[string][]iface `json:"interfaces"`
		Variants                                                      []variant          `json:"variants"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	spec := map[string]any{"id": m.ID, "version": m.Version, "title": m.Title, "description": m.Description,
		"publisher": m.Publisher, "license": m.License, "homepage": m.Homepage, "plugin_family": m.PluginFamily,
		"capability_envelope": m.Envelope}
	// The host window travels too: a signed bundle without it would be
	// offered to hosts the release says it cannot run on.
	if m.AiiosMin != "" {
		spec["aiios_min_version"] = m.AiiosMin
	}
	if m.AiiosMax != "" {
		spec["aiios_max_exclusive_version"] = m.AiiosMax
	}
	var ifaces []any
	for _, list := range m.Interfaces {
		for _, i := range list {
			ifaces = append(ifaces, map[string]any{"id": i.ID, "version": i.Version, "methods": i.Methods,
				"schema_file": fmt.Sprintf("interfaces/%s.v%d.schema.json", i.ID, i.Version)})
		}
	}
	spec["interfaces"] = ifaces
	var vs []any
	for _, v := range m.Variants {
		vs = append(vs, map[string]any{"id": v.ID, "platform": v.Platform, "arch": v.Arch, "topology": v.Topology,
			"runtime": v.Runtime, "profile": v.Profile, "entrypoint": v.Entrypoint, "capabilities": v.Capabilities})
	}
	spec["variants"] = vs
	out := filepath.Join("dist", "t3")
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	var files []string
	err = filepath.WalkDir(filepath.Join(staged, "install-root"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(staged, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(out, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return err
	}
	b, _ = json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "devsign.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	sort.Strings(files)
	fmt.Printf("staged %s: devsign.json and %d files\n  %s\n", out, len(files), strings.Join(files, "\n  "))
	return nil
}

type iface struct {
	ID      string   `json:"id"`
	Version int      `json:"version"`
	Methods []string `json:"methods"`
}

type variant struct {
	ID           string   `json:"variant_id"`
	Platform     string   `json:"platform"`
	Arch         string   `json:"arch"`
	Topology     string   `json:"topology"`
	Runtime      string   `json:"execution_runtime"`
	Profile      string   `json:"admission_profile"`
	Entrypoint   string   `json:"entrypoint"`
	Capabilities []string `json:"variant_capabilities"`
}
