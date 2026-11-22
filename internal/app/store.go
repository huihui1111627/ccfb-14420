package app

import (
	"math"

	"ancienttrees/internal/geom"
	"ancienttrees/internal/rtree"
)

// DimMode selects which index a query/store uses.
type DimMode int

const (
	Dim2D DimMode = 2
	Dim3D DimMode = 3
)

// IndexOptions configures the two trees held by a Store.
type IndexOptions struct {
	MaxEntries      int
	ForceReinsert   bool
	ReinsertPercent int
}

// QueryTrace is the optional node-visit information for one query.
type QueryTrace struct {
	Enabled         bool
	NodesVisited    int
	EntriesExamined int
	Visited         []int
	PerLevelNodes   map[int]int
	PerLevelEntries map[int]int
}

func traceFrom(st rtree.QueryStats) QueryTrace {
	return QueryTrace{
		Enabled:         st.Visited != nil || st.PerLevelNodes != nil,
		NodesVisited:    st.NodesVisited,
		EntriesExamined: st.EntriesExamined,
		Visited:         st.Visited,
		PerLevelNodes:   st.PerLevelNodes,
		PerLevelEntries: st.PerLevelEntries,
	}
}

// Store keeps the live dataset and both index dimensionalities in sync.
type Store struct {
	Data *Dataset
	Opts IndexOptions
	T2   *rtree.Tree
	T3   *rtree.Tree
}

// BuildStore indexes the whole dataset. If bulk is true the trees are built
// with STR packing; otherwise every tree is inserted one at a time.
// incrementalOrder shuffles insertion order for the incremental build.
func BuildStore(data *Dataset, opts IndexOptions, bulk bool, orderSeed int64) *Store {
	s := &Store{Data: data, Opts: opts}
	s.T2 = rtree.New(rtree.Options{Dims: 2, MaxEntries: opts.MaxEntries,
		ForceReinsert: opts.ForceReinsert, ReinsertPercent: opts.ReinsertPercent})
	s.T3 = rtree.New(rtree.Options{Dims: 3, MaxEntries: opts.MaxEntries,
		ForceReinsert: opts.ForceReinsert, ReinsertPercent: opts.ReinsertPercent})

	var order []*Tree
	if bulk {
		order = data.SortedLive()
	} else {
		order = data.Shuffled(orderSeed)
	}

	if bulk {
		items2 := make([]rtree.BulkItem, len(order))
		items3 := make([]rtree.BulkItem, len(order))
		for i, t := range order {
			items2[i] = rtree.BulkItem{Rect: t.BBox2, Key: int64(t.ID), Data: t}
			items3[i] = rtree.BulkItem{Rect: t.BBox3, Key: int64(t.ID), Data: t}
		}
		s.T2.BulkLoad(items2)
		s.T3.BulkLoad(items3)
		return s
	}
	for _, t := range order {
		s.T2.Insert(t.BBox2, int64(t.ID), t)
		s.T3.Insert(t.BBox3, int64(t.ID), t)
	}
	return s
}

// Tree returns the index for a dimensionality.
func (s *Store) Tree(dims uint8) *rtree.Tree {
	if dims == 3 {
		return s.T3
	}
	return s.T2
}

// Insert indexes a tree that was already added to the dataset.
func (s *Store) Insert(t *Tree) {
	s.T2.Insert(t.BBox2, int64(t.ID), t)
	s.T3.Insert(t.BBox3, int64(t.ID), t)
}

// Remove deletes a tree from both indexes and the dataset.
func (s *Store) Remove(id int) *Tree {
	t := s.Data.Remove(id)
	if t == nil {
		return nil
	}
	s.T2.Delete(t.BBox2, int64(id))
	s.T3.Delete(t.BBox3, int64(id))
	return t
}

// Stats is the JSON-friendly structural summary of one tree.
type LevelStatsJSON struct {
	Level    int     `json:"level"`
	Nodes    int     `json:"nodes"`
	Entries  int     `json:"entries"`
	Coverage float64 `json:"coverage"`
	Overlap  float64 `json:"overlap"`
	NodeArea float64 `json:"nodeArea"`
	// Footprint is the summed x-y extent of the children's boxes, the useful
	// coverage measure when the third dimension is a survey date.
	Footprint float64 `json:"footprint"`
}

// StatsJSON is a structural summary.
type StatsJSON struct {
	Dims            uint8            `json:"dims"`
	MaxEntries      int              `json:"maxEntries"`
	MinEntries      int              `json:"minEntries"`
	ForceReinsert   bool             `json:"forceReinsert"`
	Height          int              `json:"height"`
	NodeCount       int              `json:"nodeCount"`
	LeafCount       int              `json:"leafCount"`
	ItemCount       int              `json:"itemCount"`
	TotalOverlap    float64          `json:"totalOverlap"`
	TotalCoverage   float64          `json:"totalCoverage"`
	ReinsertOps     int64            `json:"reinsertOps"`
	ReinsertEntries int64            `json:"reinsertEntries"`
	Splits          int64            `json:"splits"`
	CondensedNodes  int64            `json:"condensedNodes"`
	Levels          []LevelStatsJSON `json:"levels"`
}

// Stats produces a summary for the requested dimensionality.
func (s *Store) Stats(dims uint8) StatsJSON {
	tr := s.Tree(dims)
	snap := tr.Snapshot()
	out := StatsJSON{
		Dims:            dims,
		MaxEntries:      s.Opts.MaxEntries,
		MinEntries:      tr.MinEntries(),
		ForceReinsert:   s.Opts.ForceReinsert,
		Height:          snap.Height,
		NodeCount:       snap.NodeCount,
		LeafCount:       snap.LeafCount,
		ItemCount:       snap.ItemCount,
		TotalOverlap:    snap.TotalOverlap,
		TotalCoverage:   snap.TotalCoverage,
		ReinsertOps:     snap.ReinsertOps,
		ReinsertEntries: snap.ReinsertEntries,
		Splits:          snap.Splits,
		CondensedNodes:  snap.CondensedNodes,
	}
	for _, l := range snap.Levels {
		ls := LevelStatsJSON{
			Level: l.Level, Nodes: l.Nodes, Entries: l.Entries,
			Coverage: l.Coverage, Overlap: l.Overlap, NodeArea: l.NodeArea,
		}
		if dims == 3 {
			for _, n := range tr.Nodes() {
				if n.Level != l.Level {
					continue
				}
				for i := range n.Entries {
					ls.Footprint += n.Entries[i].Rect.Len(0) * n.Entries[i].Rect.Len(1)
				}
			}
		} else {
			ls.Footprint = l.Coverage
		}
		out.Levels = append(out.Levels, ls)
	}
	return out
}

// QueryRange runs a 2-D range query. It returns tree IDs sorted ascending,
// plus the nodes visited.
func (s *Store) QueryRange(q geom.Box, trace bool) ([]int, QueryTrace) {
	tr := s.T2
	tr.SetTrace(trace)
	ids := make([]int, 0, 32)
	st := tr.Search(q, func(e rtree.Entry) { ids = append(ids, int(e.Key)) })
	ids = sortInts(ids)
	return ids, traceFrom(st)
}

// QueryRange3 runs a 3-D range query.
func (s *Store) QueryRange3(q geom.Box, trace bool) ([]int, QueryTrace) {
	tr := s.T3
	tr.SetTrace(trace)
	ids := make([]int, 0, 32)
	st := tr.Search(q, func(e rtree.Entry) { ids = append(ids, int(e.Key)) })
	ids = sortInts(ids)
	return ids, traceFrom(st)
}

// QueryPoly runs the exact polygon test over candidates from the index.
func (s *Store) QueryPoly(line *RedLine, trace bool) ([]int, QueryTrace) {
	tr := s.T2
	tr.SetTrace(trace)
	box := line.Rect()
	out := make([]int, 0, 16)
	st := tr.Search(box, func(e rtree.Entry) {
		t := e.Data.(*Tree)
		if geom.PolyBoxIntersects(t.Crown, box) {
			out = append(out, t.ID)
		}
	})
	out = sortInts(out)
	return out, traceFrom(st)
}

// NearItemJSON is one nearest-neighbour hit in the store's response.
type NearItemJSON struct {
	ID   int     `json:"id"`
	Dist float64 `json:"dist"`
}

// QueryNearest returns up to k nearest trees around p. maxDist <= 0 means
// unbounded. iterLimit > 0 drains the iterator that many times instead of
// stopping at k, exercising the "keep pulling in distance order" mode.
func (s *Store) QueryNearest(dims uint8, p [3]float64, k int, maxDist float64, iterLimit int, trace bool) ([]NearItemJSON, QueryTrace) {
	tr := s.Tree(dims)
	tr.SetTrace(trace)
	it := tr.Nearest(p, maxDist)
	limit := k
	if iterLimit > 0 {
		limit = iterLimit
	}
	if limit > 0 {
		it.SetLimit(limit)
	}
	capHint := limit
	if capHint < 0 || capHint > 1024 {
		capHint = 1024
	}
	out := make([]NearItemJSON, 0, capHint)
	for len(out) < limit {
		d, key, _, ok := it.Next()
		if !ok {
			break
		}
		out = append(out, NearItemJSON{ID: int(key), Dist: math.Sqrt(d)})
	}
	return out, traceFrom(it.Stats)
}

func sortInts(a []int) []int {
	if len(a) < 2 {
		return a
	}
	quickSortInts(a)
	return a
}

func quickSortInts(a []int) {
	// insertion + shell style; results sets are small
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}
