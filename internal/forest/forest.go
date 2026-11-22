// Package forest models a city's ancient-tree survey: records, generated
// data, and the R*-tree adapter used by the survey tool and the verification
// harness.
package forest

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"ancienttrees/internal/geom"
	"ancienttrees/internal/rtree"
)

// Species and health values are fixed vocabularies so labels are comparable.
var (
	Species = []string{"银杏", "国槐", "香樟", "侧柏", "皂荚", "榆树", "榕树", "枫香", "朴树", "黄连木"}
	Health  = []string{"旺盛", "一般", "衰弱", "濒危"}
	Kinds   = []string{"登记树", "后备资源", "古树群"}
)

// DayMillis is one day in milliseconds.
const DayMillis = 24 * 3600 * 1000

// DateAxisScale is how many index units one day occupies on the third axis.
//
// A raw millisecond axis is ~9 orders of magnitude larger than the metre
// axes (a 6-year span is 1.9e11 ms vs a 40 km city), so R*'s margin/area
// criteria would be dominated by time and the spatial clustering would be
// destroyed. One day = 40 units keeps the axes comparable: the six-year
// inspection window is ~87,600 units against a ~40,000 m city, and a 90-day
// time filter is 3,600 units. The unit is arbitrary; callers convert with
// DateAxis and DateRangeAxis.
const DateAxisScale = 40.0

// DateAxis converts a unix-millisecond instant to the third-axis coordinate.
func DateAxis(unixMs int64) float64 {
	return float64(unixMs) / DayMillis * DateAxisScale
}

// DateRangeAxis converts a [from,to] unix-millisecond interval to axis units.
func DateRangeAxis(fromMs, toMs int64) (float64, float64) {
	return DateAxis(fromMs), DateAxis(toMs)
}

// Record is one numbered ancient tree.
type Record struct {
	ID        int
	Code      string  // 挂牌编号
	District  int     // generated cluster id
	X, Y      float64 // centre in metres
	Crown     geom.Polygon
	MBR       geom.Rect // 2D crown bounding box
	MBR3      geom.Rect // crown MBR extruded along the inspection-date axis
	Height    float64
	DBH       float64
	Age       float64
	Species   string
	Health    string
	Kind      string
	Sampled   bool  // 巡查抽样点
	InspectAt int64 // last inspection, unix millis
}

// Survey is the queryable collection of records plus its index.
//
// Index has the configured dimension and answers range queries (in 3D a query
// may constrain the inspection-date axis). Space2D is always a planar index
// over the same records' crown MBRs: crown intersection and nearest-neighbour
// queries are planar by definition, so they must not be routed through the
// date axis.
type Survey struct {
	Records []*Record
	Index   *rtree.Tree
	Space2D *rtree.Tree
	Dim     int
}

// NewSurvey wraps records in a survey whose range index has the given
// dimension. The planar index is always created alongside it.
func NewSurvey(records []*Record, dim int) (*Survey, error) {
	if dim != 2 && dim != 3 {
		return nil, fmt.Errorf("forest: unsupported dimension %d", dim)
	}
	tree, err := rtree.New(rtree.DefaultConfig(dim))
	if err != nil {
		return nil, err
	}
	space, err := rtree.New(rtree.DefaultConfig(2))
	if err != nil {
		return nil, err
	}
	return &Survey{Records: records, Index: tree, Space2D: space, Dim: dim}, nil
}

// InsertRecord adds one record to both indexes.
func (s *Survey) InsertRecord(r *Record) {
	s.Index.Insert(s.Rect(r), r)
	s.Space2D.Insert(r.MBR, r)
}

// DeleteRecord removes one record from both indexes. It reports whether the
// range index held it.
func (s *Survey) DeleteRecord(r *Record) bool {
	ok := s.Index.DeleteFirst(s.Rect(r), func(d any) bool { return d.(*Record).ID == r.ID })
	s.Space2D.DeleteFirst(r.MBR, func(d any) bool { return d.(*Record).ID == r.ID })
	return ok
}

// BulkLoadRecords rebuilds both indexes from Records with STR packing.
func (s *Survey) BulkLoadRecords() {
	items := make([]rtree.BulkLoadItem, len(s.Records))
	items2 := make([]rtree.BulkLoadItem, len(s.Records))
	for i, r := range s.Records {
		items[i] = rtree.BulkLoadItem{Rect: s.Rect(r), Data: r}
		items2[i] = rtree.BulkLoadItem{Rect: r.MBR, Data: r}
	}
	s.Index.BulkLoad(items)
	s.Space2D.BulkLoad(items2)
}

// Rect2D returns the planar crown MBR.
func (r *Record) Rect2D() geom.Rect { return r.MBR }

// Rect3D returns the crown MBR extruded along the inspection-date axis.
func (r *Record) Rect3D() geom.Rect { return r.MBR3 }

// Rect returns the index extent of the record for this survey's dimension.
func (s *Survey) Rect(r *Record) geom.Rect {
	if s.Dim == 3 {
		return r.Rect3D()
	}
	return r.MBR
}

// Add appends records and inserts them into both indexes.
func (s *Survey) Add(recs ...*Record) {
	for _, r := range recs {
		s.Records = append(s.Records, r)
		s.InsertRecord(r)
	}
}

// BulkLoad rebuilds both indexes from the current records using STR packing.
func (s *Survey) BulkLoad() { s.BulkLoadRecords() }

// Build2D builds a 2D index over the records' crown MBRs.
func Build2D(recs []*Record) (*rtree.Tree, error) {
	t, err := rtree.New(rtree.DefaultConfig(2))
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		t.Insert(r.MBR, r)
	}
	return t, nil
}

// Build3D builds a 3D index whose third axis is the inspection time in
// milliseconds. Entries are unit slabs in the time axis so a query box with a
// time range behaves as expected.
func Build3D(recs []*Record) (*rtree.Tree, error) {
	t, err := rtree.New(rtree.DefaultConfig(3))
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		t.Insert(r.Rect3D(), r)
	}
	return t, nil
}

// Bulk2D bulk-loads a standalone 2D tree.
func Bulk2D(recs []*Record) (*rtree.Tree, error) {
	t, err := rtree.New(rtree.DefaultConfig(2))
	if err != nil {
		return nil, err
	}
	items := make([]rtree.BulkLoadItem, len(recs))
	for i, r := range recs {
		items[i] = rtree.BulkLoadItem{Rect: r.MBR, Data: r}
	}
	t.BulkLoad(items)
	return t, nil
}

// Bulk3D bulk-loads a standalone 3D tree.
func Bulk3D(recs []*Record) (*rtree.Tree, error) {
	t, err := rtree.New(rtree.DefaultConfig(3))
	if err != nil {
		return nil, err
	}
	items := make([]rtree.BulkLoadItem, len(recs))
	for i, r := range recs {
		items[i] = rtree.BulkLoadItem{Rect: r.Rect3D(), Data: r}
	}
	t.BulkLoad(items)
	return t, nil
}

// GenConfig controls the synthetic survey generator.
type GenConfig struct {
	N         int
	Clusters  int
	Seed      int64
	DaysBack  int     // inspection dates lie in [now-DaysBack, now]
	SampledFr float64 // fraction of records with crown sampling
}

// DefaultGenConfig returns a realistic city: ~45k trees in 55 urban clusters.
func DefaultGenConfig(seed int64) GenConfig {
	return GenConfig{N: 45000, Clusters: 55, Seed: seed, DaysBack: 365 * 6, SampledFr: 0.35}
}

// Cluster is a generated urban agglomeration.
type Cluster struct {
	Center geom.Point
	Radius float64
	Name   string
}

// Generate produces a synthetic city of ancient trees. Trees are clustered
// around urban centres (old town, parks, river banks) with a few percent of
// outliers, and crowns are random convex polygons of 5..9 vertices with radii
// 1.5..9.5 m. All randomness derives from the seed.
func Generate(cfg GenConfig, now time.Time) ([]*Record, []Cluster) {
	rng := rand.New(rand.NewSource(cfg.Seed))
	if cfg.N <= 0 {
		return nil, nil
	}
	nc := cfg.Clusters
	if nc < 1 {
		nc = 1
	}
	clusters := make([]Cluster, nc)
	for i := range clusters {
		// City grows around the origin with a dense old core.
		ang := rng.Float64() * 2 * math.Pi
		rad := math.Pow(rng.Float64(), 0.7) * 14000
		clusters[i] = Cluster{
			Center: geom.Point{X: rad * math.Cos(ang), Y: rad * math.Sin(ang)},
			Radius: 260 + rng.Float64()*900,
			Name:   fmt.Sprintf("片区%02d", i+1),
		}
	}
	// Cluster weights follow a Zipf-like law so a few old districts are
	// much denser than the rest.
	weights := make([]float64, nc)
	var wsum float64
	for i := range weights {
		weights[i] = 1 / math.Pow(float64(i+1), 0.9)
		wsum += weights[i]
	}
	cum := make([]float64, nc)
	var acc float64
	for i, w := range weights {
		acc += w / wsum
		cum[i] = acc
	}
	pickCluster := func() int {
		u := rng.Float64()
		idx := sort.SearchFloat64s(cum, u)
		if idx >= nc {
			idx = nc - 1
		}
		return idx
	}
	daysBack := cfg.DaysBack
	if daysBack <= 0 {
		daysBack = 365
	}
	nowMs := now.UnixMilli()
	recs := make([]*Record, 0, cfg.N)
	for i := 0; i < cfg.N; i++ {
		var p geom.Point
		district := pickCluster()
		if rng.Float64() < 0.02 {
			// Outlier: a lone tree in the countryside.
			p = geom.Point{X: (rng.Float64()*2 - 1) * 26000, Y: (rng.Float64()*2 - 1) * 26000}
			district = -1
		} else {
			c := clusters[district]
			// Gaussian-ish displacement inside the cluster, truncated at 2.2r.
			var p2 geom.Point
			for tries := 0; tries < 8; tries++ {
				gx := rng.NormFloat64() * c.Radius * 0.45
				gy := rng.NormFloat64() * c.Radius * 0.45
				if gx*gx+gy*gy < (2.2*c.Radius)*(2.2*c.Radius) {
					p2 = geom.Point{X: c.Center.X + gx, Y: c.Center.Y + gy}
					break
				}
			}
			p = p2
		}
		// Some trees in a cluster share a hutong-like line; the Gaussian
		// spread already approximates this. Street-tree look: snap some trees
		// onto an east-west line with jitter.
		if district >= 0 && rng.Float64() < 0.18 {
			c := clusters[district]
			p.Y = c.Center.Y + (p.Y-c.Center.Y)*0.15
		}
		nv := 5 + rng.Intn(5)
		crown := geom.RandomConvexPolygon(rng, p, nv, 1.5, 9.5)
		// Height / DBH / age loosely correlated with crown size, with tails.
		area := crown.Area()
		height := 9 + math.Sqrt(area)*0.75 + rng.NormFloat64()*2.2
		if height < 4 {
			height = 4
		}
		dbh := 30 + math.Sqrt(area)*3.1 + rng.NormFloat64()*8
		if dbh < 12 {
			dbh = 12
		}
		age := 100 + math.Sqrt(area)*14 + rng.NormFloat64()*95
		if age < 60 {
			age = 60 + rng.Float64()*30
		}
		if rng.Float64() < 0.10 {
			age += 300 + rng.Float64()*600
		}
		kind := Kinds[0]
		if rng.Float64() < 0.16 {
			kind = Kinds[1]
		} else if rng.Float64() < 0.05 {
			kind = Kinds[2]
		}
		healthy := Health[0]
		u := rng.Float64()
		switch {
		case u < 0.58:
			healthy = Health[0]
		case u < 0.86:
			healthy = Health[1]
		case u < 0.97:
			healthy = Health[2]
		default:
			healthy = Health[3]
		}
		inspectMs := nowMs - int64(rng.Float64()*float64(daysBack)*24*3600*1000)
		mbr := crown.BoundingRect()
		z := DateAxis(inspectMs)
		mbr3 := geom.NewRectFromBounds(
			[]float64{mbr.Min(0), mbr.Min(1), z},
			[]float64{mbr.Max(0), mbr.Max(1), z},
		)
		recs = append(recs, &Record{
			ID:        i,
			Code:      fmt.Sprintf("京古%05d", i+1),
			District:  district,
			X:         p.X,
			Y:         p.Y,
			Crown:     crown,
			MBR:       mbr,
			MBR3:      mbr3,
			Height:    math.Round(height*10) / 10,
			DBH:       math.Round(dbh*10) / 10,
			Age:       math.Round(age),
			Species:   Species[rng.Intn(len(Species))],
			Health:    healthy,
			Kind:      kind,
			Sampled:   rng.Float64() < cfg.SampledFr,
			InspectAt: inspectMs,
		})
	}
	return recs, clusters
}

// GenerateConstructionSites produces n random rectangular construction red
// lines (axis-aligned or slightly rotated) over the city extent.
func GenerateConstructionSites(rng *rand.Rand, n int, recs []*Record) []geom.Rect {
	if len(recs) == 0 || n <= 0 {
		return nil
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, r := range recs {
		minX = math.Min(minX, r.MBR.Min(0))
		minY = math.Min(minY, r.MBR.Min(1))
		maxX = math.Max(maxX, r.MBR.Max(0))
		maxY = math.Max(maxY, r.MBR.Max(1))
	}
	out := make([]geom.Rect, 0, n)
	for i := 0; i < n; i++ {
		// Sizes range from a small works yard to a city block.
		w := 20 + rng.Float64()*rng.Float64()*900
		h := 20 + rng.Float64()*rng.Float64()*900
		// Bias towards clusters half the time so queries find trees.
		var cx, cy float64
		if rng.Float64() < 0.7 {
			r := recs[rng.Intn(len(recs))]
			cx = r.X + rng.NormFloat64()*120
			cy = r.Y + rng.NormFloat64()*120
		} else {
			cx = minX + rng.Float64()*(maxX-minX)
			cy = minY + rng.Float64()*(maxY-minY)
		}
		out = append(out, geom.NewRectFromBounds(
			[]float64{cx - w/2, cy - h/2},
			[]float64{cx + w/2, cy + h/2},
		))
	}
	return out
}

// NearestRecord is a tree whose crown polygon (not merely MBR) is within a
// distance of the query point.
type NearestRecord struct {
	Record *Record
	Dist   float64
}

// TreesWithin returns trees whose crown polygon is within r metres of p,
// sorted by distance then id, using the index MBR pass first.
func (s *Survey) TreesWithin(p geom.Point, r float64) []NearestRecord {
	type cand struct {
		rec  *Record
		dist float64
	}
	var cands []cand
	box := geom.NewRectFromBounds(
		[]float64{p.X - r, p.Y - r},
		[]float64{p.X + r, p.Y + r},
	)
	for _, d := range s.Space2D.SearchRect(box) {
		rec := d.(*Record)
		// MBR distance prefilter, then exact polygon distance.
		if math.Sqrt(geom.DistToRectSq(p, rec.MBR)) > r {
			continue
		}
		dist := geom.DistanceToPolygon(p, rec.Crown)
		if dist <= r {
			cands = append(cands, cand{rec, dist})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].rec.ID < cands[j].rec.ID
	})
	out := make([]NearestRecord, len(cands))
	for i, c := range cands {
		out[i] = NearestRecord{Record: c.rec, Dist: c.dist}
	}
	return out
}

// CrownsIntersecting returns trees whose crown polygons intersect the red
// line rect, exact-tested after the MBR pass.
func (s *Survey) CrownsIntersecting(red geom.Rect) []*Record {
	var out []*Record
	for _, d := range s.Space2D.SearchRect(red) {
		rec := d.(*Record)
		if rec.Crown.IntersectsRect(red) {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Filter2D narrows a 3D tree search result set down to records whose planar
// MBR intersects the 2D box.
func Filter2D(data []any, box geom.Rect) []*Record {
	var out []*Record
	for _, d := range data {
		rec := d.(*Record)
		if rec.MBR.Intersects(box) {
			out = append(out, rec)
		}
	}
	return out
}

// NearestK returns the k trees with the smallest crown-polygon distance to p,
// ties broken by record id.
//
// Candidate generation is the index's distance-ordered iterator, which yields
// trees by increasing MBR lower-bound distance. A crown is never closer than
// its MBR, so a refined candidate can be emitted as soon as its exact distance
// is no larger than the iterator's lower bound for everything unseen.
func (s *Survey) NearestK(p geom.Point, k int) []NearestRecord {
	if k <= 0 {
		return nil
	}
	next, stop := s.NearestKIterator(p)
	defer stop()
	out := make([]NearestRecord, 0, k)
	for len(out) < k {
		r, ok := next()
		if !ok {
			break
		}
		out = append(out, r)
	}
	return out
}

// NearestKIterator streams trees in increasing crown-polygon distance. The
// returned next function yields one tree per call and reports false at the end
// of the stream; stop releases the underlying index query early and is safe to
// call after exhaustion.
func (s *Survey) NearestKIterator(p geom.Point) (next func() (NearestRecord, bool), stop func()) {
	pt := []float64{p.X, p.Y}
	it := s.Space2D.NewKNNIterator(pt, nil)
	// pending is sorted by exact crown distance then id.
	var pending []NearestRecord
	insert := func(nr NearestRecord) {
		idx := sort.Search(len(pending), func(i int) bool {
			if pending[i].Dist != nr.Dist {
				return pending[i].Dist > nr.Dist
			}
			return pending[i].Record.ID >= nr.Record.ID
		})
		pending = append(pending, NearestRecord{})
		copy(pending[idx+1:], pending[idx:])
		pending[idx] = nr
	}
	next = func() (NearestRecord, bool) {
		for {
			if len(pending) > 0 {
				lb, has := it.LowerBound()
				// Emit only when the head is *strictly* closer than anything
				// unseen. On a tie an unseen tree with a smaller record id
				// would have to come first, so equal-distance results are
				// held in pending (sorted by distance then id) until the
				// stream moves past them.
				if !has || pending[0].Dist < lb {
					r := pending[0]
					pending = pending[1:]
					return r, true
				}
			}
			if !it.Next() {
				it.Finish()
				if len(pending) > 0 {
					r := pending[0]
					pending = pending[1:]
					return r, true
				}
				return NearestRecord{}, false
			}
			rec := it.Last.Data.(*Record)
			insert(NearestRecord{Record: rec, Dist: geom.DistanceToPolygon(p, rec.Crown)})
		}
	}
	stop = func() { it.Finish() }
	return next, stop
}

// NearestStream returns up to limit trees in increasing crown distance.
func (s *Survey) NearestStream(p geom.Point, limit int) []NearestRecord {
	if limit <= 0 {
		return nil
	}
	next, stop := s.NearestKIterator(p)
	defer stop()
	out := make([]NearestRecord, 0, limit)
	for len(out) < limit {
		r, ok := next()
		if !ok {
			break
		}
		out = append(out, r)
	}
	return out
}

// BruteForceCrowns is the reference implementation for the construction-site
// query.
func BruteForceCrowns(recs []*Record, red geom.Rect) []*Record {
	out := make([]*Record, 0)
	for _, r := range recs {
		if r.MBR.Intersects(red) && r.Crown.IntersectsRect(red) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BruteForceRange is the reference implementation for a 2D MBR range query.
func BruteForceRange(recs []*Record, box geom.Rect) []*Record {
	out := make([]*Record, 0)
	for _, r := range recs {
		if r.MBR.Intersects(box) {
			out = append(out, r)
		}
	}
	return out
}

// BruteForceRange3D is the reference implementation for a 3D range query.
func BruteForceRange3D(recs []*Record, box geom.Rect) []*Record {
	out := make([]*Record, 0)
	for _, r := range recs {
		if r.Rect3D().Intersects(box) {
			out = append(out, r)
		}
	}
	return out
}

// BruteForceKNN is the reference implementation for nearest neighbours by
// crown polygon distance.
func BruteForceKNN(recs []*Record, p geom.Point, k int) []NearestRecord {
	type cand struct {
		rec  *Record
		dist float64
	}
	cands := make([]cand, 0, len(recs))
	for _, r := range recs {
		d := geom.DistanceToPolygon(p, r.Crown)
		cands = append(cands, cand{r, d})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].rec.ID < cands[j].rec.ID
	})
	if len(cands) > k {
		cands = cands[:k]
	}
	out := make([]NearestRecord, len(cands))
	for i, c := range cands {
		out[i] = NearestRecord{Record: c.rec, Dist: c.dist}
	}
	return out
}

// RadialMBRDist is a convenience for the "crown distance lower bound".
func RadialMBRDist(p geom.Point, r geom.Rect) float64 {
	return math.Sqrt(geom.DistToRectSq(p, r))
}

// CrownsIntersectingPolygon returns trees whose crown polygon intersects the
// red-line polygon. The index is probed with the red line's bounding box (plus
// an unbounded time axis in 3D), then every candidate's MBR is re-tested
// cheaply and its crown is polygon-tested exactly.
func (s *Survey) CrownsIntersectingPolygon(red geom.Polygon) []*Record {
	rb := red.BoundingRect()
	var out []*Record
	for _, d := range s.Space2D.SearchRect(rb) {
		rec := d.(*Record)
		if !rec.MBR.Intersects(rb) {
			continue
		}
		if rec.Crown.IntersectsPolygon(red) {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BruteForceCrownPolygon is the reference implementation for a red-line
// polygon query.
func BruteForceCrownPolygon(recs []*Record, red geom.Polygon) []*Record {
	rb := red.BoundingRect()
	out := make([]*Record, 0)
	for _, r := range recs {
		if r.MBR.Intersects(rb) && r.Crown.IntersectsPolygon(red) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GenerateRedLine returns a rotated rectangular construction red line, which
// is the common case for works permits. aspect in [0.35, 1] and the angle is
// uniform.
func GenerateRedLine(rng *rand.Rand, cx, cy, w, h, angle float64) geom.Polygon {
	ca, sa := math.Cos(angle), math.Sin(angle)
	corner := func(dx, dy float64) geom.Point {
		return geom.Point{X: cx + dx*ca - dy*sa, Y: cy + dx*sa + dy*ca}
	}
	hw, hh := w/2, h/2
	return geom.NewPolygon([]geom.Point{
		corner(-hw, -hh), corner(hw, -hh), corner(hw, hh), corner(-hw, hh),
	})
}

// QueryBox3D builds a 3D box over rect2D and the unix-millisecond interval
// [from, to], converted to axis units.
func QueryBox3D(rect2D geom.Rect, from, to int64) geom.Rect {
	z0, z1 := DateRangeAxis(from, to)
	return geom.NewRectFromBounds(
		[]float64{rect2D.Min(0), rect2D.Min(1), z0},
		[]float64{rect2D.Max(0), rect2D.Max(1), z1},
	)
}

// MBRHitsPolygon returns the ids of trees whose crown MBR intersects the red
// line's bounding box: the index's rectangle pass before the exact polygon
// test.
func (s *Survey) MBRHitsPolygon(red geom.Polygon) []int {
	rb := red.BoundingRect()
	out := make([]int, 0, 16)
	for _, d := range s.Space2D.SearchRect(rb) {
		out = append(out, d.(*Record).ID)
	}
	sort.Ints(out)
	return out
}

// CrownsIntersectingPolygonTraced is CrownsIntersectingPolygon plus the number
// of index nodes the MBR pass touched.
func (s *Survey) CrownsIntersectingPolygonTraced(red geom.Polygon) ([]*Record, int) {
	rb := red.BoundingRect()
	res := s.Space2D.Search(rb, nil)
	var out []*Record
	for _, d := range res.Data {
		rec := d.(*Record)
		if !rec.MBR.Intersects(rb) {
			continue
		}
		if rec.Crown.IntersectsPolygon(red) {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, res.NodesVisited
}
