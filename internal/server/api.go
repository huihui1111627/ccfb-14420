package server

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"sort"

	"ancienttrees/internal/app"
	"ancienttrees/internal/geom"
	"ancienttrees/internal/rtree"
)

// NewWorld generates a city dataset and indexes it.
func NewWorld(seed int64, trees int, opts app.IndexOptions) *World {
	data := app.NewDataset(seed, trees, trees/25+1)
	store := app.BuildStore(data, opts, true, seed)
	return &World{Data: data, Store: store, Opts: opts, Seed: seed}
}

func (s *Server) indexPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// viewTransform maps 3-D index space (x, y, date) to 2-D screen coordinates
// for one level's structure diagram.
type viewTransform struct {
	MinX, MinY, MaxX, MaxY float64 // city extents
	DateLo, DateHi         float64
	Pitch                  float64 // vertical compression for the date axis
	Dims                   uint8
}

func (s *Server) transform(dims uint8) viewTransform {
	t := viewTransform{MinX: 0, MinY: 0, MaxX: 20000, MaxY: 20000, Dims: dims}
	if dims == 3 {
		// Data dates live in [0,120); pad so canopy boxes are not clipped.
		t.DateLo, t.DateHi = 0, 120
		// A 120-month span is tiny next to a 20 km city, so draw it with a
		// visual gain rather than vanishing into the x/y extent.
		t.Pitch = 18
	}
	return t
}

type vbox struct {
	X0, Y0, X1, Y1 float64
}

func (t viewTransform) box(b geom.Box) vbox {
	out := vbox{X0: b.Lo[0], Y0: b.Lo[1], X1: b.Hi[0], Y1: b.Hi[1]}
	if t.Dims == 3 {
		m := b.Center(2)
		shift := (m - (t.DateLo+t.DateHi)/2) * t.Pitch
		out.Y0 += shift
		out.Y1 += shift
	}
	return out
}

func (t viewTransform) point(p [3]float64) (float64, float64) {
	x, y := p[0], p[1]
	if t.Dims == 3 {
		y += (p[2] - (t.DateLo+t.DateHi)/2) * t.Pitch
	}
	return x, y
}

// --- API payloads -------------------------------------------------------

type levelPayload struct {
	Level    int     `json:"level"`
	Nodes    int     `json:"nodes"`
	Entries  int     `json:"entries"`
	Coverage float64 `json:"coverage"`
	Overlap  float64 `json:"overlap"`
	NodeArea float64 `json:"nodeArea"`
}

type statsPayload struct {
	app.StatsJSON
	Levels     []levelPayload `json:"levels"`
	LiveTrees  int            `json:"liveTrees"`
	RedLines   int            `json:"redLines"`
	DateOrigin string         `json:"dateOrigin"`
	Dims       uint8          `json:"dims"`
}

func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	dims := uint8(intParam(r, "dims", 2))
	if dims != 3 {
		dims = 2
	}
	st := s.World.Store.Stats(dims)
	payload := statsPayload{
		StatsJSON:  st,
		LiveTrees:  s.World.Data.LiveCount(),
		RedLines:   len(s.World.Data.Lines),
		DateOrigin: app.DateOrigin,
		Dims:       dims,
	}
	for _, l := range st.Levels {
		payload.Levels = append(payload.Levels, levelPayload{
			Level: l.Level, Nodes: l.Nodes, Entries: l.Entries,
			Coverage: l.Coverage, Overlap: l.Overlap, NodeArea: l.NodeArea,
		})
	}
	// app.StatsJSON also has Levels with the same shape; keep the API compact.
	payload.StatsJSON.Levels = nil
	writeJSON(w, payload)
}

// overlapCell is one cell of a level's overlap grid.
type overlapCell struct {
	Count int `json:"count"`
}

type overlapPayload struct {
	Dims       uint8         `json:"dims"`
	Level      int           `json:"level"`
	N          int           `json:"n"`
	X0         float64       `json:"x0"`
	Y0         float64       `json:"y0"`
	CW         float64       `json:"cw"`
	CH         float64       `json:"ch"`
	Cells      []overlapCell `json:"cells"`
	Plane      bool          `json:"plane"`
	Covered    int           `json:"covered"`
	Overlapped int           `json:"overlapped"`
	// ExactOverlap is this level's summed pairwise sibling overlap.
	ExactOverlap float64 `json:"exactOverlap"`
	// ExactArea is the summed area/volume of this level's node rectangles.
	ExactArea float64 `json:"exactArea"`
	// PlaneOverlap is the summed pairwise x-y overlap of the level's nodes.
	PlaneOverlap float64 `json:"planeOverlap"`
	// PlaneArea is the summed x-y footprint of the level's nodes.
	PlaneArea float64 `json:"planeArea"`
	MaxCount  int     `json:"maxCount"`
}

// apiOverlap rasterises one level's node rectangles into an N x N grid in
// screen space, reporting how many rectangles cover each cell.
func (s *Server) apiOverlap(w http.ResponseWriter, r *http.Request) {
	dims := uint8(intParam(r, "dims", 2))
	if dims != 3 {
		dims = 2
	}
	level := intParam(r, "level", 0)
	n := intParam(r, "n", 48)
	if n < 4 {
		n = 4
	}
	if n > 128 {
		n = 128
	}
	tr := s.World.Store.Tree(dims)
	payload := overlapPayload{Dims: dims, Level: level, N: n, X0: 0, Y0: 0, Plane: dims == 3}
	// Grid spans the city (and date band in 3-D) in screen space.
	payload.CW = 20000 / float64(n)
	payload.CH = 20000 / float64(n)
	payload.Cells = make([]overlapCell, n*n)

	// The grid is computed on each node's x-y footprint, ignoring the date
	// axis, so in 3-D the heatmap answers "which ground positions are covered
	// by several node boxes" instead of being saturated by date projection.
	type sbox struct{ x0, y0, x1, y1 float64 }
	var boxes []sbox
	for _, node := range tr.Nodes() {
		if node.Level != level {
			continue
		}
		boxes = append(boxes, sbox{node.Rect.Lo[0], node.Rect.Lo[1], node.Rect.Hi[0], node.Rect.Hi[1]})
	}
	// exact metrics come from the same snapshot for consistency; the x-y
	// footprint extent is accumulated per level for the 3-D summary.
	var planeArea float64
	for _, l := range s.World.Store.Stats(dims).Levels {
		if l.Level == level {
			payload.ExactOverlap = l.Overlap
			payload.ExactArea = l.Coverage
		}
	}
	for _, node := range tr.Nodes() {
		if node.Level == level {
			planeArea += node.Rect.Len(0) * node.Rect.Len(1)
		}
	}
	payload.PlaneArea = planeArea
	if dims == 3 {
		// Pairwise overlap of the x-y footprints, matching the grid above.
		type fp struct{ x0, y0, x1, y1 float64 }
		var fps []fp
		for _, node := range tr.Nodes() {
			if node.Level == level {
				fps = append(fps, fp{node.Rect.Lo[0], node.Rect.Lo[1], node.Rect.Hi[0], node.Rect.Hi[1]})
			}
		}
		ov := 0.0
		for i := range fps {
			for j := i + 1; j < len(fps); j++ {
				w := math.Min(fps[i].x1, fps[j].x1) - math.Max(fps[i].x0, fps[j].x0)
				h := math.Min(fps[i].y1, fps[j].y1) - math.Max(fps[i].y0, fps[j].y0)
				if w > 0 && h > 0 {
					ov += w * h
				}
			}
		}
		payload.PlaneOverlap = ov
	}
	maxCell := 0
	for i := 0; i < n; i++ {
		cy0 := float64(i) * payload.CH
		cy1 := cy0 + payload.CH
		for j := 0; j < n; j++ {
			cx0 := float64(j) * payload.CW
			cx1 := cx0 + payload.CW
			cnt := 0
			for _, b := range boxes {
				if b.x1 <= cx0 || b.x0 >= cx1 || b.y1 <= cy0 || b.y0 >= cy1 {
					continue
				}
				cnt++
			}
			payload.Cells[i*n+j] = overlapCell{Count: cnt}
			if cnt > maxCell {
				maxCell = cnt
			}
			if cnt > 0 {
				payload.Covered++
			}
			if cnt > 1 {
				payload.Overlapped++
			}
		}
	}
	payload.MaxCount = maxCell
	writeJSON(w, payload)
}

type treePayload struct {
	ID      int          `json:"id"`
	Species string       `json:"species"`
	X       float64      `json:"x"`
	Y       float64      `json:"y"`
	Date    float64      `json:"date"`
	Crown   [][2]float64 `json:"crown"`
	BBox    [4]float64   `json:"bbox"`
}

func treeDTO(t *app.Tree) treePayload {
	return treePayload{
		ID: t.ID, Species: t.Species, X: t.X, Y: t.Y, Date: t.Date,
		Crown: t.Crown,
		BBox:  [4]float64{t.BBox2.Lo[0], t.BBox2.Lo[1], t.BBox2.Hi[0], t.BBox2.Hi[1]},
	}
}

// apiTrees returns trees in a viewport (max 3000) plus all red lines.
func (s *Server) apiTrees(w http.ResponseWriter, r *http.Request) {
	x0 := floatParam(r, "x0", 0)
	y0 := floatParam(r, "y0", 0)
	x1 := floatParam(r, "x1", 20000)
	y1 := floatParam(r, "y1", 20000)
	dims := uint8(intParam(r, "dims", 2))
	if dims != 3 {
		dims = 2
	}
	q := geom.NewBox(2, [3]float64{x0, y0, 0}, [3]float64{x1, y1, 0})
	out := struct {
		Trees  []treePayload `json:"trees"`
		Lines  []linePayload `json:"lines"`
		Total  int           `json:"total"`
		Cutoff bool          `json:"cutoff"`
	}{Total: s.World.Data.LiveCount()}
	tr := s.World.Store.Tree(dims)
	count := 0
	tr.Search(q, func(e rtree.Entry) {
		if count >= 3000 {
			out.Cutoff = true
			return
		}
		out.Trees = append(out.Trees, treeDTO(e.Data.(*app.Tree)))
		count++
	})
	sort.Slice(out.Trees, func(i, j int) bool { return out.Trees[i].ID < out.Trees[j].ID })
	for _, l := range s.World.Data.Lines {
		out.Lines = append(out.Lines, lineDTO(l))
	}
	writeJSON(w, out)
}

type linePayload struct {
	ID    int          `json:"id"`
	Poly  [][2]float64 `json:"poly"`
	Label string       `json:"label"`
}

func lineDTO(l *app.RedLine) linePayload {
	return linePayload{ID: l.ID, Poly: l.Poly(), Label: l.Label}
}

type nodePayload struct {
	ID     int        `json:"id"`
	Level  int        `json:"level"`
	Rect   [4]float64 `json:"rect"`
	Parent int        `json:"parent"`
	Count  int        `json:"count"`
}

// apiNodes returns every node's rectangle for one level, in screen space.
func (s *Server) apiNodes(w http.ResponseWriter, r *http.Request) {
	dims := uint8(intParam(r, "dims", 2))
	if dims != 3 {
		dims = 2
	}
	level := intParam(r, "level", -1)
	tr := s.World.Store.Tree(dims)
	tf := s.transform(dims)
	nodes := tr.Nodes()
	// Sort by level then id for deterministic output.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Level != nodes[j].Level {
			return nodes[i].Level < nodes[j].Level
		}
		return nodes[i].ID < nodes[j].ID
	})
	out := struct {
		Dims  uint8         `json:"dims"`
		Level int           `json:"level"`
		Nodes []nodePayload `json:"nodes"`
	}{Dims: dims, Level: level}
	for _, n := range nodes {
		if level >= 0 && n.Level != level {
			continue
		}
		b := tf.box(n.Rect)
		parent := 0
		if n.Parent != nil {
			parent = n.Parent.ID
		}
		out.Nodes = append(out.Nodes, nodePayload{
			ID: n.ID, Level: n.Level,
			Rect:   [4]float64{b.X0, b.Y0, b.X1, b.Y1},
			Parent: parent, Count: len(n.Entries),
		})
	}
	if len(out.Nodes) > 20000 {
		out.Nodes = out.Nodes[:20000]
	}
	writeJSON(w, out)
}

type queryResponse struct {
	Kind            string        `json:"kind"`
	Dims            uint8         `json:"dims"`
	Query           *queryShape   `json:"query,omitempty"`
	Results         []treePayload `json:"results"`
	Count           int           `json:"count"`
	NodesVisited    int           `json:"nodesVisited"`
	EntriesExamined int           `json:"entriesExamined"`
	Visited         []int         `json:"visited"`
	PerLevel        []levelHits   `json:"perLevel"`
	Distances       []float64     `json:"distances,omitempty"`
	CellsPerLevel   int           `json:"cellsPerLevel"`
}

type queryShape struct {
	Rect    *[4]float64  `json:"rect,omitempty"`
	Poly    [][2]float64 `json:"poly,omitempty"`
	Point   *[2]float64  `json:"point,omitempty"`
	MaxDist float64      `json:"maxDist,omitempty"`
}

type levelHits struct {
	Level int `json:"level"`
	Nodes int `json:"nodes"`
}

func (s *Server) apiQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind == "" {
		kind = "range"
	}
	dims := uint8(intParam(r, "dims", 2))
	if dims != 3 {
		dims = 2
	}
	tr := s.World.Store.Tree(dims)
	tf := s.transform(dims)
	trace := q.Get("trace") != "0"
	tr.SetTrace(trace)

	resp := queryResponse{Kind: kind, Dims: dims, CellsPerLevel: 48}

	switch kind {
	case "range":
		box := s.queryBox(r, dims)
		shape := tf.box(box)
		r := [4]float64{shape.X0, shape.Y0, shape.X1, shape.Y1}
		resp.Query = &queryShape{Rect: &r}
		ids := []int{}
		st := tr.Search(box, func(e rtree.Entry) { ids = append(ids, int(e.Key)) })
		resp.Visited = st.Visited
		resp.NodesVisited = st.NodesVisited
		resp.EntriesExamined = st.EntriesExamined
		resp.PerLevel = levelHitsFrom(st)
		sort.Ints(ids)
		for _, id := range ids {
			resp.Results = append(resp.Results, treeDTO(s.World.Data.Trees[id]))
		}
	case "polygon":
		// Canopy polygons are 2-D. Even in 3-D mode the red-line test runs on
		// the 2-D index: a canopy is cut by the line no matter when surveyed.
		tr = s.World.Store.Tree(2)
		tr.SetTrace(trace)
		line := s.queryLine(r)
		x0, y0, x1, y1 := line.X0, line.Y0, line.X1, line.Y1
		box := line.Rect()
		shape := tf.box(box)
		_ = shape
		ids := []int{}
		st := tr.Search(box, func(e rtree.Entry) {
			t := e.Data.(*app.Tree)
			if geom.PolyBoxIntersects(t.Crown, box) {
				ids = append(ids, t.ID)
			}
		})
		r := [4]float64{x0, y0, x1, y1}
		resp.Query = &queryShape{Rect: &r, Poly: line.Poly()}
		resp.Visited = st.Visited
		resp.NodesVisited = st.NodesVisited
		resp.EntriesExamined = st.EntriesExamined
		resp.PerLevel = levelHitsFrom(st)
		sort.Ints(ids)
		for _, id := range ids {
			resp.Results = append(resp.Results, treeDTO(s.World.Data.Trees[id]))
		}
	case "nearest":
		p := [3]float64{floatParam(r, "x", 10000), floatParam(r, "y", 10000), floatParam(r, "z", 60)}
		maxDist := floatParam(r, "maxDist", 0)
		k := intParam(r, "k", 5)
		mode := q.Get("mode")
		it := tr.Nearest(p, maxDist)
		limit := k
		if mode == "iter" {
			limit = intParam(r, "limit", 100)
		}
		var ids []int
		distances := []float64{}
		for len(ids) < limit {
			d, key, _, ok := it.Next()
			if !ok {
				break
			}
			ids = append(ids, int(key))
			distances = append(distances, math.Sqrt(d))
		}
		px, py := tf.point(p)
		resp.Query = &queryShape{Point: &[2]float64{px, py}, MaxDist: maxDist}
		resp.Visited = it.Stats.Visited
		resp.NodesVisited = it.Stats.NodesVisited
		resp.EntriesExamined = it.Stats.EntriesExamined
		resp.PerLevel = levelHitsFrom(it.Stats)
		for i, id := range ids {
			tp := treeDTO(s.World.Data.Trees[id])
			resp.Results = append(resp.Results, tp)
			resp.Distances = append(resp.Distances, distances[i])
		}
	default:
		writeErr(w, http.StatusBadRequest, "unknown kind")
		return
	}
	resp.Count = len(resp.Results)
	writeJSON(w, resp)
}

func levelHitsFrom(st rtree.QueryStats) []levelHits {
	if st.PerLevelNodes == nil {
		return nil
	}
	var ls []levelHits
	for lvl, n := range st.PerLevelNodes {
		ls = append(ls, levelHits{Level: lvl, Nodes: n})
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].Level < ls[j].Level })
	return ls
}

func (s *Server) queryBox(r *http.Request, dims uint8) geom.Box {
	x0 := floatParam(r, "x0", 0)
	y0 := floatParam(r, "y0", 0)
	x1 := floatParam(r, "x1", 2000)
	y1 := floatParam(r, "y1", 2000)
	if dims == 3 {
		d0 := floatParam(r, "d0", 0)
		d1 := floatParam(r, "d1", 120)
		return geom.NewBox(3, [3]float64{x0, y0, d0}, [3]float64{x1, y1, d1})
	}
	return geom.NewBox(2, [3]float64{x0, y0, 0}, [3]float64{x1, y1, 0})
}

func (s *Server) queryLine(r *http.Request) *app.RedLine {
	id := intParam(r, "line", -1)
	if id >= 0 {
		for _, l := range s.World.Data.Lines {
			if l.ID == id {
				return l
			}
		}
	}
	return &app.RedLine{
		ID: -1,
		X0: floatParam(r, "x0", 0), Y0: floatParam(r, "y0", 0),
		X1: floatParam(r, "x1", 2000), Y1: floatParam(r, "y1", 2000),
		Label: "查询红线",
	}
}

// --- mutations ----------------------------------------------------------

type insertRequest struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Date float64 `json:"date"`
}

func (s *Server) apiInsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req insertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	rng := rand.New(rand.NewSource(int64(len(s.World.Data.Live()))*7919 + int64(req.X)))
	t := s.World.Data.NewTree(rng, req.X, req.Y)
	if req.Date > 0 {
		t.Date = req.Date
		s.World.Data.RefreshBBoxes(t)
	}
	s.World.Data.Add(t)
	s.World.Store.Insert(t)
	writeJSON(w, treeDTO(t))
}

type deleteRequest struct {
	ID int `json:"id"`
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req deleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.World.Store.Remove(req.ID) == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("tree %d not found", req.ID))
		return
	}
	writeJSON(w, map[string]any{"deleted": req.ID, "live": s.World.Data.LiveCount()})
}
