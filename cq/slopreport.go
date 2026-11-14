package cq

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

type SlopFinding struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Line   int      `json:"line,omitempty"`
	Func   string   `json:"func,omitempty"`
	Detail string   `json:"detail,omitempty"`
	P      *float64 `json:"p,omitempty"`
}

var catalogueSlop = map[string]bool{"DC-04": true, "ST-02": true, "RD-04": true, "ST-04": true, "RD-02": true}

type slopJudged struct {
	findings []SlopFinding
	asked    SlopQuestions
	held     int
}

type SlopQuestions struct {
	Asked    int `json:"asked"`
	Answered int `json:"answered"`
	Failed   int `json:"failed"`
}

func (q *SlopQuestions) add(o SlopQuestions) {
	q.Asked += o.Asked
	q.Answered += o.Answered
	q.Failed += o.Failed
}

func (j intentJoin) slopOf(f FileReview, records []Record) slopJudged {
	var out slopJudged

	last := map[string]int{}
	for i, r := range records {
		last[r.ID] = i
	}
	for i, r := range records {
		if last[r.ID] != i {
			continue
		}
		switch r.Battery {
		case SlopBattery, OutlineBattery:
			out.asked.Asked++
			if contains(r.ReasonCodes, IntentFailed) || !batteryAnswered(r) {
				out.asked.Failed++
				continue
			}
			out.asked.Answered++
			window := fmt.Sprintf("%s\x00%d", fileKey(r.Ref, r.SourceDigest), r.Chunk)
			if r.Battery == SlopBattery && j.guard[window] != GuardClear {
				for _, id := range r.Expected {
					if r.Answers[id] >= PTrue {
						out.held++
					}
				}
				continue
			}
			kind, detail := "judged", fmt.Sprintf("lines %d-%d", r.Lines[0], r.Lines[1])
			if r.Battery == OutlineBattery {
				kind, detail = "outline", "judged from the outline, not the bodies; the judge does not name groups"
			}
			for _, id := range r.Expected {
				if p := r.Answers[id]; p >= PTrue {
					p := p
					out.findings = append(out.findings, SlopFinding{ID: id, Kind: kind, Line: r.Lines[0], Detail: detail, P: &p})
				}
			}
		}
	}
	for _, e := range f.Findings {
		if catalogueSlop[e.ID] {
			p := e.P
			out.findings = append(out.findings, SlopFinding{ID: e.ID, Kind: "catalogue", Line: e.Lines[0], Detail: e.Where, P: &p})
		}
	}
	return out
}

const RewrapWords = "fmt.Errorf of a lone %w, %v or %s, or errors.New of x.Error(): it adds nothing to the value it carries"

type Unlisted map[string]int

var unlistedKinds = map[string]bool{"RD-04": true, "ST-04": true}

const UnlistedWords = "counted, not listed: SL-FORWARD, a pass-through function that is not a same-name wrapper (Start passing to start is listed); RD-04 and ST-04, the catalogue's two kinds least often worth a look. Ask with list_all to list every one"

func (u *Unlisted) add(id string, n int) {
	if n == 0 {
		return
	}
	if *u == nil {
		*u = Unlisted{}
	}
	(*u)[id] += n
}

func (u *Unlisted) addAll(o Unlisted) {
	for id, n := range o {
		u.add(id, n)
	}
}

func (u Unlisted) whole() map[string]int { return u.of([]string{"SL-FORWARD", "RD-04", "ST-04"}) }

func (u Unlisted) of(kinds []string) map[string]int {
	out := map[string]int{}
	for _, id := range kinds {
		out[id] = u[id]
	}
	return out
}

func unlistedOn(lane string) []string {
	switch lane {
	case "slop":
		return []string{"SL-FORWARD", "RD-04", "ST-04"}
	case "maintainability", "":
		return []string{"RD-04", "ST-04"}
	}
	return nil
}

func (th Thresholds) unlist(f *FileReview) {
	f.Unlisted = nil
	if th.ListAll {
		return
	}
	kept := f.Findings[:0:0]
	for _, e := range f.Findings {
		if unlistedKinds[e.ID] {
			f.Unlisted.add(e.ID, 1)
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(f.Findings) {
		return
	}
	f.Findings, f.HasFindings, f.Lanes = kept, len(kept) > 0, nil
	for _, l := range []string{"correctness", "maintainability"} {
		if _, n := lanePriority(*f, l); n > 0 {
			f.Lanes = append(f.Lanes, l)
		}
	}
}

func passThroughOthers(s *FileShape) int {
	n := 0
	if s == nil || s.Test {
		return 0
	}
	for _, fn := range s.Funcs {
		if fn.Forward && fn.Callee != "" && !sameNameWrapper(fn) {
			n++
		}
	}
	return n
}

const forwardEarlier = "its whole body passes its parameters to one call (recorded by an earlier version, which did not name it; a rescan does)"

func (th Thresholds) deterministic(s *FileShape) []SlopFinding {
	if s == nil || s.Test {
		return nil
	}
	th = th.orDefault()
	var out []SlopFinding
	add := func(id string, line int, fn, detail string) {
		out = append(out, SlopFinding{ID: id, Kind: "deterministic", Line: line, Func: fn, Detail: detail})
	}
	for _, d := range s.Dups {
		if d[2] >= th.DupLines {
			add("SL-DUP", d[1], "", fmt.Sprintf("%d normalised lines repeat those at line %d", d[2], d[0]))
		}
	}
	for _, fn := range s.Funcs {
		if fn.Depth > th.Nesting {
			detail := fmt.Sprintf("nesting depth %d, over %d", fn.Depth, th.Nesting)
			if fn.ThroughLiteral {
				detail += ", through a function literal"
			}
			add("SL-NEST", fn.Line, fn.Name, detail)
		}
		if fn.Params > th.Params {
			add("SL-PARAMS", fn.Line, fn.Name, fmt.Sprintf("%d parameters, over %d", fn.Params, th.Params))
		}
		switch {
		case fn.Forward && fn.Callee == "":

			add("SL-FORWARD", fn.Line, fn.Name, forwardEarlier)
		case fn.Forward && (th.ListAll || sameNameWrapper(fn)):
			add("SL-FORWARD", fn.Line, fn.Name, "its whole body passes its parameters to "+fn.Callee)
		}
	}
	for _, b := range s.Blocks {
		if b.Depth > th.Nesting {
			add("SL-NEST", b.Line, "", fmt.Sprintf("nesting depth %d, over %d", b.Depth, th.Nesting))
		}
	}
	for _, l := range s.Rewrap {
		add("SL-REWRAP", l, "", RewrapWords)
	}
	for _, n := range s.Stubs {
		add("SL-STUB", n.Line, "", n.Text)
	}
	for _, l := range s.Unreach {
		add("SL-UNREACH", l, "", "a statement after return, panic or a jump")
	}
	return out
}

func (th Thresholds) markSlop(f *FileReview, pkg map[string][]SlopFinding) {
	f.Slop = nil
	f.Slop = append(f.Slop, th.deterministic(f.shape)...)
	f.Slop = append(f.Slop, pkg[path.Clean(f.Ref)]...)
	f.Slop = append(f.Slop, f.judgedSlop.findings...)
	sort.SliceStable(f.Slop, func(a, b int) bool { return f.Slop[a].Line < f.Slop[b].Line })
	if !th.ListAll {
		f.Unlisted.add("SL-FORWARD", passThroughOthers(f.shape))
		listed := f.Slop[:0:0]
		for _, sf := range f.Slop {
			if sf.Kind != "catalogue" || !unlistedKinds[sf.ID] {
				listed = append(listed, sf)
			}
		}
		f.Slop = listed
	}
	f.SlopCount = len(f.Slop)
	f.SlopTop = topKinds(f.Slop, 2)
	f.NotChecked = nil
	if f.shape != nil {
		for _, u := range f.shape.Unchecked {
			f.NotChecked = append(f.NotChecked, fmt.Sprintf("lines %d-%d not checked: %s", u.From, u.To, u.Why))
		}
	}
}

func topKinds(fs []SlopFinding, n int) []string {
	count := map[string]int{}
	for _, f := range fs {
		count[f.ID]++
	}
	ids := make([]string, 0, len(count))
	for id := range count {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool {
		if count[ids[a]] != count[ids[b]] {
			return count[ids[a]] > count[ids[b]]
		}
		return ids[a] < ids[b]
	})
	return ids[:min(n, len(ids))]
}

type PackageNote struct {
	Dir  string `json:"dir"`
	Note string `json:"note"`
}

func PackageFindings(j *Journal, done bool) (out map[string][]SlopFinding, notes []PackageNote, shapes int, err error) {
	out = map[string][]SlopFinding{}
	if j == nil {
		return out, nil, 0, nil
	}
	type file struct {
		ref   string
		shape *FileShape
	}
	var dir string
	var files []file
	flush := func(last bool) {
		if dir == "" {
			return
		}
		defer func() { dir, files = "", nil }()
		var skipped, unread []string
		goFiles := 0
		refs, testRefs, values := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, f := range files {
			switch {
			case f.shape.Skipped != "":
				skipped = append(skipped, path.Base(f.ref)+" ("+f.shape.Skipped+")")
			case f.shape.Test:
				for _, r := range f.shape.Refs {
					testRefs[r] = true
				}
			default:
				goFiles++
				if len(f.shape.Unchecked) > 0 {
					unread = append(unread, path.Base(f.ref))
				}
				for _, r := range f.shape.Refs {
					refs[r] = true
				}
				for _, v := range f.shape.Values {
					values[v] = true
				}
			}
		}
		if goFiles == 0 {
			return
		}
		switch {
		case len(skipped) > 0:
			notes = append(notes, PackageNote{Dir: dir, Note: "package rules not applied: not every non-test Go file was judged; left out: " + strings.Join(skipped, ", ")})
			return
		case len(unread) > 0:

			notes = append(notes, PackageNote{Dir: dir, Note: "package rules not applied: the tree rules could not read part of " + strings.Join(unread, ", ")})
			return
		case last && !done:
			notes = append(notes, PackageNote{Dir: dir, Note: "package rules not applied yet: the scan is still judging this directory"})
			return
		}
		for _, f := range files {
			if f.shape.Skipped != "" || f.shape.Test {
				continue
			}
			for _, d := range f.shape.Decls {
				name, at, _ := strings.Cut(d, ":")
				line, _ := strconv.Atoi(at)
				if refs[name] {
					continue
				}
				detail := "no caller in the package's non-test code"
				if testRefs[name] {
					detail += "; called only from tests"
				}
				out[f.ref] = append(out[f.ref], SlopFinding{ID: "SL-UNUSEDFUNC", Kind: "deterministic", Line: line, Func: name, Detail: detail})
			}
			for _, fn := range f.shape.Funcs {
				if fn.Method || len(fn.Unused) == 0 || values[fn.Name] || f.shape.Tagged {
					continue
				}
				out[f.ref] = append(out[f.ref], SlopFinding{ID: "SL-UNUSEDPARAM", Kind: "deterministic", Line: fn.Line, Func: fn.Name,
					Detail: "unused: " + strings.Join(fn.Unused, ", ")})
			}
		}
	}
	latest := map[string]int{}
	_, err = j.Walk(0, func(e JournalEvent) error {
		r := e.Record
		if r == nil || r.Battery != ShapeBattery || r.Shape == nil {
			return nil
		}
		shapes++
		ref := path.Clean(r.Ref)
		if d := path.Dir(ref); d != dir {
			flush(false)
			dir = d
			latest = map[string]int{}
		}
		if i, ok := latest[ref]; ok {
			files[i] = file{ref, r.Shape}
			return nil
		}
		latest[ref] = len(files)
		files = append(files, file{ref, r.Shape})
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return out, notes, shapes, err
	}
	flush(true)
	return out, notes, shapes, nil
}

type slopCounts struct {
	byKind   map[string]int
	asked    SlopQuestions
	unlisted Unlisted
}

func (c *slopCounts) add(f FileReview) {
	if c.byKind == nil {
		c.byKind = map[string]int{}
	}
	for _, sf := range f.Slop {
		c.byKind[sf.Kind]++
	}
	if f.SlopQs != nil {
		c.asked.add(*f.SlopQs)
	}
	c.unlisted.addAll(f.Unlisted)
}

func (c slopCounts) result() map[string]any {
	return map[string]any{"findings_deterministic": c.byKind["deterministic"], "findings_judged": c.byKind["judged"] + c.byKind["outline"],
		"findings_from_catalogue": c.byKind["catalogue"], "counted_not_listed": c.unlisted.whole(), "counted_not_listed_words": UnlistedWords,
		"questions": map[string]any{"asked": c.asked.Asked, "answered": c.asked.Answered, "failed": c.asked.Failed}}
}
