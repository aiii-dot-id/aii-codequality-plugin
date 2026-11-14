package cq

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"path"
	"runtime"
	"sort"
)

var ErrJournalInterleaved = errors.New("a file's journal events are not in one run")

var errStopWalk = errors.New("stop")

type FileRun struct {
	Ref      string
	Start    int64
	Events   int
	Records  []Record
	Coverage []Coverage

	budget   uint64
	budgeted bool
}

func frontierRow(c *Coverage) bool { return c != nil && contains(c.Reasons, "step_budget") }

func isBoundary(c *Coverage) bool { return c != nil && c.Kind == "subtree" }

func isBudgetMarker(c *Coverage) bool {
	return c != nil && c.Kind == "file" && c.Disposition == "pending" && len(c.Reasons) == 1 &&
		(c.Reasons[0] == "file_budget" || c.Reasons[0] == "examined_budget")
}

func isLeft(c *Coverage) bool {
	return c != nil && c.Kind == "file" && c.SourceDigest != "" && c.ExpectedChunks > 0 &&
		(c.Disposition == "unknown" || c.Disposition == "excluded")
}

func boundarySaid(c *Coverage) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\x00", c.Kind, c.Size, c.ExpectedChunks, c.CompletedChunks)
	h.Write([]byte(c.Disposition))
	for _, r := range c.Reasons {
		h.Write([]byte{0})
		h.Write([]byte(r))
	}
	h.Write([]byte{0, 0})
	h.Write([]byte(c.SourceDigest))
	return h.Sum64()
}

func oneBoundary(run *FileRun) (uint64, bool) {
	if len(run.Records) != 0 || len(run.Coverage) != 1 || !isBoundary(&run.Coverage[0]) {
		return 0, false
	}
	return boundarySaid(&run.Coverage[0]), true
}

func refHash(ref string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(ref))
	return h.Sum64()
}

func (run *FileRun) again(e JournalEvent) bool {
	if e.Record != nil {
		return false
	}
	if said, ok := oneBoundary(run); ok && isBoundary(e.Coverage) && boundarySaid(e.Coverage) == said {
		return true
	}
	return run.budgeted && (isBudgetMarker(e.Coverage) || isLeft(e.Coverage)) && boundarySaid(e.Coverage) == run.budget
}

func (run *FileRun) take(e JournalEvent) {
	if e.Record != nil {
		run.Records = append(run.Records, *e.Record)
		run.budgeted = false
		return
	}
	run.Coverage = append(run.Coverage, *e.Coverage)
	if run.budgeted = isBudgetMarker(e.Coverage) || isLeft(e.Coverage); run.budgeted {
		run.budget = boundarySaid(e.Coverage)
	}
}

func FoldJournal(j *Journal, visit func(*FileRun) error) (int64, error) {
	var run *FileRun
	closed := map[uint64]struct{}{}

	boundaries := map[uint64]uint64{}
	flush := func() error {
		if run == nil {
			return nil
		}
		h := refHash(run.Ref)
		closed[h] = struct{}{}
		if said, ok := oneBoundary(run); ok {
			boundaries[h] = said
		} else if run.budgeted {
			boundaries[h] = run.budget
		}
		r := run
		run = nil
		return visit(r)
	}
	n := 0
	end, err := j.WalkAt(0, func(e JournalEvent, at, _ int64) error {
		if n++; n%1000 == 0 {
			runtime.GC()
		}
		if frontierRow(e.Coverage) {
			if run != nil {
				run.Events++
			}
			return nil
		}
		ref := ""
		if e.Record != nil {
			ref = path.Clean(e.Record.Ref)
		} else {
			ref = path.Clean(e.Coverage.Ref)
		}
		if run == nil || run.Ref != ref {
			if said, ok := boundaries[refHash(ref)]; ok && e.Record == nil && (isBoundary(e.Coverage) || isBudgetMarker(e.Coverage) || isLeft(e.Coverage)) && boundarySaid(e.Coverage) == said {
				if run != nil {
					run.Events++
				}
				return nil
			}
			if err := flush(); err != nil {
				return err
			}
			if _, seen := closed[refHash(ref)]; seen {
				return fmt.Errorf("%w: %s", ErrJournalInterleaved, ref)
			}
			run = &FileRun{Ref: ref, Start: at}
		} else if run.again(e) {
			run.Events++
			return nil
		}
		run.Events++
		run.take(e)
		return nil
	})
	if err != nil {
		return end, err
	}
	return end, flush()
}

func ReadRun(j *Journal, start int64, events int) (*FileRun, error) {
	run := &FileRun{Start: start}
	begun := false
	_, err := j.WalkAt(start, func(e JournalEvent, _, _ int64) error {
		if run.Events == events {
			return errStopWalk
		}
		run.Events++
		if frontierRow(e.Coverage) {
			return nil
		}
		ref := ""
		if e.Record != nil {
			ref = path.Clean(e.Record.Ref)
		} else {
			ref = path.Clean(e.Coverage.Ref)
		}
		switch {
		case !begun:
			run.Ref, begun = ref, true
		case ref != run.Ref:
			if e.Record != nil || !(isBoundary(e.Coverage) || isBudgetMarker(e.Coverage) || isLeft(e.Coverage)) {
				return fmt.Errorf("%w: the run of %s holds an event of %s that no fold leaves out", ErrJournalCorrupt, run.Ref, ref)
			}
			return nil
		case run.again(e):
			return nil
		}
		run.take(e)
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, err
	}
	return run, nil
}

type foldKey struct {
	Bucket int    `json:"b"`
	Rank   int    `json:"r"`
	N      int    `json:"n"`
	Key    string `json:"k"`
}

func (a foldKey) less(b foldKey) bool {
	switch {
	case a.Bucket != b.Bucket:
		return a.Bucket < b.Bucket
	case a.Rank != b.Rank:
		return a.Rank > b.Rank
	case a.N != b.N:
		return a.N > b.N
	}
	return a.Key < b.Key
}

func reviewKey(f FileReview, lane string) foldKey {
	key := fileKey(f.Ref, f.SourceDigest)
	if lane == "size" {
		lines := 0
		if f.Size != nil {
			lines = f.Size.Lines
		}
		return foldKey{Rank: lines, Key: key}
	}
	if lane == "slop" {
		return foldKey{Rank: f.SlopCount, Key: key}
	}
	if lane == "maintainability" || lane == "" && f.Tier == "" {
		r, n := lanePriority(f, "maintainability")
		bucket := 0
		if lane == "" {
			bucket = len(tierOrder)
		}
		return foldKey{Bucket: bucket, Rank: r, N: n, Key: key}
	}
	tier, p, n := tierEvidence(f)
	bucket := 0
	for i, t := range tierOrder {
		if t == tier {
			bucket = i
		}
	}
	return foldKey{Bucket: bucket, Rank: int(math.Round(p * 1000)), N: n, Key: key}
}

func gapKey(g Coverage) foldKey {
	return foldKey{Bucket: gapPriority(g), Key: fileKey(g.Ref, g.SourceDigest)}
}

type foldCursor struct {
	Generation string  `json:"generation"`
	Shape      string  `json:"shape"`
	Lane       string  `json:"lane"`
	Tier       string  `json:"tier,omitempty"`
	Path       string  `json:"path,omitempty"`
	All        bool    `json:"all,omitempty"`
	Th         [5]int  `json:"th"`
	After      foldKey `json:"after"`
}

type ReviewQuery struct {
	Shape, Lane, Cursor, Generation string
	Tier                            string
	Path                            string
	PageBytes, Limit                int
	ScanDone                        bool
	Frontier                        []Coverage
	IntentFile                      *Journal
	Size                            SizeThresholds
	Package                         map[string][]SlopFinding
	PackageNotes                    []PackageNote
	SlopNote                        string
	IntentUnread                    string
	EarlierVersion                  string
}

func (q ReviewQuery) intentRun(c *IntentCursor, run *FileRun) ([]Record, int64, int) {
	if q.IntentUnread != "" {
		return append(append([]Record(nil), run.Records...), IntentUnreadable(run.Ref)), 0, -1
	}
	extra, start, n := c.Take(run.Ref)
	if len(extra) == 0 {
		return run.Records, 0, 0
	}
	if n == 0 {
		n = -1
	}
	return append(append(make([]Record, 0, len(run.Records)+len(extra)), run.Records...), extra...), start, n
}

func (q ReviewQuery) intentAgain(run *FileRun, start int64, n int) ([]Record, error) {
	switch {
	case n < 0:
		return append(append([]Record(nil), run.Records...), IntentUnreadable(run.Ref)), nil
	case n == 0:
		return run.Records, nil
	}
	extra, err := ReadIntent(q.IntentFile, start, n)
	if err != nil {
		return append(append([]Record(nil), run.Records...), IntentUnreadable(run.Ref)), nil
	}
	return append(append(make([]Record, 0, len(run.Records)+len(extra)), run.Records...), extra...), nil
}

func ReviewFold(j *Journal, q ReviewQuery) (ReviewPage, error) {
	out := ReviewPage{Policy: ReviewPolicy, Generation: q.Generation, CoverageShape: "coverage", ReportComplete: true,
		Caveat: reviewCaveat, Entries: json.RawMessage("[]")}
	if q.IntentUnread != "" {
		out.IntentNote = fmt.Sprintf(intentUnread, q.IntentUnread)
	}
	cursor := NewIntentCursor(q.IntentFile)
	var intentTotals IntentCounts
	if err := q.normalize(); err != nil {
		return out, err
	}
	var after *foldKey
	if q.Cursor != "" {
		var c foldCursor
		b, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Generation != q.Generation || c.Shape != q.Shape || c.Lane != q.Lane || c.Tier != q.Tier || c.Path != q.Path || c.All != q.Size.ListAll || c.Th != q.Size.key() {
			return out, ErrStaleCursor
		}
		after = &c.After
	}

	block := newPageBlock(q.PageBytes)
	type pick struct {
		k       foldKey
		start   int64
		events  int
		intent  int64
		intentN int
		gap     Coverage
	}
	var picks []pick
	qualifying := 0
	consider := func(p pick) {
		if after != nil && !after.less(p.k) {
			return
		}
		qualifying++
		i := sort.Search(len(picks), func(i int) bool { return p.k.less(picks[i].k) })
		if i >= q.Limit {
			return
		}
		picks = append(picks, pick{})
		copy(picks[i+1:], picks[i:])
		picks[i] = p
		if len(picks) > q.Limit {
			picks = picks[:q.Limit]
		}
	}
	frontier := map[string]bool{}
	for _, g := range q.Frontier {
		frontier[path.Clean(g.Ref)] = true
	}
	inventory, assessment := q.ScanDone, q.ScanDone
	files, gaps, earlier, scoped := 0, 0, 0, 0
	counts := newTierCounts()
	var unlisted Unlisted
	if _, err := FoldJournal(j, func(run *FileRun) error {
		delete(frontier, run.Ref)
		records, intentAt, intentN := q.intentRun(cursor, run)
		rep := BuildReview(records, run.Coverage, q.ScanDone, true, q.Lane)
		inventory = inventory && rep.InventoryComplete
		assessment = assessment && rep.AssessmentComplete
		for _, f := range rep.Files {
			q.view(&f)
			if q.inPath(f.Ref) && f.Tier != "" {
				counts[f.Tier]++
			}
			if q.inPath(f.Ref) {
				unlisted.addAll(f.Unlisted)
			}
			if q.inPath(f.Ref) && f.Intent != nil {
				intentTotals.Add(*f.Intent)
			}
			if !q.wantsFile(f) {
				continue
			}
			files++
			if q.Shape != "coverage" {
				consider(pick{k: reviewKey(f, q.Lane), start: run.Start, events: run.Events, intent: intentAt, intentN: intentN})
			}
		}
		for _, g := range rep.Gaps {
			if !q.inPath(g.Ref) {
				continue
			}
			gaps++
			if why := scopeReason(g); why != "" {
				if scoped < earlierListMax {
					out.OutOfScopeFiles = append(out.OutOfScopeFiles, ScopeFile{Ref: g.Ref, Reason: why, Words: ScopeWords[why], Size: g.Size})
				}
				scoped++
			}
			if g.Kind == "file" && contains(g.Reasons, NotAssessedEarlier) {
				if earlier < earlierListMax {
					out.EarlierFiles = append(out.EarlierFiles, EarlierFile{Ref: g.Ref, Size: g.Size})
				}
				earlier++
			}
			if q.Shape == "coverage" {
				consider(pick{k: gapKey(g), gap: g})
			}
		}
		return nil
	}); err != nil {
		return out, err
	}
	if err := cursor.Finish(); err != nil && q.IntentUnread == "" {
		out.IntentNote = fmt.Sprintf(intentStopped, err.Error())
	}
	if q.Shape != "coverage" {
		out.IntentTotals = &intentTotals
	}

	if q.Shape != "coverage" {
		out.PackageNotes = q.PackageNotes
		out.SlopNote = q.SlopNote
	}
	q.unlisted(&out, unlisted)
	if q.Lane == "slop" || q.Lane == "size" {
		out.Thresholds = q.Size.Effective()
	}
	for _, g := range q.Frontier {
		if !frontier[path.Clean(g.Ref)] {
			continue
		}
		inventory, assessment = false, false
		if !q.inPath(g.Ref) {
			continue
		}
		gaps++
		if q.Shape == "coverage" {
			consider(pick{k: gapKey(g), gap: g})
		}
	}
	if q.Shape != "coverage" {
		out.TierCounts = counts
	}
	if earlier > 0 {
		out.EarlierVersion = q.EarlierVersion
	}
	if q.Shape == "triage" {
		out.OutOfScopeCount = &scoped
		out.EarlierCount = &earlier
	} else {
		out.EarlierFiles = nil
		out.OutOfScopeFiles = nil
	}

	runtime.GC()
	out.InventoryComplete, out.AssessmentComplete, out.GapCount = inventory, assessment, gaps
	out.Total = files
	if q.Shape == "coverage" {
		out.Total = gaps
	}
	size, shown := 2, 0
	var last foldKey
	for _, p := range picks {
		var entry any = p.gap
		if q.Shape != "coverage" {
			run, err := ReadRun(j, p.start, p.events)
			if err != nil {
				return out, err
			}
			records, err := q.intentAgain(run, p.intent, p.intentN)
			if err != nil {
				return out, err
			}
			rep := BuildReview(records, run.Coverage, q.ScanDone, true, q.Lane)
			found := false
			for _, f := range rep.Files {
				q.view(&f)
				if reviewKey(f, q.Lane) == p.k {
					entry, found = q.entry(f), true
					break
				}
			}
			if !found {
				return out, fmt.Errorf("%w: %s changed between two reads", ErrJournalCorrupt, run.Ref)
			}
		}
		b, err := json.Marshal(entry)
		if err != nil {
			return out, err
		}
		if size+len(b)+1 > q.PageBytes {
			if shown > 0 {
				break
			}
			if b, err = fitted(entry, q.PageBytes-size-1); err != nil {
				return out, err
			}
		}
		size += len(b) + 1
		block.add(b)
		shown++
		last = p.k
	}
	out.Entries, out.block = block.entries(), block
	if shown < qualifying {
		b, err := json.Marshal(foldCursor{Generation: q.Generation, Shape: q.Shape, Lane: q.Lane, Tier: q.Tier, Path: q.Path, All: q.Size.ListAll, Th: q.Size.key(), After: last})
		if err != nil {
			return out, err
		}
		out.Next = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}

type SummaryFold struct {
	worst    int
	bands    map[string]int
	byLang   map[string]*summaryAgg
	findings map[string]int
	leads    map[string]int
	all      summaryAgg
	lowest   []summaryChunk
	seen     int
	top      []rankedRow
	intent   IntentCounts
	earlier  int
	scope    map[string]int
	sizes    map[string]int
	noCode   int
	Size     SizeThresholds
	Package  map[string][]SlopFinding

	slop slopCounts
}

func (s *SummaryFold) AddRun(records []Record, coverage ...Coverage) {

	sized := map[string]FileSize{}
	for _, r := range records {
		if size, ok := sizeRecorded(r); ok {
			sized[path.Clean(r.Ref)] = size
		}
	}
	for _, size := range sized {
		f := FileReview{Size: &size}
		s.Size.mark(&f)
		if f.Size.Finding != "" {
			s.sizes[f.Size.Finding]++
		}
	}
	addWindows(records, &s.all, s.byLang)
	rep := BuildReview(records, coverage, false, true, "slop")
	for _, g := range rep.Gaps {
		if g.Kind == "file" && contains(g.Reasons, NotAssessedEarlier) {
			s.earlier++
		}
		if why := scopeReason(g); why != "" {
			s.scope[why]++
		}
	}
	for _, f := range rep.Files {
		s.noCode += f.NoCode
		s.Size.markWith(&f, s.Package)
		s.slop.add(f)
		if row, ok := pinnedRow(f); ok {
			s.top = topRows(s.top, row, s.worst)
		}
		if f.Intent != nil {
			s.intent.Add(*f.Intent)
		}
	}
}

type summaryAgg struct{ lines, chunks, scored, weighted int }

func judgedWindow(r Record) (string, bool) {
	if r.Skipped != "" || r.Band == "" {
		return "", false
	}
	return fmt.Sprintf("%s\x00%d", fileKey(r.Ref, r.SourceDigest), r.Chunk), true
}

func addWindows(records []Record, all *summaryAgg, byLang map[string]*summaryAgg) {
	seen := map[string]bool{}
	for _, r := range records {
		k, ok := judgedWindow(r)
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		a := byLang[r.Language]
		if a == nil {
			a = &summaryAgg{}
			byLang[r.Language] = a
		}
		for _, x := range []*summaryAgg{a, all} {
			x.lines += r.Lines[1] - r.Lines[0] + 1
			x.chunks++
		}
	}
}

type summaryChunk struct {
	r   Record
	seq int
}

func NewSummaryFold(worst int) *SummaryFold {
	return &SummaryFold{Package: map[string][]SlopFinding{}, worst: worst, bands: map[string]int{}, byLang: map[string]*summaryAgg{}, findings: map[string]int{}, leads: map[string]int{}, scope: map[string]int{}, sizes: map[string]int{}}
}

func (s *SummaryFold) Add(r Record) {
	if r.Skipped != "" || r.Band == "" {
		return
	}
	lines := r.Lines[1] - r.Lines[0] + 1
	s.bands[r.Band]++
	a := s.byLang[r.Language]
	if a == nil {
		a = &summaryAgg{}
		s.byLang[r.Language] = a
	}
	for _, x := range []*summaryAgg{a, &s.all} {
		x.scored += lines
		x.weighted += lines * r.Index
	}
	for _, f := range r.Findings {
		if Lead(f.ID) {
			s.leads[f.ID]++
		} else {
			s.findings[f.ID]++
		}
	}
	c := summaryChunk{r: r, seq: s.seen}
	s.seen++
	i := sort.Search(len(s.lowest), func(i int) bool { return s.lowest[i].r.Index > r.Index })
	if i >= s.worst {
		return
	}
	s.lowest = append(s.lowest, summaryChunk{})
	copy(s.lowest[i+1:], s.lowest[i:])
	s.lowest[i] = c
	if len(s.lowest) > s.worst {
		s.lowest = s.lowest[:s.worst]
	}
}

func (s *SummaryFold) Result() map[string]any {
	mean := func(a *summaryAgg) any {
		if a.scored == 0 {
			return nil
		}
		return a.weighted / a.scored
	}
	langs := map[string]any{}
	for k, a := range s.byLang {
		langs[k] = map[string]any{"chunks": a.chunks, "lines": a.lines, "index": mean(a)}
	}
	w := make([]any, 0, len(s.lowest))
	for _, c := range s.lowest {
		r := c.r
		fs, ls := []any{}, []any{}
		for _, f := range r.Findings {
			item := map[string]any{"id": f.ID}
			if f.Where != "" {
				item["where"] = f.Where
			}
			if Lead(f.ID) {
				ls = append(ls, item)
			} else {
				fs = append(fs, item)
			}
		}
		assessment, _ := json.Marshal(AssessmentOf(r))
		w = append(w, map[string]any{"ref": r.Ref, "chunk": r.Chunk, "index": r.Index, "band": r.Band, "legacy_score": true, "assessment": json.RawMessage(assessment), "findings": fs, "leads": ls})
	}
	ids := make([]string, 0, len(s.findings)+len(s.leads))
	for id := range s.findings {
		ids = append(ids, id)
	}
	for id := range s.leads {
		ids = append(ids, id)
	}
	return map[string]any{"chunks_judged": s.all.chunks, "no_code_windows": s.noCode, "lines": s.all.lines, "index": mean(&s.all), "bands": s.bands,
		"languages": langs, "finding_counts": s.findings, "lead_counts": s.leads, "faults": Faults(ids), "worst": w,
		"worst_basis": worstBasis, "top_correctness": correctnessRows(s.top), "intent": s.intent.result(), NotAssessedEarlier: s.earlier, "out_of_scope": scopeCounts(s.scope),
		"size_findings":     map[string]int{"large_file": s.sizes["large_file"], "very_large_file": s.sizes["very_large_file"]},
		"slop":              s.slop.result(),
		"thresholds":        s.Size.Effective(),
		"recommended_shape": "triage", "coverage_shape": "coverage",
		"score_caveat": scoreCaveat}
}

type FindingsFold struct {
	Glob, MinSeverity, Language string
	Cursor, PageBytes           int
	Page                        []Record
	Next                        int
	Total                       int

	Over int
	size int
}

func (f *FindingsFold) Add(r Record) {
	sel := Select([]Record{r}, f.Glob, f.MinSeverity, f.Language)
	if len(sel) == 0 {
		return
	}
	i := f.Total
	f.Total++
	if i < max(f.Cursor, 0) || f.Next >= 0 && f.Next <= i && f.full() {
		return
	}
	r = sel[0]
	MarkLeads([]Record{r})
	shown := r
	shown.Answers = nil
	b, _ := json.Marshal(shown)
	if f.size+len(b) > f.PageBytes && len(f.Page) > 0 {
		f.Next = i
		f.size = f.PageBytes + 1
		return
	}
	f.size += len(b)
	f.Page = append(f.Page, r)
	if f.size > f.PageBytes {
		f.Over = len(b)
	}
}

func (f *FindingsFold) full() bool { return f.size > f.PageBytes }

func NewFindingsFold(glob, minSeverity, language string, cursor, pageBytes int) *FindingsFold {
	return &FindingsFold{Glob: glob, MinSeverity: minSeverity, Language: language, Cursor: cursor, PageBytes: pageBytes, Next: -1}
}

func scopeCounts(by map[string]int) map[string]int {
	out := map[string]int{}
	for _, r := range OutOfScope {
		out[r] = by[r]
	}
	out[IgnoredDir] = by[IgnoredDir]
	return out
}
