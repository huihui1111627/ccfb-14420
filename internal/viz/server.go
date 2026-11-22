// Package viz serves the R*-tree structure explorer.
//
// The page is a single HTML file with embedded CSS and JavaScript (no CDN, no
// build step) so it works offline on an intranet. The server exposes a small
// JSON API backed by a live, mutable survey.
package viz

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ancienttrees/internal/forest"
	"ancienttrees/internal/geom"
	"ancienttrees/internal/rtree"
	"ancienttrees/internal/verify"
)

//go:embed static/*
var staticFS embed.FS

// Server owns the in-memory survey used by the page.
type Server struct {
	mu       sync.RWMutex
	dim      int
	survey   *forest.Survey
	recs     []*forest.Record
	dead     map[int]bool
	nextID   int
	rng      *rand.Rand
	simNow   time.Time
	clusters []forest.Cluster
	bulkMeta *bulkCache
	buildMS  int64 // time spent inserting the live records one by one
}

type bulkCache struct {
	Records int
	Dim     int
	Build   buildInfo
}

// NewServer builds the default demo city.
func NewServer(n int, dim int, seed int64) (*Server, error) {
	gen := forest.DefaultGenConfig(seed)
	gen.N = n
	simNow := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	recs, clusters := forest.Generate(gen, simNow)
	s := &Server{
		dim: dim, recs: recs, dead: map[int]bool{}, nextID: len(recs),
		rng: rand.New(rand.NewSource(seed)), simNow: simNow, clusters: clusters,
	}
	if err := s.rebuild(dim); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) rebuild(dim int) error {
	survey, err := forest.NewSurvey(append([]*forest.Record(nil), s.recs...), dim)
	if err != nil {
		return err
	}
	t0 := time.Now()
	for _, r := range s.surveyRecords() {
		survey.InsertRecord(r)
	}
	s.buildMS = time.Since(t0).Milliseconds()
	s.dim = dim
	s.survey = survey
	s.bulkMeta = nil
	return nil
}

func (s *Server) surveyRecords() []*forest.Record {
	out := make([]*forest.Record, 0, len(s.recs))
	for _, r := range s.recs {
		if !s.dead[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/overlap", s.handleOverlap)
	mux.HandleFunc("GET /api/records", s.handleRecords)
	mux.HandleFunc("GET /api/ab", s.handleAB)
	mux.HandleFunc("POST /api/query", s.handleQuery)
	mux.HandleFunc("POST /api/knn", s.handleKNN)
	mux.HandleFunc("POST /api/redline", s.handleRedline)
	mux.HandleFunc("POST /api/within", s.handleWithin)
	mux.HandleFunc("POST /api/dim", s.handleDim)
	mux.HandleFunc("POST /api/mutate", s.handleMutate)
	mux.HandleFunc("POST /api/rebuild", s.handleRebuild)
	sub, err := fs.Sub(staticFS, "static")
	if err == nil {
		mux.Handle("GET /", http.FileServer(http.FS(sub)))
	}
	return gzipJSON(mux)
}

// gzipJSON compresses JSON API responses when the client accepts gzip. The
// full tree snapshot is several megabytes uncompressed for a 45k-tree city.
func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) < 5 || r.URL.Path[:5] != "/api/" {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponse{ResponseWriter: w, gz: gzip.NewWriter(w)}
		gw.Header().Set("Content-Encoding", "gzip")
		gw.Header().Add("Vary", "Accept-Encoding")
		defer gw.gz.Close()
		next.ServeHTTP(gw, r)
	})
}

type gzipResponse struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g *gzipResponse) Write(b []byte) (int, error) { return g.gz.Write(b) }

func (g *gzipResponse) WriteHeader(code int) {
	g.Header().Del("Content-Length")
	g.ResponseWriter.WriteHeader(code)
}

type metaResponse struct {
	Records  int                 `json:"records"`
	Dim      int                 `json:"dim"`
	Height   int                 `json:"height"`
	Nodes    int                 `json:"nodes"`
	Clusters int                 `json:"clusters"`
	Bounds   geom.Rect           `json:"bounds"`
	Stats    rtree.StatsSnapshot `json:"stats"`
	Config   rtree.Config        `json:"config"`
	Overlap  rtree.OverlapStats  `json:"overlap"`
	// Build compares the live insertion-built tree with an STR-packed twin.
	Build buildInfo `json:"build"`
	// Overlap2D is the planar index's coverage/overlap, for comparison when
	// Dim == 3 (a 3D tree's node MBRs projected onto the plane can be long
	// spatial strips).
	Overlap2D rtree.OverlapStats `json:"overlap2d"`
}

type buildInfo struct {
	InsertionMS    int64   `json:"insertionMs"`
	BulkMS         int64   `json:"bulkMs"`
	BulkHeight     int     `json:"bulkHeight"`
	BulkNodes      int     `json:"bulkNodes"`
	BulkLeafFill   float64 `json:"bulkLeafFill"`
	BulkOverlap    float64 `json:"bulkOverlapArea"`
	InsertLeafFill float64 `json:"insertLeafFill"`
	InsertOverlap  float64 `json:"insertOverlapArea"`
	Ready          bool    `json:"ready"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ov := s.survey.Index.ComputeOverlapStats()
	live := s.surveyRecords()
	bulk := buildInfo{Ready: true}
	// Build an STR twin only when asked (it is cheap, but doing it on every
	// meta refresh wastes work); cache by record count.
	if s.bulkMeta == nil || s.bulkMeta.Records != len(live) || s.bulkMeta.Dim != s.dim {
		t0 := time.Now()
		items := make([]rtree.BulkLoadItem, len(live))
		for i, rec := range live {
			if s.dim == 3 {
				items[i] = rtree.BulkLoadItem{Rect: rec.MBR3, Data: rec}
			} else {
				items[i] = rtree.BulkLoadItem{Rect: rec.MBR, Data: rec}
			}
		}
		bt, err := rtree.New(rtree.DefaultConfig(s.dim))
		if err == nil {
			bt.BulkLoad(items)
			bst := bt.ComputeOverlapStats()
			bulkMS := time.Since(t0).Milliseconds()
			s.bulkMeta = &bulkCache{Records: len(live), Dim: s.dim, Build: buildInfo{
				BulkMS: bulkMS, BulkHeight: bt.Height(), BulkNodes: bst.Nodes,
				BulkLeafFill: bst.FillFactor, BulkOverlap: leafOverlap(bst),
				InsertLeafFill: ov.FillFactor, InsertOverlap: leafOverlap(ov),
				Ready: true,
			}}
		}
	}
	if s.bulkMeta != nil {
		bulk = s.bulkMeta.Build
		bulk.InsertLeafFill = ov.FillFactor
		bulk.InsertOverlap = leafOverlap(ov)
	}
	bulk.InsertionMS = s.buildMS
	var ov2 rtree.OverlapStats
	if s.dim == 3 {
		ov2 = s.survey.Space2D.ComputeOverlapStats()
	}
	writeJSON(w, metaResponse{
		Records: s.survey.Index.Len(), Dim: s.dim, Height: s.survey.Index.Height(),
		Nodes: ov.Nodes, Clusters: len(s.clusters), Bounds: s.cityBounds(),
		Stats: s.survey.Index.Stats.Snapshot(), Config: s.survey.Index.Config(), Overlap: ov,
		Build: bulk, Overlap2D: ov2,
	})
}

// leafOverlap is the total pairwise MBR overlap on the leaf level: the
// quantity that directly drives how many leaves a query must open.
func leafOverlap(st rtree.OverlapStats) float64 {
	for _, l := range st.Levels {
		if l.Level == 0 {
			return l.OverlapArea
		}
	}
	return 0
}

func (s *Server) cityBounds() geom.Rect {
	b := geom.NewRectFromBounds([]float64{-16000, -16000}, []float64{16000, 16000})
	if len(s.recs) > 0 {
		b = geom.NewRect(2)
		for _, r := range s.recs {
			if s.dead[r.ID] {
				continue
			}
			b.Extend(r.MBR)
		}
		if b.IsEmpty() {
			b = geom.NewRectFromBounds([]float64{-1, -1}, []float64{1, 1})
		}
	}
	return b
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Records of this tree are labelled with their id only; the page joins
	// them against /api/records.
	dto := s.survey.Index.Snapshot(func(data any) (int, rtree.RecordLabel) {
		rec := data.(*forest.Record)
		return rec.ID, rtree.RecordLabel{Kind: rec.Kind, Sampled: rec.Sampled, Age: rec.Age}
	})
	// The page draws the spatial plane; project 3D node boxes onto axes 0/1.
	if s.dim == 3 {
		for i := range dto.Nodes {
			dto.Nodes[i].Bounds = projectRect(dto.Nodes[i].Bounds)
			for j := range dto.Nodes[i].Entries {
				dto.Nodes[i].Entries[j].Bounds = projectRect(dto.Nodes[i].Entries[j].Bounds)
			}
		}
	}
	writeJSON(w, dto)
}

// projectRect drops all axes beyond the first two.
func projectRect(r geom.Rect) geom.Rect {
	if r.D <= 2 {
		return r
	}
	return geom.NewRectFromBounds(
		[]float64{r.Min(0), r.Min(1)},
		[]float64{r.Max(0), r.Max(1)},
	)
}

func (s *Server) handleOverlap(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, s.survey.Index.ComputeOverlapStats())
}

type queryRequest struct {
	Min      []float64 `json:"min"`
	Max      []float64 `json:"max"`
	FromDate int64     `json:"fromDate"` // optional, 3D only
	ToDate   int64     `json:"toDate"`
	Crown    bool      `json:"crown"` // exact crown polygon test
}

type queryResponse struct {
	IDs              []int         `json:"ids"`
	NodesVisited     int           `json:"nodesVisited"`
	LeafEntries      int           `json:"leafEntries"`
	Trace            []rtree.Visit `json:"trace"`
	LevelCount       map[int]int   `json:"levelCount"`
	LevelQueryCount  map[int]int   `json:"levelQueryCount"`
	LevelResultCount map[int]int   `json:"levelResultCount"`
	SkippedCount     map[int]int   `json:"skippedCount"`
	BruteCount       int           `json:"bruteCount"`
	BruteMS          float64       `json:"bruteMs"`
	QueryMS          float64       `json:"queryMs"`
	TotalNodes       int           `json:"totalNodes"`
	TotalLeaves      int           `json:"totalLeaves"`
	ResultRects      []geom.Rect   `json:"resultRects"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, 400)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(req.Min) < 2 || len(req.Max) < 2 {
		httpError(w, fmt.Errorf("min/max must have at least 2 coordinates"), 400)
		return
	}
	var box geom.Rect
	if s.dim == 3 {
		from, to := req.FromDate, req.ToDate
		if from == 0 && to == 0 {
			from, to = math.MinInt64/4, math.MaxInt64/4
		}
		z0, z1 := forest.DateRangeAxis(from, to)
		box = geom.NewRectFromBounds(
			[]float64{req.Min[0], req.Min[1], z0},
			[]float64{req.Max[0], req.Max[1], z1},
		)
	} else {
		box = geom.NewRectFromBounds([]float64{req.Min[0], req.Min[1]}, []float64{req.Max[0], req.Max[1]})
	}
	t0 := time.Now()
	opt := &rtree.VisitOption{Trace: true}
	res := s.survey.Index.Search(box, opt)
	elapsed := time.Since(t0)
	ids := make([]int, 0, len(res.Data))
	for _, d := range res.Data {
		ids = append(ids, d.(*forest.Record).ID)
	}
	sort.Ints(ids)
	if req.Crown {
		ids = ids[:0]
		for _, d := range res.Data {
			rec := d.(*forest.Record)
			if rec.Crown.IntersectsRect(geom.NewRectFromBounds(req.Min, req.Max)) {
				ids = append(ids, rec.ID)
			}
		}
		sort.Ints(ids)
	}
	// Brute force timing for the HUD.
	live := s.surveyRecords()
	tb := time.Now()
	brute := 0
	for _, rec := range live {
		var rr geom.Rect
		if s.dim == 3 {
			rr = rec.MBR3
		} else {
			rr = rec.MBR
		}
		if rr.Intersects(box) {
			if !req.Crown || rec.Crown.IntersectsRect(geom.NewRectFromBounds(req.Min, req.Max)) {
				brute++
			}
		}
	}
	bruteMS := float64(time.Since(tb).Microseconds()) / 1000
	levelCount := map[int]int{}
	levelResults := map[int]int{}
	for _, v := range res.Trace {
		levelCount[v.Level]++
		levelResults[v.Level] += v.Hits
	}
	// Total nodes per level, to show "visited X of Y" and skip counts.
	totalNodes := map[int]int{}
	totalLeaves := s.survey.Index.Len()
	var walk func(n *rtree.Node)
	var count func(n *rtree.Node)
	count = func(n *rtree.Node) {
		totalNodes[n.Level]++
		for _, e := range n.Entries {
			if c := e.Child(); c != nil {
				count(c)
			}
		}
	}
	_ = walk
	if s.survey.Index.Root != nil {
		count(s.survey.Index.Root)
	}
	skipped := map[int]int{}
	for lv, tot := range totalNodes {
		skipped[lv] = tot - levelCount[lv]
	}
	resultRects := make([]geom.Rect, len(res.Rect))
	copy(resultRects, res.Rect)
	if s.dim == 3 {
		for i := range res.Trace {
			res.Trace[i].Bounds = projectRect(res.Trace[i].Bounds)
		}
	}
	writeJSON(w, queryResponse{
		IDs: ids, NodesVisited: res.NodesVisited, LeafEntries: res.LeafEntries,
		Trace: res.Trace, LevelCount: levelCount, LevelResultCount: levelResults,
		SkippedCount: skipped, BruteCount: brute,
		BruteMS: bruteMS, QueryMS: float64(elapsed.Microseconds()) / 1000,
		TotalNodes: len(res.Trace), TotalLeaves: totalLeaves, ResultRects: resultRects,
	})
}

type knnRequest struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	K       int     `json:"k"`
	Polygon bool    `json:"polygon"` // exact crown distance
}

type knnResponse struct {
	Items      []knnItem     `json:"items"`
	NodesVisit int           `json:"nodesVisited"`
	Trace      []rtree.Visit `json:"trace"`
	QueryMS    float64       `json:"queryMs"`
	BruteMS    float64       `json:"bruteMs"`
}

type knnItem struct {
	ID   int         `json:"id"`
	Dist float64     `json:"dist"`
	X    float64     `json:"x"`
	Y    float64     `json:"y"`
	Poly [][]float64 `json:"poly,omitempty"`
}

func (s *Server) handleKNN(w http.ResponseWriter, r *http.Request) {
	var req knnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, 400)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if req.K <= 0 {
		req.K = 10
	}
	t0 := time.Now()
	var items []knnItem
	if req.Polygon {
		for _, nr := range s.survey.NearestK(geom.Point{X: req.X, Y: req.Y}, req.K) {
			items = append(items, recKNNItem(nr.Record, nr.Dist))
		}
	}
	trace := []rtree.Visit{}
	nodesVisited := 0
	if !req.Polygon {
		pt := []float64{req.X, req.Y}
		if s.dim == 3 {
			pt = append(pt, 0)
		}
		hits, res := s.survey.Index.KNN(pt, req.K, &rtree.VisitOption{Trace: true})
		nodesVisited = res.NodesVisited
		trace = res.Trace
		items = items[:0]
		for _, h := range hits {
			items = append(items, recKNNItem(h.Data.(*forest.Record), h.Dist))
		}
	}
	elapsed := time.Since(t0)
	writeJSON(w, knnResponse{
		Items: items, NodesVisit: nodesVisited, Trace: trace,
		QueryMS: float64(elapsed.Microseconds()) / 1000,
	})
}

func recKNNItem(r *forest.Record, d float64) knnItem {
	it := knnItem{ID: r.ID, Dist: d, X: r.X, Y: r.Y}
	it.Poly = make([][]float64, len(r.Crown.Ring))
	for i, p := range r.Crown.Ring {
		it.Poly[i] = []float64{p.X, p.Y}
	}
	return it
}

type redlineRequest struct {
	Coords [][]float64 `json:"coords"` // polygon vertices
}

type redlineResponse struct {
	MBRHits   []int         `json:"mbrHits"`
	CrownHits []int         `json:"crownHits"`
	Nodes     int           `json:"nodesVisited"`
	Trace     []rtree.Visit `json:"trace"`
	QueryMS   float64       `json:"queryMs"`
	BruteMS   float64       `json:"bruteMs"`
	BruteHits int           `json:"bruteHits"`
}

func (s *Server) handleRedline(w http.ResponseWriter, r *http.Request) {
	var req redlineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, 400)
		return
	}
	if len(req.Coords) < 3 {
		httpError(w, fmt.Errorf("red line needs at least 3 vertices"), 400)
		return
	}
	pts := make([]geom.Point, len(req.Coords))
	for i, c := range req.Coords {
		if len(c) < 2 {
			httpError(w, fmt.Errorf("vertex %d needs 2 coords", i), 400)
			return
		}
		pts[i] = geom.Point{X: c[0], Y: c[1]}
	}
	poly := geom.NewPolygon(pts)
	s.mu.RLock()
	defer s.mu.RUnlock()
	t0 := time.Now()
	crown, nodes := s.survey.CrownsIntersectingPolygonTraced(poly)
	elapsed := time.Since(t0)
	mbrHits := s.survey.MBRHitsPolygon(poly)
	tb := time.Now()
	live := s.surveyRecords()
	brute := make([]int, 0)
	rb := poly.BoundingRect()
	for _, rec := range live {
		if rec.MBR.Intersects(rb) && rec.Crown.IntersectsPolygon(poly) {
			brute = append(brute, rec.ID)
		}
	}
	sort.Ints(brute)
	bruteMS := float64(time.Since(tb).Microseconds()) / 1000
	crownIDs := make([]int, len(crown))
	for i, rec := range crown {
		crownIDs[i] = rec.ID
	}
	writeJSON(w, redlineResponse{
		MBRHits: mbrHits, CrownHits: crownIDs, Nodes: nodes, QueryMS: float64(elapsed.Microseconds()) / 1000,
		BruteMS: bruteMS, BruteHits: len(brute),
	})
}

type withinRequest struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Radius float64 `json:"radius"`
}

type withinItem struct {
	ID     int     `json:"id"`
	Dist   float64 `json:"dist"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Code   string  `json:"code"`
	Health string  `json:"health"`
	DBH    float64 `json:"dbh"`
}

type withinResponse struct {
	Items        []withinItem  `json:"items"`
	NodesVisited int           `json:"nodesVisited"`
	LeafEntries  int           `json:"leafEntries"`
	MBRHits      int           `json:"mbrHits"`
	BruteHits    int           `json:"bruteHits"`
	QueryMS      float64       `json:"queryMs"`
	BruteMS      float64       `json:"bruteMs"`
	Trace        []rtree.Visit `json:"trace"`
}

// handleWithin implements the inspector's "which ancient trees are within r
// metres of me" query: index MBR pass, then exact crown-polygon distance.
func (s *Server) handleWithin(w http.ResponseWriter, r *http.Request) {
	var req withinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, 400)
		return
	}
	if req.Radius <= 0 {
		req.Radius = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	box := geom.NewRectFromBounds(
		[]float64{req.X - req.Radius, req.Y - req.Radius},
		[]float64{req.X + req.Radius, req.Y + req.Radius},
	)
	var qbox geom.Rect
	if s.dim == 3 {
		qbox = geom.NewRectFromBounds(
			[]float64{box.Min(0), box.Min(1), math.Inf(-1)},
			[]float64{box.Max(0), box.Max(1), math.Inf(1)},
		)
	} else {
		qbox = box
	}
	t0 := time.Now()
	res := s.survey.Index.Search(qbox, &rtree.VisitOption{Trace: true})
	p := geom.Point{X: req.X, Y: req.Y}
	items := make([]withinItem, 0, 32)
	checked := 0
	for _, d := range res.Data {
		rec := d.(*forest.Record)
		if math.Sqrt(geom.DistToRectSq(p, rec.MBR)) > req.Radius {
			continue
		}
		checked++
		dist := geom.DistanceToPolygon(p, rec.Crown)
		if dist <= req.Radius {
			items = append(items, withinItem{
				ID: rec.ID, Dist: dist, X: rec.X, Y: rec.Y, Code: rec.Code,
				Health: rec.Health, DBH: rec.DBH,
			})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Dist != items[j].Dist {
			return items[i].Dist < items[j].Dist
		}
		return items[i].ID < items[j].ID
	})
	elapsed := time.Since(t0)
	// Brute force reference.
	tb := time.Now()
	brute := 0
	for _, rec := range s.surveyRecords() {
		if geom.DistanceToPolygon(p, rec.Crown) <= req.Radius {
			brute++
		}
	}
	bruteMS := float64(time.Since(tb).Microseconds()) / 1000
	if s.dim == 3 {
		for i := range res.Trace {
			res.Trace[i].Bounds = projectRect(res.Trace[i].Bounds)
		}
	}
	writeJSON(w, withinResponse{
		Items: items, NodesVisited: res.NodesVisited, LeafEntries: res.LeafEntries,
		MBRHits: len(res.Data), BruteHits: brute,
		QueryMS: float64(elapsed.Microseconds()) / 1000, BruteMS: bruteMS,
		Trace: res.Trace,
	})
}

type dimRequest struct {
	Dim int `json:"dim"`
}

func (s *Server) handleDim(w http.ResponseWriter, r *http.Request) {
	var req dimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, 400)
		return
	}
	if req.Dim != 2 && req.Dim != 3 {
		httpError(w, fmt.Errorf("dim must be 2 or 3"), 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rebuild(req.Dim); err != nil {
		httpError(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"dim": s.dim, "height": s.survey.Index.Height(), "records": s.survey.Index.Len()})
}

type mutateRequest struct {
	Action string  `json:"action"` // insert | delete | rebuildBulk
	Count  int     `json:"count"`
	CX     float64 `json:"cx"`
	CY     float64 `json:"cy"`
	Radius float64 `json:"radius"`
}

type mutateResponse struct {
	Inserted int     `json:"inserted"`
	Deleted  int     `json:"deleted"`
	Records  int     `json:"records"`
	Height   int     `json:"height"`
	MS       float64 `json:"ms"`
}

func (s *Server) handleMutate(w http.ResponseWriter, r *http.Request) {
	var req mutateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t0 := time.Now()
	resp := mutateResponse{}
	switch req.Action {
	case "insert":
		n := req.Count
		if n <= 0 {
			n = 100
		}
		if n > 5000 {
			n = 5000
		}
		for i := 0; i < n; i++ {
			nx := req.CX + s.rng.NormFloat64()*math.Max(req.Radius, 20)
			ny := req.CY + s.rng.NormFloat64()*math.Max(req.Radius, 20)
			crown := geom.RandomConvexPolygon(s.rng, geom.Point{X: nx, Y: ny}, 5+s.rng.Intn(5), 1.5, 9.5)
			insp := s.simNow.Add(-time.Duration(s.rng.Int63n(6 * 365 * 24 * int64(time.Hour)))).UnixMilli()
			mbr := crown.BoundingRect()
			z := forest.DateAxis(insp)
			rec := &forest.Record{
				ID: s.nextID, Code: fmt.Sprintf("京古新%05d", s.nextID), X: nx, Y: ny,
				Crown: crown, MBR: mbr, InspectAt: insp,
				MBR3: geom.NewRectFromBounds(
					[]float64{mbr.Min(0), mbr.Min(1), z},
					[]float64{mbr.Max(0), mbr.Max(1), z},
				),
				Height: 10 + s.rng.Float64()*12, DBH: 25 + s.rng.Float64()*60,
				Age:     100 + s.rng.Float64()*600,
				Species: forest.Species[s.rng.Intn(len(forest.Species))],
				Health:  forest.Health[s.rng.Intn(len(forest.Health))],
				Kind:    forest.Kinds[s.rng.Intn(len(forest.Kinds))],
				Sampled: s.rng.Float64() < 0.35,
			}
			s.nextID++
			s.recs = append(s.recs, rec)
			s.survey.InsertRecord(rec)
			resp.Inserted++
		}
	case "delete":
		n := req.Count
		if n <= 0 {
			n = 100
		}
		if n > 5000 {
			n = 5000
		}
		// Delete records inside the given disk.
		var victims []*forest.Record
		for _, rec := range s.surveyRecords() {
			dx, dy := rec.X-req.CX, rec.Y-req.CY
			if dx*dx+dy*dy <= req.Radius*req.Radius {
				victims = append(victims, rec)
				if len(victims) >= n {
					break
				}
			}
		}
		for _, rec := range victims {
			s.survey.DeleteRecord(rec)
			s.dead[rec.ID] = true
			resp.Deleted++
		}
	case "rebuildBulk":
		s.survey.Records = s.surveyRecords()
		s.survey.BulkLoadRecords()
	default:
		httpError(w, fmt.Errorf("unknown action %q", req.Action), 400)
		return
	}
	resp.Records = s.survey.Index.Len()
	resp.Height = s.survey.Index.Height()
	resp.MS = float64(time.Since(t0).Microseconds()) / 1000
	writeJSON(w, resp)
}

func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rebuild(s.dim); err != nil {
		httpError(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"records": s.survey.Index.Len(), "height": s.survey.Index.Height()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func httpError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

var _ = strconv.Itoa

// ---------------------------------------------------------------- records

type recordDTO struct {
	ID        int         `json:"id"`
	Code      string      `json:"code"`
	X         float64     `json:"x"`
	Y         float64     `json:"y"`
	Kind      string      `json:"kind"`
	Health    string      `json:"health"`
	Species   string      `json:"species"`
	Sampled   bool        `json:"sampled"`
	Age       float64     `json:"age"`
	Height    float64     `json:"height"`
	DBH       float64     `json:"dbh"`
	InspectAt int64       `json:"inspectAt"`
	MBR       geom.Rect   `json:"mbr"`
	Crown     [][]float64 `json:"crown"`
	District  int         `json:"district"`
}

func toRecordDTO(r *forest.Record) recordDTO {
	crown := make([][]float64, len(r.Crown.Ring))
	for i, p := range r.Crown.Ring {
		crown[i] = []float64{p.X, p.Y}
	}
	return recordDTO{
		ID: r.ID, Code: r.Code, X: r.X, Y: r.Y, Kind: r.Kind, Health: r.Health,
		Species: r.Species, Sampled: r.Sampled, Age: r.Age, Height: r.Height,
		DBH: r.DBH, InspectAt: r.InspectAt, MBR: r.MBR, Crown: crown, District: r.District,
	}
}

// handleRecords serves crown geometry. Query parameters:
//
//	ids=1,2,3       explicit record ids (ignored with sample)
//	sample=N        deterministic random sample across live records
//	minx..maxy      optional bbox filter
//	limit=N         cap for bbox queries (default 3000)
func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := r.URL.Query()
	if ids := q.Get("ids"); ids != "" {
		var out []recordDTO
		for _, part := range splitComma(ids) {
			id, err := strconv.Atoi(part)
			if err != nil {
				continue
			}
			for _, rec := range s.recs {
				if rec.ID == id && !s.dead[id] {
					out = append(out, toRecordDTO(rec))
					break
				}
			}
		}
		writeJSON(w, out)
		return
	}
	live := s.surveyRecords()
	if bbox := q.Get("minx"); bbox != "" {
		f := parseFloats(q.Get("minx"), q.Get("miny"), q.Get("maxx"), q.Get("maxy"))
		if f != nil {
			box := geom.NewRectFromBounds(f[:2], f[2:])
			limit := atoiDefault(q.Get("limit"), 3000)
			out := make([]recordDTO, 0, 64)
			for _, rec := range live {
				if rec.MBR.Intersects(box) {
					out = append(out, toRecordDTO(rec))
					if len(out) >= limit {
						break
					}
				}
			}
			writeJSON(w, out)
			return
		}
	}
	n := atoiDefault(q.Get("sample"), 6000)
	if n > len(live) {
		n = len(live)
	}
	rng := rand.New(rand.NewSource(20261122))
	idx := rng.Perm(len(live))
	out := make([]recordDTO, 0, n)
	for _, i := range idx[:n] {
		out = append(out, toRecordDTO(live[i]))
	}
	writeJSON(w, out)
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, ch := range s {
		if ch == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(ch)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func parseFloats(vals ...string) []float64 {
	out := make([]float64, len(vals))
	for i, v := range vals {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil
		}
		out[i] = f
	}
	return out
}

// ---------------------------------------------------------------- live A/B

type abResponse struct {
	Queries          int     `json:"queries"`
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
	NodesImprovement float64 `json:"nodesImprovementPct"`
	OverlapChange    float64 `json:"overlapChangePct"`
	SameAnswers      bool    `json:"sameAnswers"`
	MS               int64   `json:"ms"`
}

func (s *Server) handleAB(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	live := s.surveyRecords()
	s.mu.RUnlock()
	t0 := time.Now()
	ab, _ := verify.CompareReinsertion(live, 20261122, 1200)
	resp := abResponse{
		Queries: ab.Queries, OnAvgNodes: ab.OnAvgNodes, OffAvgNodes: ab.OffAvgNodes,
		OnAvgLeaf: ab.OnAvgLeaf, OffAvgLeaf: ab.OffAvgLeaf,
		OnOverlapArea: ab.OnOverlapArea, OffOverlapArea: ab.OffOverlapArea,
		OnCoverArea: ab.OnCoverArea, OffCoverArea: ab.OffCoverArea,
		OnSplits: ab.OnSplits, OffSplits: ab.OffSplits,
		OnReinsertOps: ab.OnReinsertOps, OnReinserted: ab.OnReinserted,
		OnHeight: ab.OnHeight, OffHeight: ab.OffHeight,
		NodesImprovement: ab.NodesImprovement, SameAnswers: ab.SameAnswers,
		MS: time.Since(t0).Milliseconds(),
	}
	if ab.OffOverlapArea > 0 {
		resp.OverlapChange = 100 * (ab.OnOverlapArea - ab.OffOverlapArea) / ab.OffOverlapArea
	}
	writeJSON(w, resp)
}
