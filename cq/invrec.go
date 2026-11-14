package cq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	InvDir  = "dir"
	InvFile = "file"
	InvDone = "done"
)

const (
	InvListed          = "listed"
	InvCutShort        = "cut_short"
	InvUnreadable      = "unreadable"
	InvExcluded        = "excluded"
	InvRulesUnreadable = "rules_unreadable"
)

const (
	InvUnfinished = "unfinished"
	InvComplete   = "complete"
	InvDamaged    = "damaged"
)

const InvNamed = 50

const InvMaxMembers = 1 << 20

type InvRec struct {
	Kind    string `json:"k"`
	Index   int    `json:"i,omitempty"`
	Parent  int    `json:"p,omitempty"`
	Path    string `json:"path,omitempty"`
	State   string `json:"state,omitempty"`
	Entries int    `json:"entries,omitempty"`
	List    string `json:"list,omitempty"`
	Rules   string `json:"rules,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Total   int    `json:"total,omitempty"`
	Closure string `json:"closure,omitempty"`
}

func invDirState(s string) bool {
	switch s {
	case InvListed, InvCutShort, InvUnreadable, InvExcluded, InvRulesUnreadable:
		return true
	}
	return false
}

func (r InvRec) valid() bool {
	switch r.Kind {
	case InvDone:
		return r.Total >= 1 && r.Total <= InvMaxMembers && r.Closure != ""
	case InvDir:
		if !invDirState(r.State) {
			return false
		}
	case InvFile:
		if r.State == "" {
			return false
		}
	default:
		return false
	}
	if r.Index < 1 || r.Parent < 0 || r.Parent == r.Index {
		return false
	}
	if r.Path == "" || strings.ContainsAny(r.Path, "\x00\r\n") {
		return false
	}
	return (r.Parent == 0) == (r.Index == 1 && r.Kind == InvDir)
}

type InvEntry struct{ Name, Kind string }

func invSum(lines []string) string {
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func InvListDigest(entries []InvEntry) string {
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = e.Name + "\x00" + e.Kind + "\n"
	}
	sort.Strings(lines)
	return invSum(lines)
}

func InvClosure(members []InvRec) string {
	by := make(map[int]InvRec, len(members))
	nums := make([]int, 0, len(members))
	for _, m := range members {
		by[m.Index] = m
		nums = append(nums, m.Index)
	}
	sort.Ints(nums)
	lines := make([]string, len(nums))
	for i, n := range nums {
		m := by[n]
		lines[i] = strconv.Itoa(n) + "\x00" + m.Path + "\x00" + m.Kind + "\x00" + m.State + "\x00" + m.Digest + "\n"
	}
	return invSum(lines)
}

func invChild(dir, child string) (string, bool) {
	name := path.Base(child)
	want := name
	if dir != "." {
		want = dir + "/" + name
	}
	return name, want == child && name != "." && name != "/"
}

type InvView struct {
	Status string

	Total int

	Members int

	Unknown int

	MissingCount int
	Missing      []int

	Orphans int

	Invalid int

	Conflicts int

	Why []string

	recs map[int]InvRec
	pos  map[int]int
	kids map[int][]int
}

func FoldInventory(recs []InvRec) *InvView {
	v := &InvView{recs: map[int]InvRec{}, pos: map[int]int{}, kids: map[int][]int{}}
	var done *InvRec
	for i := range recs {
		r := recs[i]
		switch {
		case !r.valid():
			v.Invalid++
		case r.Kind == InvDone:
			if done == nil {
				done = &recs[i]
			} else if *done != r {
				v.Conflicts++
			}
		default:
			v.add(r, i)
		}
	}
	v.settle(done)
	return v
}

func (v *InvView) add(r InvRec, at int) {
	if first, ok := v.recs[r.Index]; ok {
		if first != r {
			v.Conflicts++
		}
		return
	}
	v.recs[r.Index], v.pos[r.Index] = r, at
	v.kids[r.Parent] = append(v.kids[r.Parent], r.Index)
	v.Members++
}

func (v *InvView) isKnown(i int) bool {
	for steps := 0; steps <= len(v.recs); steps++ {
		r, ok := v.recs[i]
		if !ok {
			return false
		}
		if r.Parent == 0 {
			return true
		}
		p, ok := v.recs[r.Parent]
		if !ok || p.Kind != InvDir || v.pos[r.Parent] > v.pos[i] {
			return false
		}
		if p.State != InvListed && p.State != InvCutShort {
			return false
		}
		if _, under := invChild(p.Path, r.Path); !under {
			return false
		}
		i = r.Parent
	}
	return false
}

func (v *InvView) closed(dir int) bool {
	d, ok := v.recs[dir]
	if !ok || d.Kind != InvDir || d.State != InvListed {
		return false
	}
	kids := v.kids[dir]
	ents := make([]InvEntry, 0, len(kids))
	for _, k := range kids {
		c := v.recs[k]
		name, under := invChild(d.Path, c.Path)
		if !under || v.pos[k] < v.pos[dir] {
			return false
		}
		ents = append(ents, InvEntry{name, c.Kind})
	}
	return len(kids) == d.Entries && InvListDigest(ents) == d.List
}

func (v *InvView) Established(dir int) bool {
	return v.Status != InvUnfinished && v.isKnown(dir) && v.closed(dir)
}

func (v *InvView) settle(done *InvRec) {
	if done == nil {
		v.Status, v.Total = InvUnfinished, v.Members
		return
	}
	v.Total = done.Total
	known := 0
	for i := range v.recs {
		if i <= v.Total && v.isKnown(i) {
			known++
		}
	}
	v.findMissing()
	v.Orphans = v.Members - known
	v.Unknown = v.Total - known
	v.Why = v.damage(done)
	v.Status = InvComplete
	if len(v.Why) > 0 {
		v.Status = InvDamaged
	}
}

func (v *InvView) findMissing() {
	for i := 1; i <= v.Total; i++ {
		if _, ok := v.recs[i]; ok {
			continue
		}
		v.MissingCount++
		if len(v.Missing) < InvNamed {
			v.Missing = append(v.Missing, i)
		}
	}
}

func (v *InvView) damage(done *InvRec) []string {
	var why []string
	if v.MissingCount > 0 {
		why = append(why, "records missing: "+strconv.Itoa(v.MissingCount))
	}
	if v.Orphans > 0 {
		why = append(why, "members with no place in the tree: "+strconv.Itoa(v.Orphans))
	}
	open := 0
	members := make([]InvRec, 0, len(v.recs))
	for i, r := range v.recs {
		members = append(members, r)
		if r.Kind == InvDir && r.State == InvListed && !v.closed(i) {
			open++
		}
	}
	if open > 0 {
		why = append(why, "directories whose listed entries are not all recorded: "+strconv.Itoa(open))
	}
	if InvClosure(members) != done.Closure {
		why = append(why, "the closing record's digest does not match the records")
	}
	return why
}

func ReadInventory(s Store, file string) (*InvView, RecStats, error) {
	var recs []InvRec
	unparsed := 0
	st, err := FoldRecs(s, file, func(body []byte) error {
		var r InvRec
		if json.Unmarshal(body, &r) != nil {
			unparsed++
			return nil
		}
		recs = append(recs, r)
		return nil
	})
	if err != nil {
		return nil, st, err
	}
	v := FoldInventory(recs)
	v.Invalid += unparsed
	return v, st, nil
}
