// Package rtree implements an R*-tree over D-dimensional axis-aligned boxes.
//
// The implementation is dimension generic: the same insert, split, delete and
// query code works for D=2 (planar crowns) and D=3 (planar crowns plus the
// inspection date as a third axis).
//
// Features required by the ancient-tree survey use case:
//   - R* subtree choice: area enlargement above the leaf-parent level, overlap
//     enlargement when the children are leaves;
//   - forced reinsertion once per level per insertion (farthest entries from
//     the node centre are reinserted);
//   - R* split: axis by margin sum, split point by minimum overlap (area tie
//     break);
//   - condense on underfull deletes: the whole node is dismantled and its
//     entries are reinserted at their original level;
//   - STR (sort-tile-recursive) bulk loading;
//   - range search with an optional trace of visited nodes, and a
//     distance-ordered nearest-neighbour iterator.
package rtree

import (
	"errors"
	"math"
	"sort"
	"sync/atomic"

	"ancienttrees/internal/geom"
)

// Entry is one slot in a node. For a leaf node Data holds the record; for an
// internal node Child points at the subtree. Bounds must be used instead of
// Rect for internal entries.
type Entry struct {
	Rect  geom.Rect
	Data  any
	child *Node
}

// Bounds returns the spatial extent of the entry.
func (e Entry) Bounds() geom.Rect {
	if e.child != nil {
		return e.child.Bounds
	}
	return e.Rect
}

// Child exposes the subtree of an internal entry (nil for leaves).
func (e Entry) Child() *Node { return e.child }

// Node is one R*-tree node. Bounds is always the union of the entry bounds.
type Node struct {
	ID      int
	Leaf    bool
	Level   int // 0 = leaf
	Parent  *Node
	Entries []Entry
	Bounds  geom.Rect
}

// Config controls tree construction.
type Config struct {
	Dim              int
	MaxEntries       int     // M
	MinEntries       int     // m; also the underfull threshold
	ForcedReinsert   bool    // enable R* forced reinsertion
	ReinsertFraction float64 // fraction of M reinserted, default 0.3
}

// DefaultConfig returns the usual R* parameters for the given dimension.
func DefaultConfig(dim int) Config {
	return Config{
		Dim:              dim,
		MaxEntries:       16,
		MinEntries:       8,
		ForcedReinsert:   true,
		ReinsertFraction: 0.3,
	}
}

func (c Config) normalized() (Config, error) {
	if c.Dim < 1 {
		return c, errors.New("rtree: dimension must be >= 1")
	}
	if c.MaxEntries < 3 {
		return c, errors.New("rtree: MaxEntries must be >= 3")
	}
	if c.MinEntries == 0 {
		c.MinEntries = int(math.Round(float64(c.MaxEntries) * 0.4))
	}
	if c.MinEntries < 1 || 2*c.MinEntries > c.MaxEntries+1 {
		return c, errors.New("rtree: MinEntries must satisfy 1 <= 2*MinEntries <= MaxEntries+1")
	}
	if c.ReinsertFraction <= 0 || c.ReinsertFraction >= 1 {
		c.ReinsertFraction = 0.3
	}
	return c, nil
}

// Stats are cumulative counters. Query counters are reset by ResetQueryStats.
type Stats struct {
	Inserted          atomic.Int64
	Deleted           atomic.Int64
	Splits            atomic.Int64
	ReinsertOps       atomic.Int64
	ReinsertedEntries atomic.Int64
	DeletedNodes      atomic.Int64
	Queries           atomic.Int64
	NodesVisited      atomic.Int64
	LeafEntries       atomic.Int64
	Results           atomic.Int64
	BulkLoads         atomic.Int64
}

// StatsSnapshot is a plain, copyable view of Stats.
type StatsSnapshot struct {
	Inserted          int64 `json:"inserted"`
	Deleted           int64 `json:"deleted"`
	Splits            int64 `json:"splits"`
	ReinsertOps       int64 `json:"reinsertOps"`
	ReinsertedEntries int64 `json:"reinsertedEntries"`
	DeletedNodes      int64 `json:"deletedNodes"`
	Queries           int64 `json:"queries"`
	NodesVisited      int64 `json:"nodesVisited"`
	LeafEntries       int64 `json:"leafEntries"`
	Results           int64 `json:"results"`
	BulkLoads         int64 `json:"bulkLoads"`
}

// Snapshot copies the counters.
func (s *Stats) Snapshot() StatsSnapshot {
	return StatsSnapshot{
		Inserted:          s.Inserted.Load(),
		Deleted:           s.Deleted.Load(),
		Splits:            s.Splits.Load(),
		ReinsertOps:       s.ReinsertOps.Load(),
		ReinsertedEntries: s.ReinsertedEntries.Load(),
		DeletedNodes:      s.DeletedNodes.Load(),
		Queries:           s.Queries.Load(),
		NodesVisited:      s.NodesVisited.Load(),
		LeafEntries:       s.LeafEntries.Load(),
		Results:           s.Results.Load(),
		BulkLoads:         s.BulkLoads.Load(),
	}
}

// AvgNodesPerQuery is the mean number of index nodes touched per query.
func (s StatsSnapshot) AvgNodesPerQuery() float64 {
	if s.Queries == 0 {
		return 0
	}
	return float64(s.NodesVisited) / float64(s.Queries)
}

// AvgLeafEntriesPerQuery is the mean number of leaf entries examined per
// query.
func (s StatsSnapshot) AvgLeafEntriesPerQuery() float64 {
	if s.Queries == 0 {
		return 0
	}
	return float64(s.LeafEntries) / float64(s.Queries)
}

// Tree is an R*-tree.
type Tree struct {
	cfg         Config
	Root        *Node
	recordCount int
	nextID      int
	Stats       Stats
}

// New creates an empty tree.
func New(cfg Config) (*Tree, error) {
	c, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	return &Tree{cfg: c}, nil
}

// Config returns the normalised configuration.
func (t *Tree) Config() Config { return t.cfg }

// Dim returns the dimension.
func (t *Tree) Dim() int { return t.cfg.Dim }

// Len returns the number of stored records.
func (t *Tree) Len() int { return t.recordCount }

// Height returns the root level (0 for an empty or single-leaf tree).
func (t *Tree) Height() int {
	if t.Root == nil {
		return 0
	}
	return t.Root.Level
}

// ResetQueryStats zeroes only the query counters, keeping structural counters.
func (t *Tree) ResetQueryStats() {
	t.Stats.Queries.Store(0)
	t.Stats.NodesVisited.Store(0)
	t.Stats.LeafEntries.Store(0)
	t.Stats.Results.Store(0)
}

func (t *Tree) newNode(leaf bool, level int) *Node {
	t.nextID++
	n := &Node{
		ID:     t.nextID,
		Leaf:   leaf,
		Level:  level,
		Bounds: geom.NewRect(t.cfg.Dim),
	}
	return n
}

// Insert adds one record.
func (t *Tree) Insert(rect geom.Rect, data any) {
	if rect.D != t.cfg.Dim {
		panic("rtree: rect dimension mismatch")
	}
	t.recordCount++
	t.Stats.Inserted.Add(1)
	t.insertEntry(Entry{Rect: rect, Data: data}, 0, newInsertCtx())
}

type insertCtx struct {
	reinserted map[int]bool
}

func newInsertCtx() *insertCtx { return &insertCtx{reinserted: make(map[int]bool, 4)} }

// insertEntry attaches e to a node at targetLevel, then resolves overflow and
// updates ancestor MBRs. targetLevel is the level of the receiving node.
func (t *Tree) insertEntry(e Entry, targetLevel int, ctx *insertCtx) {
	if t.Root == nil {
		if targetLevel != 0 {
			panic("rtree: cannot insert at non-leaf level into empty tree")
		}
		t.Root = t.newNode(true, 0)
	}
	if targetLevel > t.Root.Level {
		panic("rtree: target level above root")
	}
	n := t.chooseSubtree(e.Bounds(), targetLevel)
	if e.child != nil {
		e.child.Parent = n
	}
	n.Entries = append(n.Entries, e)
	n.Bounds.Extend(e.Bounds())
	t.resolveOverflow(n, ctx)
}

// chooseSubtree descends to a node at targetLevel.
//
// R* rule: above the leaf-parent level choose the child requiring the least
// area enlargement; when the children are leaves choose the child requiring
// the least overlap enlargement (ties: area enlargement, then area). The
// caller passes the entry's effective bounds, so internal reinsertions (whose
// Entry.Rect is unset) are placed correctly.
func (t *Tree) chooseSubtree(rect geom.Rect, targetLevel int) *Node {
	n := t.Root
	for n.Level > targetLevel {
		best := 0
		if n.Level == 1 {
			bestOverlap := math.Inf(1)
			bestEnlarge := math.Inf(1)
			bestArea := math.Inf(1)
			for i := range n.Entries {
				e := n.Entries[i]
				er := e.Bounds()
				enl := er.Enlargement(rect)
				area := er.Area()
				var ov float64
				for j := range n.Entries {
					if j == i {
						continue
					}
					// R* overlap enlargement: the overlap this child's MBR
					// would have with each sibling if grown to cover rect.
					ov += er.EnlargedOverlap(rect, n.Entries[j].Bounds()) - er.IntersectionArea(n.Entries[j].Bounds())
				}
				if ov < bestOverlap ||
					(ov == bestOverlap && enl < bestEnlarge) ||
					(ov == bestOverlap && enl == bestEnlarge && area < bestArea) {
					best = i
					bestOverlap, bestEnlarge, bestArea = ov, enl, area
				}
			}
		} else {
			bestEnlarge := math.Inf(1)
			bestArea := math.Inf(1)
			for i := range n.Entries {
				e := n.Entries[i]
				er := e.Bounds()
				enl := er.Enlargement(rect)
				area := er.Area()
				if enl < bestEnlarge || (enl == bestEnlarge && area < bestArea) {
					best = i
					bestEnlarge, bestArea = enl, area
				}
			}
		}
		n = n.Entries[best].child
	}
	return n
}

// resolveOverflow handles an overfull node and then repairs bounds upwards.
func (t *Tree) resolveOverflow(n *Node, ctx *insertCtx) {
	for {
		n.Bounds = computeBounds(n.Entries, t.cfg.Dim)
		if len(n.Entries) <= t.cfg.MaxEntries {
			t.syncUp(n)
			return
		}
		if t.tryReinsert(n, ctx) {
			t.syncUp(n)
			return
		}
		sib := t.split(n)
		t.Stats.Splits.Add(1)
		if n.Parent == nil {
			// Split the root: grow one level.
			root := t.newNode(false, n.Level+1)
			n.Parent = root
			sib.Parent = root
			root.Entries = []Entry{{child: n}, {child: sib}}
			root.Bounds = computeBounds(root.Entries, t.cfg.Dim)
			t.Root = root
			return
		}
		p := n.Parent
		p.Entries = append(p.Entries, Entry{child: sib})
		sib.Parent = p
		p.Bounds.Extend(sib.Bounds)
		n = p
	}
}

// tryReinsert performs R* forced reinsertion at most once per level in a
// single top-level insertion. It removes the entries farthest from the node
// centre and reinserts them at the same level.
func (t *Tree) tryReinsert(n *Node, ctx *insertCtx) bool {
	if !t.cfg.ForcedReinsert || n.Parent == nil || ctx.reinserted[n.Level] {
		return false
	}
	k := int(math.Round(t.cfg.ReinsertFraction * float64(t.cfg.MaxEntries)))
	if k < 1 {
		k = 1
	}
	if k > len(n.Entries)-1 {
		k = len(n.Entries) - 1
	}
	if k < 1 {
		return false
	}
	ctx.reinserted[n.Level] = true

	centre := n.Bounds.Center()
	dist := make([]float64, len(n.Entries))
	idx := make([]int, len(n.Entries))
	for i := range n.Entries {
		b := n.Entries[i].Bounds()
		var s float64
		for d := 0; d < t.cfg.Dim; d++ {
			mid := (b.Min(d) + b.Max(d)) / 2
			s += (mid - centre[d]) * (mid - centre[d])
		}
		dist[i] = s
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return dist[idx[a]] > dist[idx[b]] })

	remove := make([]bool, len(n.Entries))
	removed := make([]Entry, 0, k)
	for i := 0; i < k; i++ {
		remove[idx[i]] = true
		removed = append(removed, n.Entries[idx[i]])
	}
	kept := n.Entries[:0]
	for i, e := range n.Entries {
		if !remove[i] {
			kept = append(kept, e)
		}
	}
	n.Entries = kept
	n.Bounds = computeBounds(n.Entries, t.cfg.Dim)

	t.Stats.ReinsertOps.Add(1)
	t.Stats.ReinsertedEntries.Add(int64(k))

	// Parent bounds must be consistent before the recursive inserts query the
	// tree.
	t.syncUp(n)
	for _, e := range removed {
		if e.child != nil {
			e.child.Parent = nil
		}
		t.insertEntry(e, n.Level, ctx)
	}
	return true
}

// syncUp recomputes the MBR of n and every ancestor.
func (t *Tree) syncUp(n *Node) {
	for cur := n; cur != nil; cur = cur.Parent {
		cur.Bounds = computeBounds(cur.Entries, t.cfg.Dim)
	}
}

func computeBounds(entries []Entry, dim int) geom.Rect {
	b := geom.NewRect(dim)
	for i := range entries {
		b.Extend(entries[i].Bounds())
	}
	return b
}

// sortedIdx returns indices ordered by the lower (byMax=false) or upper bound
// on axis.
func sortedIdx(entries []Entry, axis int, byMax bool) []int {
	idx := make([]int, len(entries))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra := entries[idx[a]].Bounds()
		rb := entries[idx[b]].Bounds()
		if byMax {
			return ra.Max(axis) < rb.Max(axis)
		}
		return ra.Min(axis) < rb.Min(axis)
	})
	return idx
}

// marginSum evaluates all legal two-way distributions of the entries in the
// given order and returns the sum of the margins of both groups.
func marginSum(entries []Entry, idx []int, minFill int) float64 {
	m := len(entries)
	sum := 0.0
	for k := minFill; k <= m-minFill; k++ {
		g1 := geom.NewRect(entries[0].Bounds().D)
		for _, i := range idx[:k] {
			g1.Extend(entries[i].Bounds())
		}
		g2 := geom.NewRect(entries[0].Bounds().D)
		for _, i := range idx[k:] {
			g2.Extend(entries[i].Bounds())
		}
		sum += g1.Margin() + g2.Margin()
	}
	return sum
}

// split implements the R* split and returns the new sibling of n. n keeps the
// first group.
func (t *Tree) split(n *Node) *Node {
	entries := n.Entries
	m := len(entries)
	minFill := t.cfg.MinEntries
	if 2*minFill > m {
		minFill = m / 2
	}
	if minFill < 1 {
		minFill = 1
	}
	dim := t.cfg.Dim

	// 1. Split axis: minimise the sum of group margins over both sort orders.
	// The chosen order is kept so the split points come from the same
	// distributions that produced the winning margin (R* specification).
	bestAxis := 0
	bestByMax := false
	bestMargin := math.Inf(1)
	for axis := 0; axis < dim; axis++ {
		for _, byMax := range []bool{false, true} {
			idx := sortedIdx(entries, axis, byMax)
			ms := marginSum(entries, idx, minFill)
			if ms < bestMargin {
				bestMargin = ms
				bestAxis = axis
				bestByMax = byMax
			}
		}
	}

	// 2. Split index: minimise overlap of the two groups; ties by total area.
	bestOverlap := math.Inf(1)
	bestArea := math.Inf(1)
	var bestIdx []int
	bestK := minFill
	{
		idx := sortedIdx(entries, bestAxis, bestByMax)
		for k := minFill; k <= m-minFill; k++ {
			g1 := geom.NewRect(dim)
			for _, i := range idx[:k] {
				g1.Extend(entries[i].Bounds())
			}
			g2 := geom.NewRect(dim)
			for _, i := range idx[k:] {
				g2.Extend(entries[i].Bounds())
			}
			ov := g1.IntersectionArea(g2)
			area := g1.Area() + g2.Area()
			if bestIdx == nil || ov < bestOverlap || (ov == bestOverlap && area < bestArea) {
				bestOverlap, bestArea = ov, area
				bestIdx, bestK = idx, k
			}
		}
	}

	sibling := t.newNode(n.Leaf, n.Level)
	g1 := make([]Entry, 0, bestK)
	g2 := make([]Entry, 0, m-bestK)
	for _, i := range bestIdx[:bestK] {
		g1 = append(g1, entries[i])
	}
	for _, i := range bestIdx[bestK:] {
		g2 = append(g2, entries[i])
	}
	n.Entries = g1
	sibling.Entries = g2
	for i := range n.Entries {
		if n.Entries[i].child != nil {
			n.Entries[i].child.Parent = n
		}
	}
	for i := range sibling.Entries {
		if sibling.Entries[i].child != nil {
			sibling.Entries[i].child.Parent = sibling
		}
	}
	n.Bounds = computeBounds(n.Entries, dim)
	sibling.Bounds = computeBounds(sibling.Entries, dim)
	return sibling
}

// DeleteFirst removes the first record whose bounding rect equals rect and
// whose data is accepted by match. The whole tree is searched only along
// overlapping branches. It returns true when a record was removed.
func (t *Tree) DeleteFirst(rect geom.Rect, match func(data any) bool) bool {
	if t.Root == nil {
		return false
	}
	var target *Node
	targetIdx := -1
	var dfs func(n *Node) bool
	dfs = func(n *Node) bool {
		if n.Leaf {
			for i := range n.Entries {
				e := n.Entries[i]
				if e.Rect.Equal(rect) && (match == nil || match(e.Data)) {
					target = n
					targetIdx = i
					return true
				}
			}
			return false
		}
		for i := range n.Entries {
			e := n.Entries[i]
			if e.Bounds().Intersects(rect) && dfs(e.child) {
				return true
			}
		}
		return false
	}
	if !dfs(t.Root) {
		return false
	}
	target.Entries = append(target.Entries[:targetIdx], target.Entries[targetIdx+1:]...)
	t.recordCount--
	t.Stats.Deleted.Add(1)
	t.condense(target)
	return true
}

// reinsertItem is an entry waiting to be put back at a given level.
type reinsertItem struct {
	entry Entry
	level int
}

// condense removes underfull nodes all the way up (R* condense), collecting
// their entries for reinsertion at the corresponding level.
func (t *Tree) condense(n *Node) {
	var pending []reinsertItem
	cur := n
	for cur != nil {
		t.syncUp(cur)
		if cur == t.Root {
			break
		}
		if len(cur.Entries) >= t.cfg.MinEntries {
			break
		}
		p := cur.Parent
		for _, e := range cur.Entries {
			if e.child != nil {
				e.child.Parent = nil
			}
			pending = append(pending, reinsertItem{entry: e, level: cur.Level})
		}
		// Detach cur from its parent.
		for j := range p.Entries {
			if p.Entries[j].child == cur {
				p.Entries = append(p.Entries[:j], p.Entries[j+1:]...)
				break
			}
		}
		cur.Parent = nil
		cur.Entries = nil
		t.Stats.DeletedNodes.Add(1)
		cur = p
	}

	// Collapse a root with a single child, and drop empty roots.
	for t.Root != nil && !t.Root.Leaf && len(t.Root.Entries) == 1 {
		t.Root = t.Root.Entries[0].child
		t.Root.Parent = nil
	}
	if t.Root != nil && len(t.Root.Entries) == 0 {
		t.Root = nil
	}
	if t.Root != nil {
		t.syncUp(t.Root)
	}

	ctx := newInsertCtx()
	for _, it := range pending {
		t.insertEntry(it.entry, it.level, ctx)
	}
}

// Clear empties the tree but keeps configuration and stats.
func (t *Tree) Clear() {
	t.Root = nil
	t.recordCount = 0
}
