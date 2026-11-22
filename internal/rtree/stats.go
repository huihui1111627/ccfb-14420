package rtree

import (
	"fmt"
	"sort"

	"ancienttrees/internal/geom"
)

// Validate checks the structural invariants of the tree and returns a list of
// violations. An empty list means the tree is consistent.
func (t *Tree) Validate() []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	if t.Root == nil {
		if t.recordCount != 0 {
			add("empty tree but recordCount=%d", t.recordCount)
		}
		return problems
	}
	if t.Root.Parent != nil {
		add("root has a parent")
	}

	ids := make(map[int]bool)
	total := 0
	var walk func(n *Node, depth int) int
	walk = func(n *Node, depth int) int {
		if ids[n.ID] {
			add("duplicate node id %d", n.ID)
		}
		ids[n.ID] = true
		if n.Parent != nil && n.Parent.Level != n.Level+1 {
			add("node %d level %d has parent level %d", n.ID, n.Level, n.Parent.Level)
		}
		if n.Level == 0 && !n.Leaf {
			add("node %d level 0 but not leaf", n.ID)
		}
		if n.Level > 0 && n.Leaf {
			add("node %d leaf at level %d", n.ID, n.Level)
		}
		if n.Parent != nil && len(n.Entries) < t.cfg.MinEntries {
			add("node %d underfull: %d < %d", n.ID, len(n.Entries), t.cfg.MinEntries)
		}
		if len(n.Entries) > t.cfg.MaxEntries {
			add("node %d overfull: %d > %d", n.ID, len(n.Entries), t.cfg.MaxEntries)
		}
		computed := computeBounds(n.Entries, t.cfg.Dim)
		if len(n.Entries) > 0 && !computed.Equal(n.Bounds) {
			add("node %d bounds mismatch: stored %v want %v", n.ID, rectDebug(n.Bounds), rectDebug(computed))
		}
		if n.Level > 0 {
			childHeight := -1
			for _, e := range n.Entries {
				if e.child == nil {
					add("internal node %d has non-child entry", n.ID)
					continue
				}
				if e.child.Parent != n {
					add("entry in node %d has child %d with wrong parent", n.ID, e.child.ID)
				}
				h := walk(e.child, depth+1)
				if childHeight == -1 {
					childHeight = h
				} else if h != childHeight {
					add("unbalanced tree at node %d: subtree heights %d vs %d", n.ID, childHeight, h)
				}
			}
			if childHeight >= 0 {
				return childHeight + 1
			}
			return 0
		}
		for _, e := range n.Entries {
			if e.child != nil {
				add("leaf node %d has child entry", n.ID)
			}
			if e.Rect.D != t.cfg.Dim {
				add("leaf entry dim %d want %d", e.Rect.D, t.cfg.Dim)
			}
			total++
		}
		return 0
	}
	walk(t.Root, 0)

	if total != t.recordCount {
		add("record count mismatch: tree %d, counter %d", total, t.recordCount)
	}
	return problems
}

func rectDebug(r geom.Rect) string {
	s := "["
	for i := 0; i < r.D; i++ {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%.4g..%.4g", r.Min(i), r.Max(i))
	}
	return s + "]"
}

// LevelStats describes one level of the tree.
type LevelStats struct {
	Level   int `json:"level"`
	Nodes   int `json:"nodes"`
	Entries int `json:"entries"`
	// CoverArea is the measure of the union of all node MBRs on the level
	// (m^2 in 2D, m^2*ms in 3D).
	CoverArea float64 `json:"coverArea"`
	// OverlapArea is the sum, over all unordered pairs of distinct nodes on
	// the level, of the measure of their MBR intersection. This is the
	// quantity R* split and subtree choice try to keep small.
	OverlapArea float64 `json:"overlapArea"`
	// OverlapRatio is OverlapArea / CoverArea.
	OverlapRatio float64 `json:"overlapRatio"`
	// MeanOverlapPerNode is the average, over nodes, of the area of the node
	// covered by at least one other node's MBR: (sum of pair overlaps * 2) /
	// nodes.
	MeanOverlapPerNode float64 `json:"meanOverlapPerNode"`
	// SiblingOverlapArea restricts the pair sum to nodes sharing a parent.
	SiblingOverlapArea float64 `json:"siblingOverlapArea"`
	AvgFill            float64 `json:"avgFill"`
	MinFill            int     `json:"minFill"`
	MaxFill            int     `json:"maxFill"`
}

// OverlapStats is whole-tree coverage/overlap information.
type OverlapStats struct {
	Levels []LevelStats `json:"levels"`
	// TotalOverlapArea is the sum of pairwise MBR overlaps over all levels;
	// TotalCoverArea is the sum of each level's union area.
	TotalOverlapArea float64 `json:"totalOverlapArea"`
	TotalCoverArea   float64 `json:"totalCoverArea"`
	Nodes            int     `json:"nodes"`
	Leaves           int     `json:"leaves"`
	Height           int     `json:"height"`
	Records          int     `json:"records"`
	// FillFactor is the mean leaf fill relative to MaxEntries.
	FillFactor float64 `json:"fillFactor"`
	// Projected is true when the tree has more than two dimensions: the
	// coverage and overlap measures are then computed on the spatial
	// projection (axes 0 and 1), dropping the time axis, because a union of
	// point-like time slabs has zero measure in 3D.
	Projected bool `json:"projected"`
}

// ComputeOverlapStats walks the tree and measures per-level coverage and
// overlap. It is O(sum levelNodes^2) because it measures all pairs; it is a
// diagnostic, not part of the query path.
func (t *Tree) ComputeOverlapStats() OverlapStats {
	var st OverlapStats
	st.Records = t.recordCount
	if t.Root == nil {
		return st
	}
	st.Height = t.Root.Level
	if t.cfg.Dim > 2 {
		st.Projected = true
	}
	byLevel := make(map[int][]*Node)
	var collect func(n *Node)
	collect = func(n *Node) {
		st.Nodes++
		if n.Leaf {
			st.Leaves++
		}
		byLevel[n.Level] = append(byLevel[n.Level], n)
		if !n.Leaf {
			for _, e := range n.Entries {
				collect(e.child)
			}
		}
	}
	collect(t.Root)

	for level := 0; level <= st.Height; level++ {
		nodes := byLevel[level]
		if len(nodes) == 0 {
			continue
		}
		ls := LevelStats{Level: level, Nodes: len(nodes), MinFill: 1 << 30}
		entries := 0
		for _, n := range nodes {
			entries += len(n.Entries)
			if len(n.Entries) > ls.MaxFill {
				ls.MaxFill = len(n.Entries)
			}
			if len(n.Entries) < ls.MinFill || ls.MinFill == 1<<30 {
				ls.MinFill = len(n.Entries)
			}
		}
		ls.Entries = entries
		ls.AvgFill = float64(entries) / float64(len(nodes))
		ls.CoverArea = unionNodesMeasure(nodes, t.cfg.Dim)

		var pairOverlap, siblingOverlap float64
		for i := 0; i < len(nodes); i++ {
			for j := i + 1; j < len(nodes); j++ {
				a := intersectionMeasure(nodes[i].Bounds, nodes[j].Bounds, t.cfg.Dim)
				if a <= 0 {
					continue
				}
				pairOverlap += a
				if nodes[i].Parent != nil && nodes[i].Parent == nodes[j].Parent {
					siblingOverlap += a
				}
			}
		}
		ls.OverlapArea = pairOverlap
		ls.SiblingOverlapArea = siblingOverlap
		if ls.CoverArea > 0 {
			ls.OverlapRatio = pairOverlap / ls.CoverArea
		}
		if len(nodes) > 0 {
			// Each pair overlap contributes to the covered area of both
			// members.
			ls.MeanOverlapPerNode = 2 * pairOverlap / float64(len(nodes))
		}
		st.Levels = append(st.Levels, ls)
		st.TotalOverlapArea += pairOverlap
		st.TotalCoverArea += ls.CoverArea
	}
	if len(st.Levels) > 0 {
		leaf := st.Levels[0]
		st.FillFactor = leaf.AvgFill / float64(t.cfg.MaxEntries)
	}
	return st
}

// intersectionMeasure is the area of the intersection of two rects on the
// spatial axes. Only the first two axes are used when dim>2, matching
// unionNodesMeasure and ComputeOverlapStats.Projected, so coverage and
// overlap stay in the same units.
func intersectionMeasure(a, b geom.Rect, dim int) float64 {
	axes := dim
	if axes > 2 {
		axes = 2
	}
	m := 1.0
	for i := 0; i < axes; i++ {
		lo := a.Min(i)
		if b.Min(i) > lo {
			lo = b.Min(i)
		}
		hi := a.Max(i)
		if b.Max(i) < hi {
			hi = b.Max(i)
		}
		if hi <= lo {
			return 0
		}
		m *= hi - lo
	}
	return m
}

// unionNodesMeasure is the exact measure of the union of node MBRs. In 3D the
// projection onto the spatial axes is measured (see Projected).
func unionNodesMeasure(nodes []*Node, dim int) float64 {
	if len(nodes) == 0 {
		return 0
	}
	rects := make([]geom.Rect, len(nodes))
	for i, n := range nodes {
		rects[i] = n.Bounds
	}
	return unionRectMeasure2D(rects)
}

// unionRectMeasure2D computes the area of the union of the rects' projections
// onto axes 0 and 1 with a sweep line: sort the x events, maintain the union
// length of the active y intervals, and multiply the length by the swept x
// width. Coincident x events are processed before the next strip is measured.
func unionRectMeasure2D(rects []geom.Rect) float64 {
	type edge struct {
		x   float64
		lo  float64
		hi  float64
		add bool
	}
	edges := make([]edge, 0, 2*len(rects))
	for _, r := range rects {
		lo, hi := r.Min(1), r.Max(1)
		if hi <= lo {
			continue
		}
		edges = append(edges, edge{r.Min(0), lo, hi, true})
		edges = append(edges, edge{r.Max(0), lo, hi, false})
	}
	if len(edges) == 0 {
		return 0
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].x != edges[j].x {
			return edges[i].x < edges[j].x
		}
		// Remove before add at the same x so zero-width strips contribute
		// nothing and touching edges do not create phantom coverage.
		return !edges[i].add && edges[j].add
	})
	var active []interval
	var area, prevX float64
	for i := 0; i < len(edges); {
		x := edges[i].x
		if i > 0 {
			area += (x - prevX) * unionLength(active)
		}
		// Apply every event at this x before the next strip is measured.
		for i < len(edges) && edges[i].x == x {
			e := edges[i]
			if e.add {
				active = append(active, interval{e.lo, e.hi})
			} else {
				removeInterval(&active, e.lo, e.hi)
			}
			i++
		}
		prevX = x
	}
	return area
}

func removeInterval(active *[]interval, lo, hi float64) {
	a := *active
	for i, v := range a {
		if v.lo == lo && v.hi == hi {
			a[i] = a[len(a)-1]
			*active = a[:len(a)-1]
			return
		}
	}
}

type interval struct{ lo, hi float64 }

func unionLength(ivs []interval) float64 {
	if len(ivs) == 0 {
		return 0
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].lo < ivs[j].lo })
	total := 0.0
	curLo, curHi := ivs[0].lo, ivs[0].hi
	for _, v := range ivs[1:] {
		if v.lo > curHi {
			total += curHi - curLo
			curLo, curHi = v.lo, v.hi
		} else if v.hi > curHi {
			curHi = v.hi
		}
	}
	return total + curHi - curLo
}
