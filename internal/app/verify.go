package app

import (
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"sync"
	"time"

	"ancienttrees/internal/geom"
	"ancienttrees/internal/rtree"
)

// ReinsertComparison measures a clean incremental build with forced
// reinsertion on and off across several seeds, so the effect of rule 2 can be
// checked independently of delete churn.
type ReinsertComparison struct {
	Trees      int       `json:"trees"`
	Seeds      []int64   `json:"seeds"`
	On         []float64 `json:"overlapOn"`
	Off        []float64 `json:"overlapOff"`
	OnPerItem  []float64 `json:"overlapPerItemOn"`
	OffPerItem []float64 `json:"overlapPerItemOff"`
}

// CompareReinsert builds the same synthetic city for each seed twice, once
// with forced reinsertion and once without, and records total overlap.
func CompareReinsert(trees int, maxEntries, pct int, seeds []int64) ReinsertComparison {
	out := ReinsertComparison{Trees: trees, Seeds: seeds}
	for _, seed := range seeds {
		data := NewDataset(seed, trees, 1)
		// Each mode gets its own order built from the same seed, so neither
		// mode benefits from the other's insertion sequence.
		order := data.Shuffled(seed*7 + 1)
		for _, on := range []bool{true, false} {
			tr := rtree.New(rtree.Options{Dims: 2, MaxEntries: maxEntries,
				ForceReinsert: on, ReinsertPercent: pct})
			for _, t := range order {
				tr.Insert(t.BBox2, int64(t.ID), t)
			}
			snap := tr.Snapshot()
			if on {
				out.On = append(out.On, snap.TotalOverlap)
				out.OnPerItem = append(out.OnPerItem, snap.TotalOverlap/float64(snap.ItemCount))
			} else {
				out.Off = append(out.Off, snap.TotalOverlap)
				out.OffPerItem = append(out.OffPerItem, snap.TotalOverlap/float64(snap.ItemCount))
			}
		}
	}
	return out
}

// VerifyConfig controls a verification run.
type VerifyConfig struct {
	Trees           int
	Ops             int
	Seed            int64
	MaxEntries      int
	ReinsertPercent int
	Workers         int
	QueriesPerBatch int
	Progress        func(string)
}

// QuerySummary aggregates per-query work for one query type in one mode.
type QuerySummary struct {
	Count      int     `json:"count"`
	AvgNodes   float64 `json:"avgNodes"`
	MaxNodes   int     `json:"maxNodes"`
	AvgEntries float64 `json:"avgEntries"`
	AvgResults float64 `json:"avgResults"`
	TotalNodes int64   `json:"totalNodes"`
}

// ConfigEcho is the JSON-friendly view of a run's configuration.
type ConfigEcho struct {
	Trees           int   `json:"trees"`
	Ops             int   `json:"ops"`
	Seed            int64 `json:"seed"`
	MaxEntries      int   `json:"maxEntries"`
	ReinsertPercent int   `json:"reinsertPercent"`
	Workers         int   `json:"workers"`
	QueriesPerBatch int   `json:"queriesPerBatch"`
}

// ModeSummary is the structural summary at the end of a run for one mode.
type ModeSummary struct {
	Name            string                  `json:"name"`
	ForceReinsert   bool                    `json:"forceReinsert"`
	Height          int                     `json:"height"`
	Nodes           int                     `json:"nodes"`
	Leaves          int                     `json:"leaves"`
	Items           int                     `json:"items"`
	TotalOverlap    float64                 `json:"totalOverlap"`
	TotalCoverage   float64                 `json:"totalCoverage"`
	OverlapPerItem  float64                 `json:"overlapPerItem"`
	Splits          int64                   `json:"splits"`
	ReinsertOps     int64                   `json:"reinsertOps"`
	ReinsertEntries int64                   `json:"reinsertEntries"`
	CondensedNodes  int64                   `json:"condensedNodes"`
	Dims            uint8                   `json:"dims"`
	Queries         map[string]QuerySummary `json:"queries"`
	Levels          []LevelDetail           `json:"levels"`
	Dims3           *DimsSummary            `json:"dims3,omitempty"`
}

// DimsSummary is the structure of the 3-D index for the same mode.
type DimsSummary struct {
	Height          int     `json:"height"`
	Nodes           int     `json:"nodes"`
	Leaves          int     `json:"leaves"`
	Items           int     `json:"items"`
	Overlap         float64 `json:"overlap"`
	Coverage        float64 `json:"coverage"`
	Splits          int64   `json:"splits"`
	ReinsertOps     int64   `json:"reinsertOps"`
	ReinsertEntries int64   `json:"reinsertEntries"`
}

// LevelDetail is one level of the finished tree.
type LevelDetail struct {
	Level    int     `json:"level"`
	Nodes    int     `json:"nodes"`
	Entries  int     `json:"entries"`
	Overlap  float64 `json:"overlap"`
	Coverage float64 `json:"coverage"`
}

// BuildCompare compares a bulk-built tree with an incrementally built one.
type BuildCompare struct {
	Dims               uint8   `json:"dims"`
	BulkHeight         int     `json:"bulkHeight"`
	IncrHeight         int     `json:"incrHeight"`
	BulkNodes          int     `json:"bulkNodes"`
	IncrNodes          int     `json:"incrNodes"`
	BulkOverlap        float64 `json:"bulkOverlap"`
	IncrOverlap        float64 `json:"incrOverlap"`
	BulkCoverage       float64 `json:"bulkCoverage"`
	IncrCoverage       float64 `json:"incrCoverage"`
	BulkOverlapPerItem float64 `json:"bulkOverlapPerItem"`
	IncrOverlapPerItem float64 `json:"incrOverlapPerItem"`
	BulkAvgNodes       float64 `json:"bulkAvgNodes"`
	IncrAvgNodes       float64 `json:"incrAvgNodes"`
	QueriesChecked     int     `json:"queriesChecked"`
	QueryMismatches    int     `json:"queryMismatches"`
}

// VerifyReport is the outcome of a verification run.
type VerifyReport struct {
	Config      VerifyConfig   `json:"-"`
	ConfigEcho  ConfigEcho     `json:"config"`
	ElapsedMS   int64          `json:"elapsedMs"`
	OpsRun      int            `json:"opsRun"`
	Inserts     int            `json:"inserts"`
	Deletes     int            `json:"deletes"`
	Queries     int            `json:"queries"`
	LiveTrees   int            `json:"liveTrees"`
	Mismatches  int            `json:"mismatches"`
	MismatchLog []string       `json:"mismatchLog,omitempty"`
	Modes       []ModeSummary  `json:"modes"`
	Builds      []BuildCompare `json:"builds"`
	Passed      bool           `json:"passed"`
}

func (c *VerifyConfig) defaults() {
	if c.Trees <= 0 {
		c.Trees = 30000
	}
	if c.Ops <= 0 {
		c.Ops = 300000
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = 16
	}
	if c.ReinsertPercent <= 0 {
		c.ReinsertPercent = 30
	}
	if c.Workers <= 0 {
		c.Workers = runtime.NumCPU()
	}
	if c.Progress == nil {
		c.Progress = func(string) {}
	}
}

// modeState is one index configuration under test (reinsert on or off).
type modeState struct {
	name string
	on   bool
	t2   *rtree.Tree
	t3   *rtree.Tree
	q    map[string]*queryAcc
}

type queryAcc struct {
	nodes, entries, results, count, maxNodes int64
}

func (a *queryAcc) add(nodes, entries, results int) {
	a.count++
	a.nodes += int64(nodes)
	a.entries += int64(entries)
	a.results += int64(results)
	if int64(nodes) > a.maxNodes {
		a.maxNodes = int64(nodes)
	}
}

func newMode(name string, on bool, cfg *VerifyConfig) *modeState {
	opts := rtree.Options{
		Dims: 2, MaxEntries: cfg.MaxEntries,
		ForceReinsert: on, ReinsertPercent: cfg.ReinsertPercent,
	}
	opts3 := opts
	opts3.Dims = 3
	return &modeState{
		name: name, on: on,
		t2: rtree.New(opts),
		t3: rtree.New(opts3),
		q: map[string]*queryAcc{
			"range2d": {}, "range3d": {}, "polygon": {},
			"nearestK": {}, "nearestIter": {}, "nearestMaxDist": {},
		},
	}
}

func (m *modeState) acc(kind string) *queryAcc { return m.q[kind] }

// RunVerification performs the interleaved workload and compares every index
// result against the brute-force reference.
func RunVerification(cfg VerifyConfig) *VerifyReport {
	cfg.defaults()
	start := time.Now()
	rng := rand.New(rand.NewSource(cfg.Seed))
	data := NewDataset(cfg.Seed, cfg.Trees, cfg.Trees/12+1)

	modes := []*modeState{
		newMode("reinsert-on", true, &cfg),
		newMode("reinsert-off", false, &cfg),
	}
	rep := &VerifyReport{Config: cfg}
	rep.ConfigEcho = ConfigEcho{
		Trees: cfg.Trees, Ops: cfg.Ops, Seed: cfg.Seed,
		MaxEntries: cfg.MaxEntries, ReinsertPercent: cfg.ReinsertPercent,
		Workers: cfg.Workers, QueriesPerBatch: cfg.QueriesPerBatch,
	}

	// Initial load, in random order, into every mode.
	initial := data.Shuffled(cfg.Seed + 1)
	for _, t := range initial {
		for _, m := range modes {
			m.t2.Insert(t.BBox2, int64(t.ID), t)
			m.t3.Insert(t.BBox3, int64(t.ID), t)
		}
	}

	step := cfg.Ops / 20
	if step < 1 {
		step = 1
	}

	// Queries are answered immediately by the live index and queued; the
	// brute-force reference runs over each batch in parallel. The queue is
	// flushed before every mutation so the reference always scans exactly the
	// tree set that the index saw.
	workers := cfg.Workers
	if workers < 1 {
		workers = 1
	}
	queue := make([]pendingQuery, 0, cfg.QueriesPerBatch)
	batchCap := cfg.QueriesPerBatch
	if batchCap <= 0 {
		batchCap = 2000
	}
	flush := func() {
		if len(queue) == 0 {
			return
		}
		verifyBatch(rep, modes, data, queue, workers)
		queue = queue[:0]
	}

	for op := 0; op < cfg.Ops; op++ {
		if op%step == 0 {
			cfg.Progress(fmt.Sprintf("op %d/%d live=%d mismatches=%d", op, cfg.Ops, data.LiveCount(), rep.Mismatches))
		}
		if data.LiveCount() < 50 {
			insertRandom(data, rng, modes)
			rep.Inserts++
			rep.OpsRun++
			continue
		}
		switch k := rng.Intn(100); {
		case k < 12:
			flush()
			insertRandom(data, rng, modes)
			rep.Inserts++
		case k < 22:
			flush()
			t := data.RandomTree(rng)
			data.Remove(t.ID)
			for _, m := range modes {
				if !m.t2.Delete(t.BBox2, int64(t.ID)) {
					rep.fail("delete missing in 2D tree", t.ID)
				}
				if !m.t3.Delete(t.BBox3, int64(t.ID)) {
					rep.fail("delete missing in 3D tree", t.ID)
				}
			}
			rep.Deletes++
		case k < 50:
			q := data.RandomQueryRect(rng, 2000)
			queue = append(queue, makeRangeQuery(rep, modes, "range2d", q, 2))
		case k < 60:
			q := data.RandomQueryBox3(rng, 2000)
			queue = append(queue, makeRangeQuery(rep, modes, "range3d", q, 3))
		case k < 72:
			line := data.RandomLine(rng)
			queue = append(queue, makePolyQuery(rep, modes, line))
		case k < 84:
			p := data.RandomPoint(rng)
			kk := 1 + rng.Intn(20)
			queue = append(queue, makeNearQuery(rep, modes, "nearestK", 2, p, kk, 0))
		case k < 94:
			p := data.RandomPoint(rng)
			limit := 50 + rng.Intn(200)
			queue = append(queue, makeNearQuery(rep, modes, "nearestIter", 2, p, limit, 0))
		default:
			p := data.RandomPoint3(rng)
			dist := 20 + rng.Float64()*600
			queue = append(queue, makeNearQuery(rep, modes, "nearestMaxDist", 3, p, -1, dist))
		}
		rep.OpsRun++
		if len(queue) >= batchCap {
			flush()
		}
		if rep.Mismatches > 20 {
			break
		}
	}
	flush()

	// Structural invariants.
	for _, m := range modes {
		if err := m.t2.Check(); err != nil {
			rep.fail(fmt.Sprintf("%s 2D invariant: %v", m.name, err), -1)
		}
		if err := m.t3.Check(); err != nil {
			rep.fail(fmt.Sprintf("%s 3D invariant: %v", m.name, err), -1)
		}
	}

	for _, m := range modes {
		sum := summarizeMode(m)
		s3 := m.t3.Snapshot()
		sum.Dims3 = &DimsSummary{
			Height: s3.Height, Nodes: s3.NodeCount, Leaves: s3.LeafCount,
			Items: s3.ItemCount, Overlap: s3.TotalOverlap, Coverage: s3.TotalCoverage,
			Splits: s3.Splits, ReinsertOps: s3.ReinsertOps,
			ReinsertEntries: s3.ReinsertEntries,
		}
		rep.Modes = append(rep.Modes, sum)
	}
	rep.Builds = compareBuilds(data, &cfg, rep)
	rep.Queries = rep.totalQueries()
	rep.LiveTrees = data.LiveCount()
	rep.ElapsedMS = time.Since(start).Milliseconds()
	rep.Passed = rep.Mismatches == 0
	for _, b := range rep.Builds {
		if b.QueryMismatches > 0 {
			rep.Passed = false
		}
	}
	return rep
}

func (r *VerifyReport) fail(msg string, id int) {
	r.Mismatches++
	if len(r.MismatchLog) < 50 {
		if id >= 0 {
			r.MismatchLog = append(r.MismatchLog, fmt.Sprintf("%s (tree %d)", msg, id))
		} else {
			r.MismatchLog = append(r.MismatchLog, msg)
		}
	}
}

func (r *VerifyReport) totalQueries() int {
	n := 0
	for _, m := range r.Modes {
		for _, q := range m.Queries {
			n += q.Count
		}
	}
	return n
}

func insertRandom(data *Dataset, rng *rand.Rand, modes []*modeState) {
	t := data.AddRandomTree(rng)
	for _, m := range modes {
		m.t2.Insert(t.BBox2, int64(t.ID), t)
		m.t3.Insert(t.BBox3, int64(t.ID), t)
	}
}

func summarizeMode(m *modeState) ModeSummary {
	s2 := m.t2.Snapshot()
	out := ModeSummary{
		Name:            m.name,
		ForceReinsert:   m.on,
		Height:          s2.Height,
		Nodes:           s2.NodeCount,
		Leaves:          s2.LeafCount,
		Items:           s2.ItemCount,
		TotalOverlap:    s2.TotalOverlap,
		TotalCoverage:   s2.TotalCoverage,
		Splits:          s2.Splits,
		ReinsertOps:     s2.ReinsertOps,
		ReinsertEntries: s2.ReinsertEntries,
		CondensedNodes:  s2.CondensedNodes,
		Dims:            2,
		Queries:         map[string]QuerySummary{},
	}
	if s2.ItemCount > 0 {
		out.OverlapPerItem = s2.TotalOverlap / float64(s2.ItemCount)
	}
	for _, l := range s2.Levels {
		out.Levels = append(out.Levels, LevelDetail{
			Level: l.Level, Nodes: l.Nodes, Entries: l.Entries,
			Overlap: l.Overlap, Coverage: l.Coverage,
		})
	}
	for k, a := range m.q {
		if a.count == 0 {
			continue
		}
		out.Queries[k] = QuerySummary{
			Count:      int(a.count),
			AvgNodes:   float64(a.nodes) / float64(a.count),
			MaxNodes:   int(a.maxNodes),
			AvgEntries: float64(a.entries) / float64(a.count),
			AvgResults: float64(a.results) / float64(a.count),
			TotalNodes: a.nodes,
		}
	}
	return out
}

func sameIDs(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pendingQuery is one query whose index answers were captured when it was
// issued and whose brute-force reference is computed later, in parallel.
type pendingQuery struct {
	kind    string
	dims    uint8
	box     geom.Box
	poly    [][2]float64
	point   [3]float64
	k       int
	maxDist float64
	answers []capturedAnswer
}

type capturedAnswer struct {
	index int
	ids   []int
	nodes int
	exams int
}

func cloneIDs(a []int) []int { return append([]int(nil), a...) }

func makeRangeQuery(rep *VerifyReport, modes []*modeState, kind string, q geom.Box, dims uint8) pendingQuery {
	pq := pendingQuery{kind: kind, dims: dims, box: q}
	for i, m := range modes {
		var ids []int
		var st rtree.QueryStats
		if dims == 3 {
			ids, st = m.t3Search(q)
		} else {
			ids, st = m.t2Search(q)
		}
		pq.answers = append(pq.answers, capturedAnswer{index: i, ids: ids, nodes: st.NodesVisited, exams: st.EntriesExamined})
	}
	return pq
}

func makePolyQuery(rep *VerifyReport, modes []*modeState, line *RedLine) pendingQuery {
	pq := pendingQuery{kind: "polygon", dims: 2, box: line.Rect(), poly: line.Poly()}
	for i, m := range modes {
		ids, st := m.t2SearchPoly(line)
		pq.answers = append(pq.answers, capturedAnswer{index: i, ids: ids, nodes: st.NodesVisited, exams: st.EntriesExamined})
	}
	return pq
}

func makeNearQuery(rep *VerifyReport, modes []*modeState, kind string, dims uint8, p [3]float64, k int, maxDist float64) pendingQuery {
	pq := pendingQuery{kind: kind, dims: dims, point: p, k: k, maxDist: maxDist}
	for i, m := range modes {
		tr := m.t2
		if dims == 3 {
			tr = m.t3
		}
		it := tr.Nearest(p, maxDist)
		if k > 0 {
			it.SetLimit(k)
		}
		var ids []int
		for {
			if k >= 0 && len(ids) >= k {
				break
			}
			_, key, _, ok := it.Next()
			if !ok {
				break
			}
			ids = append(ids, int(key))
		}
		pq.answers = append(pq.answers, capturedAnswer{index: i, ids: ids, nodes: it.Stats.NodesVisited, exams: it.Stats.EntriesExamined})
	}
	return pq
}

// verifyBatch computes brute-force references in parallel, then checks every
// captured answer and accumulates per-query statistics.
func verifyBatch(rep *VerifyReport, modes []*modeState, data *Dataset, qs []pendingQuery, workers int) {
	want := make([][]int, len(qs))
	jobs := make(chan int, len(qs))
	for i := range qs {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				q := &qs[i]
				switch q.kind {
				case "range2d":
					want[i] = data.BruteRectIDs(q.box)
				case "range3d":
					want[i] = data.BruteBox3IDs(q.box)
				case "polygon":
					want[i] = data.BrutePolyHits(q.box)
				case "nearestMaxDist":
					want[i] = data.BruteNearestIDs(q.point, -1, q.maxDist, q.dims)
				default:
					want[i] = data.BruteNearestIDs(q.point, q.k, 0, q.dims)
				}
			}
		}()
	}
	wg.Wait()

	for i := range qs {
		q := &qs[i]
		for _, a := range q.answers {
			if !sameIDs(a.ids, want[i]) {
				rep.fail(fmt.Sprintf("%s %s mismatch: got %d want %d", modes[a.index].name, q.kind, len(a.ids), len(want[i])), -1)
			}
			modes[a.index].acc(q.kind).add(a.nodes, a.exams, len(a.ids))
		}
	}
}

// compareBuilds rebuilds the final dataset with STR packing and with plain
// insertion, checks both against brute force and reports structural metrics.
func compareBuilds(data *Dataset, cfg *VerifyConfig, rep *VerifyReport) []BuildCompare {
	rng := rand.New(rand.NewSource(cfg.Seed + 99))
	var out []BuildCompare
	for _, dims := range []uint8{2, 3} {
		opts := rtree.Options{Dims: dims, MaxEntries: cfg.MaxEntries,
			ForceReinsert: true, ReinsertPercent: cfg.ReinsertPercent}
		bulk := rtree.New(opts)
		incr := rtree.New(opts)
		order := data.SortedLive()
		items := make([]rtree.BulkItem, len(order))
		for i, t := range order {
			items[i] = rtree.BulkItem{Rect: t.Rect(dims), Key: int64(t.ID), Data: t}
		}
		bulk.BulkLoad(items)
		shuffled := data.Shuffled(cfg.Seed + 7)
		for _, t := range shuffled {
			incr.Insert(t.Rect(dims), int64(t.ID), t)
		}
		if err := bulk.Check(); err != nil {
			rep.fail(fmt.Sprintf("bulk %dD invariant: %v", dims, err), -1)
		}
		if err := incr.Check(); err != nil {
			rep.fail(fmt.Sprintf("incremental %dD invariant: %v", dims, err), -1)
		}
		bs := bulk.Snapshot()
		is := incr.Snapshot()
		bc := BuildCompare{
			Dims: dims, BulkHeight: bs.Height, IncrHeight: is.Height,
			BulkNodes: bs.NodeCount, IncrNodes: is.NodeCount,
			BulkOverlap: bs.TotalOverlap, IncrOverlap: is.TotalOverlap,
			BulkCoverage: bs.TotalCoverage, IncrCoverage: is.TotalCoverage,
		}
		if bs.ItemCount > 0 {
			bc.BulkOverlapPerItem = bs.TotalOverlap / float64(bs.ItemCount)
			bc.IncrOverlapPerItem = is.TotalOverlap / float64(is.ItemCount)
		}
		// Query parity on a fresh sample.
		var bulkNodes, incrNodes int64
		const sample = 300
		for i := 0; i < sample; i++ {
			q := data.RandomQueryRect(rng, 2500)
			if dims == 3 {
				q = data.RandomQueryBox3(rng, 2500)
			}
			var want []int
			if dims == 3 {
				want = data.BruteBox3IDs(q)
			} else {
				want = data.BruteRectIDs(q)
			}
			a := collectKeys(bulk, q)
			b := collectKeys(incr, q)
			bulkNodes += int64(a.nodes)
			incrNodes += int64(b.nodes)
			if !sameIDs(a.ids, want) || !sameIDs(b.ids, want) {
				bc.QueryMismatches++
				rep.fail(fmt.Sprintf("build compare %dD query mismatch", dims), -1)
			}
			bc.QueriesChecked++
		}
		bc.BulkAvgNodes = float64(bulkNodes) / float64(bc.QueriesChecked)
		bc.IncrAvgNodes = float64(incrNodes) / float64(bc.QueriesChecked)
		out = append(out, bc)
	}
	return out
}

type keyedResult struct {
	ids   []int
	nodes int
}

func collectKeys(t *rtree.Tree, q geom.Box) keyedResult {
	ids := make([]int, 0, 16)
	st := t.Search(q, func(e rtree.Entry) { ids = append(ids, int(e.Key)) })
	sort.Ints(ids)
	return keyedResult{ids: ids, nodes: st.NodesVisited}
}

// --- helpers on modeState ------------------------------------------------

func (m *modeState) t2Search(q geom.Box) ([]int, rtree.QueryStats) {
	ids := make([]int, 0, 16)
	st := m.t2.Search(q, func(e rtree.Entry) { ids = append(ids, int(e.Key)) })
	sort.Ints(ids)
	return ids, st
}

func (m *modeState) t3Search(q geom.Box) ([]int, rtree.QueryStats) {
	ids := make([]int, 0, 16)
	st := m.t3.Search(q, func(e rtree.Entry) { ids = append(ids, int(e.Key)) })
	sort.Ints(ids)
	return ids, st
}

func (m *modeState) t2SearchPoly(line *RedLine) ([]int, rtree.QueryStats) {
	box := line.Rect()
	ids := make([]int, 0, 16)
	st := m.t2.Search(box, func(e rtree.Entry) {
		t := e.Data.(*Tree)
		if geom.PolyBoxIntersects(t.Crown, box) {
			ids = append(ids, t.ID)
		}
	})
	sort.Ints(ids)
	return ids, st
}
