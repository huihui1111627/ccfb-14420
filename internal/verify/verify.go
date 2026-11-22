// Package verify implements the exhaustive differential test required for the
// survey index: a few hundred thousand mixed operations, each compared against
// a brute-force scan, plus the forced-reinsertion A/B statistics.
package verify

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"ancienttrees/internal/forest"
	"ancienttrees/internal/geom"
	"ancienttrees/internal/rtree"
)

// Options controls a verification run.
type Options struct {
	Seed     int64 `json:"seed"`
	N        int   `json:"n"`
	Ops      int   `json:"ops"` // total operations (range, knn, delete, insert, polygon)
	Clusters int   `json:"clusters"`
	DaysBack int   `json:"daysBack"`
	// Progress is called with periodic updates; it is not serialised.
	Progress func(p Progress) `json:"-"`
}

// DefaultOptions is the full acceptance run.
func DefaultOptions(seed int64) Options {
	return Options{
		Seed:     seed,
		N:        45000,
		Ops:      260000,
		Clusters: 55,
		DaysBack: 365 * 6,
	}
}

// Progress is one progress update.
type Progress struct {
	Phase   string  `json:"phase"`
	Done    int     `json:"done"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
	Message string  `json:"message"`
}

// Report is the verification result, serialisable for the page.
type Report struct {
	StartedAt  time.Time       `json:"startedAt"`
	DurationMS int64           `json:"durationMs"`
	Options    Options         `json:"options"`
	Records    int             `json:"records"`
	OpsRun     int             `json:"opsRun"`
	Passed     bool            `json:"passed"`
	Failures   []Failure       `json:"failures"`
	OpStats    map[string]int  `json:"opStats"`
	Insertion  InsertionReport `json:"insertion"`
	Bulk       BulkReport      `json:"bulk"`
	Index2D    IndexReport     `json:"index2d"`
	Index3D    IndexReport     `json:"index3d"`
	ReinsertAB ReinsertAB      `json:"reinsertAB"`
	BuildStats BuildStats      `json:"buildStats"`
	// Structure checks.
	Structure StructureReport `json:"structure"`
	Message   string          `json:"message"`
}

// Failure is one divergence between index and brute force.
type Failure struct {
	Op     string `json:"op"`
	Step   int    `json:"step"`
	Detail string `json:"detail"`
	Want   int    `json:"want"`
	Got    int    `json:"got"`
	Extra  string `json:"extra,omitempty"`
}

// InsertionReport covers the random-order insertion tree.
type InsertionReport struct {
	Records     int    `json:"records"`
	Height      int    `json:"height"`
	Nodes       int    `json:"nodes"`
	Splits      int64  `json:"splits"`
	ReinsertOps int64  `json:"reinsertOps"`
	Reinserted  int64  `json:"reinsertedEntries"`
	Valid       bool   `json:"valid"`
	ValidMsg    string `json:"validMsg,omitempty"`
}

// BulkReport compares STR bulk loading with sequential insertion.
type BulkReport struct {
	BulkHeight        int     `json:"bulkHeight"`
	InsertHeight      int     `json:"insertHeight"`
	BulkNodes         int     `json:"bulkNodes"`
	InsertNodes       int     `json:"insertNodes"`
	BulkLeafFill      float64 `json:"bulkLeafFill"`
	InsertLeafFill    float64 `json:"insertLeafFill"`
	BulkOverlapArea   float64 `json:"bulkOverlapArea"`
	InsertOverlapArea float64 `json:"insertOverlapArea"`
	BulkAvgNodes      float64 `json:"bulkAvgNodes"`
	InsertAvgNodes    float64 `json:"insertAvgNodes"`
	BulkValid         bool    `json:"bulkValid"`
	QueriesCompared   int     `json:"queriesCompared"`
}

// IndexReport records per-index statistics.
type IndexReport struct {
	Height           int     `json:"height"`
	Nodes            int     `json:"nodes"`
	Leaves           int     `json:"leaves"`
	AvgNodesPerQuery float64 `json:"avgNodesPerQuery"`
	AvgLeafPerQuery  float64 `json:"avgLeafPerQuery"`
	CoverArea        float64 `json:"coverArea"`
	OverlapArea      float64 `json:"overlapArea"`
	Queries          int64   `json:"queries"`
}

// ReinsertAB is the forced-reinsertion on/off comparison.
type ReinsertAB struct {
	OnAvgNodes       float64 `json:"onAvgNodes"`
	OffAvgNodes      float64 `json:"offAvgNodes"`
	OnAvgLeaf        float64 `json:"onAvgLeaf"`
	OffAvgLeaf       float64 `json:"offAvgLeaf"`
	OnOverlapArea    float64 `json:"onOverlapArea"`
	OffOverlapArea   float64 `json:"offOverlapArea"`
	OnCoverArea      float64 `json:"onCoverArea"`
	OffCoverArea     float64 `json:"offCoverArea"`
	OnSplits         int64   `json:"onSplits"`
	OffSplits        int64   `json:"offSplits"`
	OnReinsertOps    int64   `json:"onReinsertOps"`
	OnReinserted     int64   `json:"onReinserted"`
	OnHeight         int     `json:"onHeight"`
	OffHeight        int     `json:"offHeight"`
	Queries          int     `json:"queries"`
	SameAnswers      bool    `json:"sameAnswers"`
	NodesImprovement float64 `json:"nodesImprovementPct"`
}

// BuildStats records construction cost.
type BuildStats struct {
	InsertionMS int64 `json:"insertionMs"`
	BulkMS      int64 `json:"bulkMs"`
	ABInsertMS  int64 `json:"abInsertMs"`
}

// StructureReport holds invariant-check results.
type StructureReport struct {
	Checked  int      `json:"checked"`
	Failures []string `json:"failures"`
}

// Run executes the whole verification. It is safe to run in a background
// goroutine; progress is reported through Options.Progress.
func Run(ctx context.Context, opt Options) (*Report, error) {
	start := time.Now()
	rep := &Report{
		StartedAt: start,
		Options:   opt,
		OpStats:   map[string]int{},
		Failures:  []Failure{},
	}
	progress := func(phase string, done, total int, msg string) {
		if opt.Progress == nil {
			return
		}
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(done) / float64(total)
		}
		opt.Progress(Progress{Phase: phase, Done: done, Total: total, Percent: pct, Message: msg})
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	progress("生成古树", 0, 1, "按城区成团分布生成古树与树冠多边形")
	gen := forest.DefaultGenConfig(opt.Seed)
	gen.N = opt.N
	gen.Clusters = opt.Clusters
	gen.DaysBack = opt.DaysBack
	simNow := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	recs, _ := forest.Generate(gen, simNow)
	rep.Records = len(recs)
	progress("生成古树", 1, 1, fmt.Sprintf("%d 棵古树", len(recs)))

	// ---------------------------------------------------------------- build
	t0 := time.Now()
	recs = dedupeIDs(recs)
	tree2D, err := rtree.New(rtree.DefaultConfig(2))
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		tree2D.Insert(r.MBR, r)
	}
	rep.BuildStats.InsertionMS = time.Since(t0).Milliseconds()
	progress("插入建树", 1, 1, "2D 逐条插入完成")

	t0 = time.Now()
	tree2DBulk, err := rtree.New(rtree.DefaultConfig(2))
	if err != nil {
		return nil, err
	}
	items := make([]rtree.BulkLoadItem, len(recs))
	for i, r := range recs {
		items[i] = rtree.BulkLoadItem{Rect: r.MBR, Data: r}
	}
	tree2DBulk.BulkLoad(items)
	rep.BuildStats.BulkMS = time.Since(t0).Milliseconds()
	progress("批量建树", 1, 1, "STR 打包建树完成")

	// STR vs insertion on the same query set. Use a deterministic query set so
	// the two trees are compared on identical work.
	qrng := rand.New(rand.NewSource(opt.Seed ^ 0x5EED))
	queries := make([]geom.Rect, 0, 400)
	for i := 0; i < 400; i++ {
		queries = append(queries, randomQueryBox(qrng, recs))
	}
	progress("批量建树对比", 0, len(queries), "STR 与逐条插入对比查询")
	compareQueries(ctx, opt, rep, recs, tree2D, tree2DBulk, queries, progress)

	// ---------------------------------------------------------------- 2D index
	tree3D, err := rtree.New(rtree.DefaultConfig(3))
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		tree3D.Insert(r.MBR3, r)
	}
	progress("三维建树", 1, 1, "日期作为第三维插入完成")

	survey := &forest.Survey{
		Records: append([]*forest.Record(nil), recs...),
		Index:   tree2D, Space2D: tree2D, Dim: 2,
	}
	survey3 := &forest.Survey{
		Records: append([]*forest.Record(nil), recs...),
		Index:   tree3D, Space2D: tree2D, Dim: 3,
	}

	// ---------------------------------------------------------------- mixed ops
	progress("混合操作对拍", 0, opt.Ops, "范围查询 / 最近邻 / 施工红线 / 插入 / 删除")
	mixed := newMixedRun(opt, recs, survey, survey3, simNow, progress)
	if err := mixed.run(ctx); err != nil {
		return nil, err
	}
	rep.OpsRun = mixed.ops
	rep.OpStats = mixed.opCounts
	rep.Failures = append(rep.Failures, mixed.failures...)

	// Structural validation after all the churn.
	for _, c := range []struct {
		name string
		tree *rtree.Tree
	}{{"2D", survey.Index}, {"3D", survey3.Index}} {
		rep.Structure.Checked++
		if probs := c.tree.Validate(); len(probs) > 0 {
			for _, p := range probs {
				rep.Structure.Failures = append(rep.Structure.Failures, c.name+": "+p)
			}
			rep.Failures = append(rep.Failures, Failure{Op: "结构校验", Detail: c.name + " " + probs[0], Extra: fmt.Sprint(len(probs))})
		}
	}

	// ---------------------------------------------------------------- reports
	rep.Index2D = indexReport(survey.Index)
	rep.Index3D = indexReport(survey3.Index)
	st2 := survey.Index.ComputeOverlapStats()
	st3 := survey3.Index.ComputeOverlapStats()
	rep.Index2D.OverlapArea = st2.TotalOverlapArea
	rep.Index2D.CoverArea = st2.TotalCoverArea
	rep.Index3D.OverlapArea = st3.TotalOverlapArea
	rep.Index3D.CoverArea = st3.TotalCoverArea

	// ---------------------------------------------------------------- A/B
	progress("强制重插入对比", 0, 1, "同一数据分别开关强制重插入建树并对比查询")
	ab, abMS, err := runReinsertAB(ctx, opt, recs)
	if err != nil {
		return nil, err
	}
	rep.ReinsertAB = ab
	rep.BuildStats.ABInsertMS = abMS
	progress("强制重插入对比", 1, 1, "对比完成")

	if rep.Structure.Failures == nil {
		rep.Structure.Failures = []string{}
	}
	if rep.Failures == nil {
		rep.Failures = []Failure{}
	}
	rep.Passed = len(rep.Failures) == 0
	rep.DurationMS = time.Since(start).Milliseconds()
	if rep.Passed {
		rep.Message = fmt.Sprintf("全部一致：%d 次操作，%d 项结构校验通过", rep.OpsRun, rep.Structure.Checked)
	} else {
		rep.Message = fmt.Sprintf("发现 %d 处不一致", len(rep.Failures))
	}
	return rep, nil
}

// compareQueries evaluates the same queries on an insertion-built and a
// bulk-built tree and records shape/overlap differences.
func compareQueries(ctx context.Context, opt Options, rep *Report, recs []*forest.Record,
	ins, bulk *rtree.Tree, queries []geom.Rect, progress func(string, int, int, string)) {
	br := BulkReport{}
	br.InsertHeight = ins.Height()
	br.BulkHeight = bulk.Height()
	br.InsertNodes = len(ins.Snapshot(nil).Nodes)
	br.BulkNodes = len(bulk.Snapshot(nil).Nodes)
	ist := ins.ComputeOverlapStats()
	bst := bulk.ComputeOverlapStats()
	br.InsertOverlapArea = ist.TotalOverlapArea
	br.BulkOverlapArea = bst.TotalOverlapArea
	br.InsertLeafFill = ist.FillFactor
	br.BulkLeafFill = bst.FillFactor
	ins.ResetQueryStats()
	bulk.ResetQueryStats()
	for i, q := range queries {
		select {
		case <-ctx.Done():
			rep.Bulk = br
			return
		default:
		}
		a := ins.Search(q, nil)
		b := bulk.Search(q, nil)
		if !sameRecordSet(a.Data, b.Data) {
			rep.Failures = append(rep.Failures, Failure{
				Op: "批量建树对比", Step: i,
				Detail: "STR 树与插入树范围查询结果不同",
				Want:   len(a.Data), Got: len(b.Data),
			})
		}
		br.QueriesCompared++
		if i%50 == 0 {
			progress("批量建树对比", i, len(queries), "STR 与逐条插入对比查询")
		}
	}
	br.InsertAvgNodes = ins.Stats.Snapshot().AvgNodesPerQuery()
	br.BulkAvgNodes = bulk.Stats.Snapshot().AvgNodesPerQuery()
	br.BulkValid = len(bulk.Validate()) == 0
	rep.Bulk = br
}

// dedupeIDs guards the harness itself: every record added to a tree must have
// a unique ID, otherwise set comparisons could hide duplicate tree entries.
func dedupeIDs(recs []*forest.Record) []*forest.Record {
	seen := make(map[int]bool, len(recs))
	out := recs[:0]
	for _, r := range recs {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	return out
}

func indexReport(t *rtree.Tree) IndexReport {
	st := t.ComputeOverlapStats()
	sn := t.Stats.Snapshot()
	return IndexReport{
		Height:           st.Height,
		Nodes:            st.Nodes,
		Leaves:           st.Leaves,
		AvgNodesPerQuery: sn.AvgNodesPerQuery(),
		AvgLeafPerQuery:  sn.AvgLeafEntriesPerQuery(),
		Queries:          sn.Queries,
	}
}

// CompareReinsertion is the exported A/B helper used by the page.
func CompareReinsertion(recs []*forest.Record, seed int64, queries int) (ReinsertAB, int64) {
	opt := Options{Seed: seed}
	ab, ms, _ := runReinsertAB(context.Background(), opt, recs)
	_ = queries
	return ab, ms
}

// runReinsertAB builds the same records twice, once with forced reinsertion
// and once without, then runs the same deterministic range queries on both.
// Per-query answers are compared with each other (not with brute force: the
// main mixed run already proves correctness against brute force). This keeps
// the A/B pass linear in the number of queries.
func runReinsertAB(ctx context.Context, opt Options, recs []*forest.Record) (ReinsertAB, int64, error) {
	mk := func(on bool) *rtree.Tree {
		cfg := rtree.DefaultConfig(2)
		cfg.ForcedReinsert = on
		t, _ := rtree.New(cfg)
		return t
	}
	t0 := time.Now()
	on := mk(true)
	for _, r := range recs {
		on.Insert(r.MBR, r)
	}
	off := mk(false)
	for _, r := range recs {
		off.Insert(r.MBR, r)
	}
	ms := time.Since(t0).Milliseconds()

	rng := rand.New(rand.NewSource(opt.Seed ^ 0xAB))
	const qn = 3000
	queries := make([]geom.Rect, qn)
	for i := range queries {
		queries[i] = randomQueryBox(rng, recs)
	}
	var ab ReinsertAB
	ab.Queries = qn
	ab.SameAnswers = true
	on.ResetQueryStats()
	off.ResetQueryStats()
	for i, q := range queries {
		select {
		case <-ctx.Done():
			return ab, ms, ctx.Err()
		default:
		}
		ra := on.Search(q, nil)
		rb := off.Search(q, nil)
		if !sameRecordSet(ra.Data, rb.Data) {
			ab.SameAnswers = false
		}
		_ = i
	}
	onSnap := on.Stats.Snapshot()
	offSnap := off.Stats.Snapshot()
	ab.OnAvgNodes = onSnap.AvgNodesPerQuery()
	ab.OffAvgNodes = offSnap.AvgNodesPerQuery()
	ab.OnAvgLeaf = onSnap.AvgLeafEntriesPerQuery()
	ab.OffAvgLeaf = offSnap.AvgLeafEntriesPerQuery()
	ons := on.ComputeOverlapStats()
	offs := off.ComputeOverlapStats()
	ab.OnOverlapArea = ons.TotalOverlapArea
	ab.OffOverlapArea = offs.TotalOverlapArea
	ab.OnCoverArea = ons.TotalCoverArea
	ab.OffCoverArea = offs.TotalCoverArea
	ab.OnSplits = onSnap.Splits
	ab.OffSplits = offSnap.Splits
	ab.OnReinsertOps = onSnap.ReinsertOps
	ab.OnReinserted = onSnap.ReinsertedEntries
	ab.OnHeight = on.Height()
	ab.OffHeight = off.Height()
	if ab.OffAvgNodes > 0 {
		ab.NodesImprovement = 100 * (ab.OffAvgNodes - ab.OnAvgNodes) / ab.OffAvgNodes
	}
	return ab, ms, nil
}

// mixedRun owns the mutable state of the mixed workload.
type mixedRun struct {
	opt      Options
	recs     []*forest.Record // the live records (insertions append)
	byID     map[int]*forest.Record
	dead     map[int]bool
	live     []*forest.Record // tombstones removed by swap-delete
	survey   *forest.Survey
	survey3  *forest.Survey
	rng      *rand.Rand
	ops      int
	opCounts map[string]int
	failures []Failure
	progress func(string, int, int, string)
	simNow   time.Time
	nextID   int
	// deleted records kept for possible reinsertion
	deleted []*forest.Record
}

func newMixedRun(opt Options, recs []*forest.Record, s2, s3 *forest.Survey, simNow time.Time, progress func(string, int, int, string)) *mixedRun {
	m := &mixedRun{
		opt: opt, recs: recs, byID: make(map[int]*forest.Record, len(recs)),
		dead: make(map[int]bool), survey: s2, survey3: s3, simNow: simNow,
		live:     append([]*forest.Record(nil), recs...),
		rng:      rand.New(rand.NewSource(opt.Seed ^ 0xC0FFEE)),
		opCounts: map[string]int{}, progress: progress,
		nextID: len(recs),
	}
	for _, r := range recs {
		m.byID[r.ID] = r
	}
	return m
}

func (m *mixedRun) fail(f Failure) {
	m.failures = append(m.failures, f)
}

func (m *mixedRun) run(ctx context.Context) error {
	total := m.opt.Ops
	for m.ops < total {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if m.ops%2000 == 0 {
			m.progress("混合操作对拍", m.ops, total, fmt.Sprintf("已完成 %d 次操作，%d 处不一致", m.ops, len(m.failures)))
		}
		m.step()
		m.ops++
		if len(m.failures) > 20 {
			// Stop early but keep the report informative.
			m.progress("混合操作对拍", m.ops, total, "不一致过多，提前停止")
			return nil
		}
	}
	m.progress("混合操作对拍", total, total, "混合操作完成")
	return nil
}

// step dispatches one operation. The operation mix is fixed:
//
//	45% range queries (2D and 3D)
//	20% k-nearest and streaming nearest
//	10% construction red lines (rect + polygon, exact crown test)
//	15% delete
//	10% insert (including reinsertion of previously deleted trees)
func (m *mixedRun) step() {
	u := m.rng.Float64()
	switch {
	case u < 0.24:
		m.opRange2D()
	case u < 0.45:
		m.opRange3D()
	case u < 0.57:
		m.opKNN()
	case u < 0.65:
		m.opStream()
	case u < 0.75:
		m.opRedLineRect()
	case u < 0.82:
		m.opRedLinePolygon()
	case u < 0.94:
		m.opDelete()
	default:
		m.opInsert()
	}
}

func (m *mixedRun) randomQueryBox() geom.Rect { return randomQueryBox(m.rng, m.recs) }

func (m *mixedRun) opRange2D() {
	m.opCounts["range2d"]++
	q := m.randomQueryBox()
	got := m.survey.Index.SearchRect(q)
	want := bruteRange(m.live, q)
	if !sameRecordSet(got, recsToAny(want)) {
		m.fail(Failure{Op: "2D范围查询", Step: m.ops, Detail: fmt.Sprintf("query=%v", q),
			Want: len(want), Got: len(got), Extra: countDiff(got, want)})
	}
}

func (m *mixedRun) opRange3D() {
	m.opCounts["range3d"]++
	box2 := m.randomQueryBox()
	var q geom.Rect
	if m.rng.Float64() < 0.5 {
		// A time slice: trees inspected within a 90-day window.
		day := int64(24 * 3600 * 1000)
		now := m.simNow.UnixMilli()
		from := now - int64(m.rng.Float64()*float64(m.opt.DaysBack))*day
		q = forest.QueryBox3D(box2, from, from+90*day)
	} else {
		q = forest.QueryBox3D(box2, math.MinInt64/2, math.MaxInt64/2)
	}
	got := m.survey3.Index.SearchRect(q)
	want := bruteRange3D(m.live, q)
	if !sameRecordSet(got, recsToAny(want)) {
		m.fail(Failure{Op: "3D范围查询", Step: m.ops, Detail: fmt.Sprintf("query=%v", q),
			Want: len(want), Got: len(got), Extra: countDiff(got, want)})
	}
}

func (m *mixedRun) opKNN() {
	m.opCounts["knn"]++
	p := randomPointNear(m.rng, m.recs)
	k := 1 + m.rng.Intn(25)
	got := m.survey.NearestK(p, k)
	want := bruteKNN(m.live, p, k)
	if !sameNearest(got, want, 1e-9) {
		m.fail(Failure{Op: "最近邻", Step: m.ops,
			Detail: fmt.Sprintf("p=(%.2f,%.2f) k=%d", p.X, p.Y, k),
			Want:   len(want), Got: len(got), Extra: nearestDiff(got, want)})
	}
}

func (m *mixedRun) opStream() {
	m.opCounts["stream"]++
	p := randomPointNear(m.rng, m.recs)
	limit := 1 + m.rng.Intn(40)
	got := m.survey.NearestStream(p, limit)
	want := bruteKNN(m.live, p, limit)
	if !sameNearest(got, want, 1e-9) {
		m.fail(Failure{Op: "递增最近邻", Step: m.ops,
			Detail: fmt.Sprintf("p=(%.2f,%.2f) limit=%d", p.X, p.Y, limit),
			Want:   len(want), Got: len(got), Extra: nearestDiff(got, want)})
	}
	// The stream must be non-decreasing in distance.
	for i := 1; i < len(got); i++ {
		if got[i].Dist < got[i-1].Dist-1e-9 {
			m.fail(Failure{Op: "递增最近邻", Step: m.ops, Detail: "距离非递增", Want: i - 1, Got: i})
			break
		}
	}
}

func (m *mixedRun) opRedLineRect() {
	m.opCounts["redline_rect"]++
	q := m.randomQueryBox()
	// Grow the box into a construction site rectangle (not degenerate).
	red := geom.NewRectFromBounds(
		[]float64{q.Min(0) - 40, q.Min(1) - 40},
		[]float64{q.Max(0) + 80, q.Max(1) + 80},
	)
	got := m.survey.CrownsIntersecting(red)
	want := bruteCrowns(m.live, red)
	if !sameRecordSlice(got, want) {
		m.fail(Failure{Op: "施工红线(矩形)", Step: m.ops, Detail: fmt.Sprintf("red=%v", red),
			Want: len(want), Got: len(got), Extra: countDiff(recsToAny(got), want)})
	}
}

func (m *mixedRun) opRedLinePolygon() {
	m.opCounts["redline_polygon"]++
	p := randomPointNear(m.rng, m.recs)
	w := 30 + m.rng.Float64()*400
	h := 30 + m.rng.Float64()*400
	red := forest.GenerateRedLine(m.rng, p.X, p.Y, w, h, m.rng.Float64()*math.Pi)
	got := m.survey.CrownsIntersectingPolygon(red)
	want := bruteCrownPolygon(m.live, red)
	if !sameRecordSlice(got, want) {
		m.fail(Failure{Op: "施工红线(多边形)", Step: m.ops,
			Detail: fmt.Sprintf("centre=(%.2f,%.2f) w=%.1f h=%.1f", p.X, p.Y, w, h),
			Want:   len(want), Got: len(got), Extra: countDiff(recsToAny(got), want)})
	}
}

func (m *mixedRun) opDelete() {
	m.opCounts["delete"]++
	if len(m.dead) > len(m.byID)*8/10 || len(m.byID) == 0 {
		m.opInsert()
		return
	}
	// Pick a live record uniformly.
	var target *forest.Record
	// Rejection sampling is fine at this occupancy.
	for tries := 0; tries < 40; tries++ {
		r := m.recs[m.rng.Intn(len(m.recs))]
		if !m.dead[r.ID] {
			target = r
			break
		}
	}
	if target == nil {
		for id, r := range m.byID {
			if !m.dead[id] {
				target = r
				break
			}
		}
	}
	if target == nil {
		return
	}
	ok2 := m.survey.Index.DeleteFirst(target.MBR, func(d any) bool { return d.(*forest.Record).ID == target.ID })
	ok3 := m.survey3.Index.DeleteFirst(target.MBR3, func(d any) bool { return d.(*forest.Record).ID == target.ID })
	if !ok2 || !ok3 {
		m.fail(Failure{Op: "删除", Step: m.ops, Detail: fmt.Sprintf("id=%d", target.ID), Want: 1, Got: 0})
	}
	m.dead[target.ID] = true
	for i, r := range m.live {
		if r == target {
			m.live[i] = m.live[len(m.live)-1]
			m.live = m.live[:len(m.live)-1]
			break
		}
	}
	m.deleted = append(m.deleted, target)
}

func (m *mixedRun) opInsert() {
	m.opCounts["insert"]++
	var r *forest.Record
	if len(m.deleted) > 0 && m.rng.Float64() < 0.6 {
		// Reinsert a previously deleted tree at a new location/date, as a
		// resurveyed tree would be.
		i := m.rng.Intn(len(m.deleted))
		r = m.deleted[i]
		m.deleted = append(m.deleted[:i], m.deleted[i+1:]...)
		// Nudge position and inspection date.
		nx := r.X + m.rng.NormFloat64()*2
		ny := r.Y + m.rng.NormFloat64()*2
		nb := r.Crown.BoundingRect()
		dx := nx - (nb.Min(0)+nb.Max(0))/2
		dy := ny - (nb.Min(1)+nb.Max(1))/2
		ring := make([]geom.Point, len(r.Crown.Ring))
		for j, pt := range r.Crown.Ring {
			ring[j] = geom.Point{X: pt.X + dx, Y: pt.Y + dy}
		}
		r.Crown = geom.NewPolygon(ring)
		r.X, r.Y = nx, ny
		r.MBR = r.Crown.BoundingRect()
		r.InspectAt += int64(m.rng.Int63n(30 * 24 * 3600 * 1000))
		z := forest.DateAxis(r.InspectAt)
		r.MBR3 = geom.NewRectFromBounds(
			[]float64{r.MBR.Min(0), r.MBR.Min(1), z},
			[]float64{r.MBR.Max(0), r.MBR.Max(1), z},
		)
		m.dead[r.ID] = false
		m.live = append(m.live, r)
	} else {
		// A newly registered tree near an existing cluster.
		src := m.recs[m.rng.Intn(len(m.recs))]
		nx := src.X + m.rng.NormFloat64()*60
		ny := src.Y + m.rng.NormFloat64()*60
		crown := geom.RandomConvexPolygon(m.rng, geom.Point{X: nx, Y: ny}, 5+m.rng.Intn(5), 1.5, 9.5)
		insp := m.simNow.Add(-time.Duration(m.rng.Int63n(int64(optDays(m)) * 24 * int64(time.Hour)))).UnixMilli()
		r = &forest.Record{
			ID: m.nextID, Code: fmt.Sprintf("京古新%05d", m.nextID), X: nx, Y: ny,
			Crown: crown, MBR: crown.BoundingRect(), InspectAt: insp,
			Height: 10 + m.rng.Float64()*10, DBH: 30 + m.rng.Float64()*50,
			Age:     100 + m.rng.Float64()*500,
			Species: forest.Species[m.rng.Intn(len(forest.Species))],
			Health:  forest.Health[m.rng.Intn(len(forest.Health))],
			Kind:    forest.Kinds[0], Sampled: m.rng.Float64() < 0.35,
		}
		z := forest.DateAxis(r.InspectAt)
		r.MBR3 = geom.NewRectFromBounds(
			[]float64{r.MBR.Min(0), r.MBR.Min(1), z},
			[]float64{r.MBR.Max(0), r.MBR.Max(1), z},
		)
		m.nextID++
		m.byID[r.ID] = r
		m.recs = append(m.recs, r)
		m.live = append(m.live, r)
		m.dead[r.ID] = false
	}
	m.survey.Index.Insert(r.MBR, r)
	m.survey3.Index.Insert(r.MBR3, r)
	m.survey.Records = append(m.survey.Records, r)
	m.survey3.Records = append(m.survey3.Records, r)
}

func optDays(m *mixedRun) int {
	if m.opt.DaysBack > 0 {
		return m.opt.DaysBack
	}
	return 365
}

// ---------------------------------------------------------------- helpers

func recsToAny(recs []*forest.Record) []any {
	out := make([]any, len(recs))
	for i, r := range recs {
		out[i] = r
	}
	return out
}

func sameRecordSet(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int]int, len(a))
	for _, d := range a {
		seen[d.(*forest.Record).ID]++
	}
	for _, d := range b {
		id := d.(*forest.Record).ID
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}
	return true
}

// sameRecordSlice compares two record slices as sets; the index returns
// records in tree order while brute force returns them in slice order.
func sameRecordSlice(a, b []*forest.Record) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int]bool, len(a))
	for _, r := range a {
		if seen[r.ID] {
			return false
		}
		seen[r.ID] = true
	}
	for _, r := range b {
		if !seen[r.ID] {
			return false
		}
	}
	return true
}

func sameNearest(a, b []forest.NearestRecord, eps float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Record.ID != b[i].Record.ID {
			return false
		}
		if math.Abs(a[i].Dist-b[i].Dist) > eps {
			return false
		}
	}
	return true
}

func countDiff(got []any, want []*forest.Record) string {
	gotSet := make(map[int]bool, len(got))
	for _, d := range got {
		gotSet[d.(*forest.Record).ID] = true
	}
	wantSet := make(map[int]bool, len(want))
	for _, r := range want {
		wantSet[r.ID] = true
	}
	missing := 0
	for id := range wantSet {
		if !gotSet[id] {
			missing++
		}
	}
	extra := 0
	for id := range gotSet {
		if !wantSet[id] {
			extra++
		}
	}
	return fmt.Sprintf("缺少%d 多余%d", missing, extra)
}

func nearestDiff(got, want []forest.NearestRecord) string {
	if len(got) != len(want) {
		return fmt.Sprintf("长度 %d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Record.ID != want[i].Record.ID {
			return fmt.Sprintf("第%d个: id %d vs %d (距离 %.6f vs %.6f)",
				i, got[i].Record.ID, want[i].Record.ID, got[i].Dist, want[i].Dist)
		}
	}
	return ""
}

func randomQueryBox(rng *rand.Rand, recs []*forest.Record) geom.Rect {
	// Half the queries are anchored on a random tree (so they contain
	// results), half are uniform city-wide boxes.
	var cx, cy float64
	if rng.Float64() < 0.5 && len(recs) > 0 {
		r := recs[rng.Intn(len(recs))]
		cx, cy = r.X, r.Y
	} else {
		cx = (rng.Float64()*2 - 1) * 15000
		cy = (rng.Float64()*2 - 1) * 15000
	}
	var w, h float64
	switch rng.Intn(4) {
	case 0: // 100 m neighbourhood, the inspector's query
		w = 100 + rng.Float64()*50
		h = w
	case 1: // park-sized
		w = 300 + rng.Float64()*500
		h = 300 + rng.Float64()*500
	case 2: // district-sized
		w = 1000 + rng.Float64()*2000
		h = 1000 + rng.Float64()*2000
	default: // tiny, often empty
		w = 5 + rng.Float64()*30
		h = 5 + rng.Float64()*30
	}
	return geom.NewRectFromBounds(
		[]float64{cx - w/2, cy - h/2},
		[]float64{cx + w/2, cy + h/2},
	)
}

// randomPointNear picks a point near a tree (inspector standing by a tree) or
// anywhere in the city.
func randomPointNear(rng *rand.Rand, recs []*forest.Record) geom.Point {
	if len(recs) == 0 || rng.Float64() < 0.3 {
		return geom.Point{X: (rng.Float64()*2 - 1) * 15000, Y: (rng.Float64()*2 - 1) * 15000}
	}
	r := recs[rng.Intn(len(recs))]
	// Within ~100 m of the tree.
	if rng.Float64() < 0.6 {
		return geom.Point{X: r.X + rng.NormFloat64()*40, Y: r.Y + rng.NormFloat64()*40}
	}
	return geom.Point{X: r.X, Y: r.Y}
}

// ---------------------------------------------------------------- brute force
//
// These are the reference implementations the index is checked against. Each
// one examines every live tree; the only optimisation is to skip the exact
// polygon test when an MBR test already proves the answer.

func bruteRange(live []*forest.Record, q geom.Rect) []*forest.Record {
	out := make([]*forest.Record, 0, 16)
	for _, r := range live {
		if r.MBR.Intersects(q) {
			out = append(out, r)
		}
	}
	return out
}

func bruteRange3D(live []*forest.Record, q geom.Rect) []*forest.Record {
	out := make([]*forest.Record, 0, 16)
	for _, r := range live {
		if r.MBR3.Intersects(q) {
			out = append(out, r)
		}
	}
	return out
}

func bruteCrowns(live []*forest.Record, red geom.Rect) []*forest.Record {
	out := make([]*forest.Record, 0, 16)
	for _, r := range live {
		if r.MBR.Intersects(red) && r.Crown.IntersectsRect(red) {
			out = append(out, r)
		}
	}
	return out
}

func bruteCrownPolygon(live []*forest.Record, red geom.Polygon) []*forest.Record {
	rb := red.BoundingRect()
	out := make([]*forest.Record, 0, 16)
	for _, r := range live {
		if r.MBR.Intersects(rb) && r.Crown.IntersectsPolygon(red) {
			out = append(out, r)
		}
	}
	return out
}

// bruteKNN scans every tree, pruning the polygon test with the MBR lower
// bound against the current k-th best exact distance.
func bruteKNN(live []*forest.Record, p geom.Point, k int) []forest.NearestRecord {
	if k <= 0 || len(live) == 0 {
		return nil
	}
	type cand struct {
		rec  *forest.Record
		dist float64
	}
	best := make([]cand, 0, k)
	worst := math.Inf(1)
	pt := geom.Point{X: p.X, Y: p.Y}
	for _, r := range live {
		if len(best) == k {
			mb := math.Sqrt(geom.DistToRectSq(pt, r.MBR))
			if mb > worst {
				continue
			}
		}
		d := geom.DistanceToPolygon(p, r.Crown)
		if len(best) == k && d > worst {
			continue
		}
		// Insert sorted by (distance, id).
		idx := sort.Search(len(best), func(i int) bool {
			if best[i].dist != d {
				return best[i].dist > d
			}
			return best[i].rec.ID > r.ID
		})
		best = append(best, cand{})
		copy(best[idx+1:], best[idx:])
		best[idx] = cand{r, d}
		if len(best) > k {
			best = best[:k]
		}
		if len(best) == k {
			worst = best[k-1].dist
		}
	}
	out := make([]forest.NearestRecord, len(best))
	for i, c := range best {
		out[i] = forest.NearestRecord{Record: c.rec, Dist: c.dist}
	}
	return out
}
