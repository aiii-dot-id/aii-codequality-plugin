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
				var rs []rule
				if name == ".gitattributes" {
					rs, e = attributeRules(line, scope)
					attrs = append(attrs, rs...)
				} else {
					rs, e = scopedRule(line, scope, false)
					ign = append(ign, rs...)
				}
				if e != nil {
					return nil, nil, fmt.Errorf("rule file %q line %d: %w", file, n+1, e)
				}
			}
		}
	}
	return ign, attrs, nil
}

var exclusionAttributes = map[string]bool{"linguist-vendored": true, "linguist-generated": true, "linguist-documentation": true}

func attributeRules(line, scope string) ([]rule, error) {
	fields := strings.Fields(line)
	type state struct {
		name string
		set  bool
	}
	var states []state
	for _, a := range fields[1:] {
		prefix, rest := "", a
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "!") {
			prefix, rest = a[:1], a[1:]
		}
		name, value, valued := strings.Cut(rest, "=")
		switch {
		case !strings.HasPrefix(name, "linguist-"):
			continue
		case strings.HasPrefix(fields[0], "[attr]"):
			return nil, fmt.Errorf("unsupported linguist attribute in a macro")
		case name == "linguist-language" || name == "linguist-detectable":
			continue
		case !exclusionAttributes[name]:
			return nil, fmt.Errorf("unsupported linguist attribute %q", name)
		case prefix == "" && (!valued || value == "true"):
			states = append(states, state{name, true})
		case prefix == "" && value == "false", prefix != "" && !valued:
			states = append(states, state{name, false})
		default:
			return nil, fmt.Errorf("unsupported linguist attribute value %q", a)
		}
	}
	if len(states) == 0 {
		return nil, nil
	}
	pattern := fields[0]
	if strings.HasPrefix(pattern, "!") || strings.HasSuffix(pattern, "/") {
		return nil, fmt.Errorf("unsupported attribute pattern")
	}
	base, err := scopedRule(pattern, scope, true)
	if err != nil {
		return nil, err
	}
	out := make([]rule, 0, len(states))
	for _, st := range states {
		r := base[0]
		r.attr, r.neg = st.name, !st.set
		out = append(out, r)
	}
	return out, nil
}

func scopedRule(pattern, scope string, attr bool) ([]rule, error) {
	p := strings.TrimPrefix(pattern, "!")
	p = strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/")
	if p == "" {
		return nil, fmt.Errorf("empty rule pattern")
	}
	for i := 0; i < len(p); i++ {
		ch := p[i]
		if ch == '[' {
			n, ok := charClass(p[i:])
			if !ok {
				return nil, fmt.Errorf("unsupported character class")
			}
			i += n - 1
			continue
		}
		if ch < ' ' || ch >= 127 || strings.IndexByte("\\\"]", ch) >= 0 {
			return nil, fmt.Errorf("unsupported pattern character or escaping")
		}
	}
	for _, component := range strings.Split(p, "/") {
		if component == "" || component == "." || component == ".." ||
			(strings.Contains(component, "**") && component != "**") {
			return nil, fmt.Errorf("unsupported pattern component")
		}
	}
	rules, err := compileRules([]string{pattern})
	if err != nil {
		return nil, err
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
	return []rule{r}, err
}

func charClass(p string) (n int, ok bool) {
	i := 1
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		i++
	}
	start := i
	for ; i < len(p) && p[i] != ']'; i++ {
		if p[i] <= ' ' || p[i] >= 127 || strings.IndexByte("\\[/\"", p[i]) >= 0 {
			return 0, false
		}
	}
	return i + 1, i < len(p) && i > start
}
