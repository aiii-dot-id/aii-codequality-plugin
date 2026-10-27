package cq

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

const maxScopedRulesBytes = 128 << 10

func scopedRules(fs FS, base, dir string) (ign, attrs []rule, err error) {
	normalize := func(p string) (string, error) {
		if strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
			return "", fmt.Errorf("rules path must be slash-relative")
		}
		for _, part := range strings.Split(p, "/") {
			if part == ".." {
				return "", fmt.Errorf("rules path must not contain parent traversal")
			}
		}
		p = path.Clean(p)
		if p == "." {
			p = ""
		}
		return p, nil
	}
	if base, err = normalize(base); err != nil {
		return nil, nil, err
	}
	if dir, err = normalize(dir); err != nil {
		return nil, nil, err
	}
	if base != "" && dir != base && !strings.HasPrefix(dir, base+"/") {
		return nil, nil, fmt.Errorf("rules directory %q is outside base %q", dir, base)
	}
	chain := []string{dir}
	for p := dir; p != base; {
		p = path.Dir(p)
		if p == "." {
			p = ""
		}
		chain = append(chain, p)
	}
	total := 0
	for i := len(chain) - 1; i >= 0; i-- {
		d := chain[i]
		entries, truncated, e := fs.List(d)
		if e != nil {
			return nil, nil, fmt.Errorf("list rule directory %q: %w", d, e)
		}

		if truncated {
			return nil, nil, fmt.Errorf("rule directory %q listing truncated; absence cannot be established", d)
		}
		scope := strings.TrimPrefix(strings.TrimPrefix(d, base), "/")
		for _, name := range []string{".gitignore", ".gitattributes"} {
			var entry *Entry
			for j := range entries {
				if entries[j].Name == name {
					entry = &entries[j]
					break
				}
			}
			if entry == nil {
				continue
			}
			file := path.Join(d, name)
			if entry.Symlink || entry.Dir {
				return nil, nil, fmt.Errorf("rule file %q is a symlink or directory", file)
			}
			limit := min(maxRulesBytes, maxScopedRulesBytes-total)
			if entry.Size > int64(limit) {
				return nil, nil, fmt.Errorf("rule file %q exceeds remaining %d-byte rule budget: %w", file, limit, ErrTooLarge)
			}
			raw, e := fs.Read(file, int64(limit))
			if e != nil {
				return nil, nil, fmt.Errorf("read rule file %q: %w", file, e)
			}
			if len(raw) > limit {
				return nil, nil, fmt.Errorf("rule file %q exceeds remaining %d-byte rule budget: %w", file, limit, ErrTooLarge)
			}
			total += len(raw)
			for n, line := range strings.Split(string(raw), "\n") {
				if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				line = strings.TrimRight(line, " \t\r")
				isAttr := name == ".gitattributes"
				r, e := scopedRule(line, scope, isAttr)
				if e != nil {
					return nil, nil, fmt.Errorf("rule file %q line %d: %w", file, n+1, e)
				}
				if isAttr {
					attrs = append(attrs, r)
				} else {
					ign = append(ign, r)
				}
			}
		}
	}
	return ign, attrs, nil
}

func scopedRule(line, scope string, attr bool) (rule, error) {
	pattern := line
	if attr {
		parts := strings.Fields(line)
		if len(parts) < 2 || strings.TrimLeft(line, " \t\r") != line {
			return rule{}, fmt.Errorf("unsupported attribute syntax")
		}
		for _, a := range parts[1:] {
			if vendoredAttr("x "+a) != "x" {
				return rule{}, fmt.Errorf("unsupported attribute semantics")
			}
		}
		pattern = vendoredAttr(line)
		if strings.HasPrefix(pattern, "!") || strings.HasSuffix(pattern, "/") {
			return rule{}, fmt.Errorf("unsupported attribute pattern")
		}
	}
	p := strings.TrimPrefix(pattern, "!")
	p = strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/")
	if p == "" {
		return rule{}, fmt.Errorf("empty rule pattern")
	}
	for _, ch := range p {
		if ch <= ' ' || ch >= 127 || strings.ContainsRune("\\\"[]", ch) {
			return rule{}, fmt.Errorf("unsupported pattern character or escaping")
		}
	}
	for _, component := range strings.Split(p, "/") {
		if component == "" || component == "." || component == ".." ||
			(strings.Contains(component, "**") && component != "**") {
			return rule{}, fmt.Errorf("unsupported pattern component")
		}
	}
	rules, err := compileRules([]string{pattern})
	if err != nil {
		return rule{}, err
	}
	r := rules[0]
	rx := r.rx.String()
	if attr {
		rx = strings.TrimSuffix(rx, "(?:/.*)?$") + "$"
	}
	if scope != "" {
		rx = "^" + regexp.QuoteMeta(scope+"/") + strings.TrimPrefix(rx, "^")
	}
	r.rx, err = regexp.Compile(rx)
	return r, err
}
