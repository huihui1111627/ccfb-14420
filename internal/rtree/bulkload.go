package rtree

import (
	"math"
	"sort"

	"ancienttrees/internal/geom"
)

// BulkLoadItem is one record for STR bulk loading.
type BulkLoadItem struct {
	Rect geom.Rect
	Data any
}

// BulkLoad discards the current contents and rebuilds the tree with
// Sort-Tile-Recursive packing.
//
// With L = ceil(N/M) leaf nodes, the items are sorted along the first axis and
// cut into ceil(L^(1/D)) slabs; each slab is sorted along the next axis and
// cut again, recursively, until a slab fits in one node. The leaf MBRs are
// then packed into parents in the same order, bottom-up. The resulting tree is
// balanced and usually much more compact than repeated insertion.
func (t *Tree) BulkLoad(items []BulkLoadItem) {
	t.Root = nil
	t.recordCount = 0
	if len(items) == 0 {
		return
	}
	dim := t.cfg.Dim
	work := make([]BulkLoadItem, len(items))
	copy(work, items)

	chunks := strChunks(work, 0, dim, t.cfg.MaxEntries)

	level := make([]*Node, 0, len(chunks))
	for _, chunk := range chunks {
		n := t.newNode(true, 0)
		n.Entries = make([]Entry, 0, len(chunk))
		for _, it := range chunk {
			n.Entries = append(n.Entries, Entry{Rect: it.Rect, Data: it.Data})
		}
		n.Bounds = computeBounds(n.Entries, dim)
		level = append(level, n)
	}

	for len(level) > 1 {
		nextLevel := level[0].Level + 1
		// Tile the child MBRs along the spatial axes with the same STR
		// recursion used for the leaves, so each parent covers a compact
		// spatial slab rather than a run of the previous level's order.
		wrapped := make([]BulkLoadItem, len(level))
		for i, c := range level {
			wrapped[i] = BulkLoadItem{Rect: c.Bounds, Data: i}
		}
		// strChunks stops as soon as a group fits in one node; because each
		// child MBR is far larger than a tree crown, going straight to
		// MaxEntries-sized chunks keeps parents compact.
		chunks := strChunks(wrapped, 0, dim, t.cfg.MaxEntries)
		parents := make([]*Node, 0, len(chunks))
		for _, chunk := range chunks {
			p := t.newNode(false, nextLevel)
			p.Entries = make([]Entry, 0, len(chunk))
			for _, it := range chunk {
				c := level[it.Data.(int)]
				c.Parent = p
				p.Entries = append(p.Entries, Entry{child: c})
			}
			p.Bounds = computeBounds(p.Entries, dim)
			parents = append(parents, p)
		}
		level = parents
	}
	t.Root = level[0]
	t.Root.Parent = nil
	t.recordCount = len(items)
	t.Stats.BulkLoads.Add(1)
	t.Stats.Inserted.Add(int64(len(items)))
}

// strChunks recursively tiles items along axes starting at axis.
func strChunks(items []BulkLoadItem, axis, dim, max int) [][]BulkLoadItem {
	n := len(items)
	if n <= max {
		sortByRemaining(items, axis, dim)
		return [][]BulkLoadItem{items}
	}
	// Number of slabs along this axis. With `remaining` axes left to tile,
	// each slab can hold roughly groups^(1/remaining) nodes, so taking that
	// many slabs leaves every slab within reach of the remaining axes.
	groups := (n + max - 1) / max
	remaining := dim - axis
	slabs := int(math.Floor(math.Pow(float64(groups), 1/float64(remaining))))
	if slabs < 1 {
		slabs = 1
	}
	if slabs > groups {
		slabs = groups
	}
	sortByAxis(items, axis)
	nextAxis := axis + 1
	if nextAxis >= dim {
		nextAxis = 0
	}
	var out [][]BulkLoadItem
	start := 0
	for k := 0; k < slabs; k++ {
		rem := n - start
		remSlabs := slabs - k
		size := (rem + remSlabs - 1) / remSlabs
		end := start + size
		if end > n {
			end = n
		}
		if end == start {
			continue
		}
		out = append(out, strChunks(items[start:end], nextAxis, dim, max)...)
		start = end
	}
	return out
}

func sortByAxis(items []BulkLoadItem, axis int) {
	if len(items) == 0 || items[0].Rect.D <= axis {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Rect, items[j].Rect
		if a.Min(axis) != b.Min(axis) {
			return a.Min(axis) < b.Min(axis)
		}
		return a.Max(axis) < b.Max(axis)
	})
}

func sortByRemaining(items []BulkLoadItem, fromAxis, dim int) {
	if len(items) == 0 {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Rect, items[j].Rect
		for ax := fromAxis; ax < dim; ax++ {
			if a.Min(ax) != b.Min(ax) {
				return a.Min(ax) < b.Min(ax)
			}
			if a.Max(ax) != b.Max(ax) {
				return a.Max(ax) < b.Max(ax)
			}
		}
		return false
	})
}

// bulkLevelGroups partitions node indexes into compact spatial groups for one
// parent level. It is exported for tests so grouping quality can be measured
// without going through the whole tree.
func bulkLevelGroups(rects []geom.Rect, max int, dim int) [][]int {
	items := make([]BulkLoadItem, len(rects))
	for i, r := range rects {
		items[i] = BulkLoadItem{Rect: r, Data: i}
	}
	chunks := strChunks(items, 0, dim, max)
	out := make([][]int, 0, len(chunks))
	for _, c := range chunks {
		idx := make([]int, 0, len(c))
		for _, it := range c {
			idx = append(idx, it.Data.(int))
		}
		out = append(out, idx)
	}
	return out
}
