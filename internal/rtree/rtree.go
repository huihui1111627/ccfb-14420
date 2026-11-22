// Package rtree implements a configurable-dimension R*-tree over
// axis-aligned boxes.
//
// Behaviour that matters for this project:
//
//   - choose-subtree uses area enlargement above leaf level and overlap
//     enlargement on the level immediately above the leaves;
//   - an overflowing node first attempts ONE forced reinsertion per level per
//     insertion, re-inserting the entries farthest from the node centre;
//   - a real split picks the axis with the smallest perimeter sum and then the
//     split index with the smallest overlap (ties broken by area);
//   - deleting an item condenses under-full nodes away entirely and reinserts
//     their entries at the level they came from;
//   - bulk loading packs sorted slices (STR);
//   - nearest-neighbour search is a best-first priority queue that can either
//     stop after k results or be iterated indefinitely in distance order.
package rtree

import (
	"fmt"
	"math"
	"sort"

	"ancienttrees/internal/geom"
)

// Entry is either a leaf item (Child == nil) or a subtree reference.
type Entry struct {
	Rect  geom.Box
	Key   int64
	Data  any
	Child *Node
}

// Node is one tree node. Level 0 is a leaf.
type Node struct {
	ID      int
	Level   int
	Entries []Entry
	Parent  *Node
	Rect    geom.Box
}

func (n *Node) isLeaf() bool { return n.Level == 0 }

func (n *Node) recalc() {
	var r geom.Box
	for i := range n.Entries {
		r = r.Union(n.Entries[i].Rect)
	}
	n.Rect = r
}

// QueryStats describes the work done by one query.
type QueryStats struct {
	NodesVisited    int
	EntriesExamined int
	Visited         []int       // node IDs, only when tracing is enabled
	PerLevelNodes   map[int]int // only when tracing is enabled
	PerLevelEntries map[int]int // only when tracing is enabled
}

// Options tune construction behaviour.
type Options struct {
	Dims            uint8
	MaxEntries      int  // M
	MinEntries      int  // m, defaults to ceil(M/2)
	ForceReinsert   bool // forced reinsertion instead of immediate split
	ReinsertPercent int  // percent of M moved on forced reinsertion
	Trace           bool // record visited node IDs in QueryStats
}

// Tree is the index. It is not safe for concurrent mutation.
type Tree struct {
	opts Options
	Root *Node
	Size int

	nextNodeID int

	ReinsertOps     int64
	ReinsertEntries int64
	Splits          int64
	CondensedNodes  int64

	reinsertDone map[int]bool // per top-level insertion/deletion, keyed by level
}

// New creates an empty tree.
func New(opts Options) *Tree {
	if opts.Dims < 1 || opts.Dims > 3 {
		panic("rtree: dimensions must be 1..3")
	}
	if opts.MaxEntries < 4 {
		opts.MaxEntries = 4
	}
	if opts.MinEntries <= 0 {
		opts.MinEntries = (opts.MaxEntries + 1) / 2
	}
	if opts.MinEntries > opts.MaxEntries/2+1 {
		opts.MinEntries = opts.MaxEntries/2 + 1
	}
	if opts.ReinsertPercent <= 0 {
		opts.ReinsertPercent = 30
	}
	return &Tree{opts: opts}
}

// Dims returns the configured dimensionality.
func (t *Tree) Dims() uint8 { return t.opts.Dims }

// MaxEntries returns M.
func (t *Tree) MaxEntries() int { return t.opts.MaxEntries }

// MinEntries returns m.
func (t *Tree) MinEntries() int { return t.opts.MinEntries }

// SetForceReinsert switches forced reinsertion on or off.
func (t *Tree) SetForceReinsert(on bool) { t.opts.ForceReinsert = on }

// SetTrace toggles visit tracing for subsequent queries.
func (t *Tree) SetTrace(on bool) { t.opts.Trace = on }

func (t *Tree) newNode(level int) *Node {
	t.nextNodeID++
	return &Node{ID: t.nextNodeID, Level: level}
}

// Insert adds a new leaf item.
func (t *Tree) Insert(r geom.Box, key int64, data any) {
	t.reinsertDone = make(map[int]bool)
	t.insertEntry(Entry{Rect: r, Key: key, Data: data}, 0, true)
}

func (t *Tree) insertEntry(e Entry, targetLevel int, countNew bool) {
	if t.Root == nil {
		root := t.newNode(targetLevel)
		root.Entries = []Entry{e}
		root.recalc()
		t.Root = root
		if countNew {
			t.Size++
		}
		return
	}
	if targetLevel > t.Root.Level {
		targetLevel = t.Root.Level
	}
	n := t.chooseSubtree(e.Rect, targetLevel)
	if e.Child != nil {
		e.Child.Parent = n
	}
	n.Entries = append(n.Entries, e)
	if countNew {
		t.Size++
	}
	t.adjustFrom(n, true)
}

// chooseSubtree descends to a node at wantLevel. On the level directly above
// the leaves the child with the least overlap enlargement wins; higher up the
// child with the least area enlargement wins.
func (t *Tree) chooseSubtree(r geom.Box, wantLevel int) *Node {
	n := t.Root
	for n.Level > wantLevel {
		best := 0
		var bestPrim, bestSec, bestThird float64
		var bestArea float64
		bestInit := false
		for i := range n.Entries {
			ch := n.Entries[i].Child
			var prim, sec float64
			if ch.Level == 0 {
				prim = overlapEnlarge(n.Entries, i, r)
				sec = ch.Rect.Enlargement(r)
			} else {
				prim = ch.Rect.Enlargement(r)
				sec = ch.Rect.Area()
			}
			third := float64(len(ch.Entries))
			area := ch.Rect.Area()
			if !bestInit ||
				prim < bestPrim ||
				(prim == bestPrim && sec < bestSec) ||
				(prim == bestPrim && sec == bestSec && third < bestThird) ||
				(prim == bestPrim && sec == bestSec && third == bestThird && area < bestArea) {
				best, bestPrim, bestSec, bestThird, bestArea = i, prim, sec, third, area
				bestInit = true
			}
		}
		n = n.Entries[best].Child
	}
	return n
}

// overlapEnlarge is the increase in total pairwise overlap among siblings if
// entry i were enlarged by r.
func overlapEnlarge(entries []Entry, i int, r geom.Box) float64 {
	old := entries[i].Rect
	grown := old.Union(r)
	delta := 0.0
	for j := range entries {
		if j == i {
			continue
		}
		delta += grown.Overlap(entries[j].Rect) - old.Overlap(entries[j].Rect)
	}
	return delta
}

func (t *Tree) adjustFrom(n *Node, allowReinsert bool) {
	for n != nil {
		n.recalc()
		parent := n.Parent
		if parent != nil {
			parent.entryFor(n).Rect = n.Rect
		}
		if len(n.Entries) <= t.opts.MaxEntries {
			n = parent
			continue
		}

		if allowReinsert && t.opts.ForceReinsert && !t.reinsertDone[n.Level] {
			t.reinsertDone[n.Level] = true
			far := t.extractFar(n)
			if len(far) > 0 {
				t.ReinsertOps++
				t.ReinsertEntries += int64(len(far))
				n.recalc()
				t.refreshUp(n)
				for _, e := range far {
					t.insertEntry(e, n.Level, false)
				}
				return
			}
		}

		sib := t.split(n)
		t.Splits++
		if parent == nil {
			root := t.newNode(n.Level + 1)
			root.Entries = []Entry{
				{Child: n, Rect: n.Rect},
				{Child: sib, Rect: sib.Rect},
			}
			n.Parent = root
			sib.Parent = root
			root.recalc()
			t.Root = root
			return
		}
		sib.Parent = parent
		parent.entryFor(n).Rect = n.Rect
		parent.Entries = append(parent.Entries, Entry{Child: sib, Rect: sib.Rect})
		n = parent
	}
}

// entryFor returns a pointer to the parent's entry referencing child n.
func (n *Node) entryFor(child *Node) *Entry {
	for i := range n.Entries {
		if n.Entries[i].Child == child {
			return &n.Entries[i]
		}
	}
	panic("rtree: child not found in parent")
}

// extractFar removes the entries farthest from the node centre, keeping at
// least m entries behind.
func (t *Tree) extractFar(n *Node) []Entry {
	p := t.opts.MaxEntries * t.opts.ReinsertPercent / 100
	if p < 1 {
		p = 1
	}
	keepMin := t.opts.MinEntries
	if max := len(n.Entries) - keepMin; p > max {
		p = max
	}
	if p <= 0 {
		return nil
	}
	idx := make([]int, len(n.Entries))
	for i := range idx {
		idx[i] = i
	}
	cx := make([]float64, t.opts.Dims)
	for d := range cx {
		cx[d] = n.Rect.Center(d)
	}
	dist := func(e Entry) float64 {
		s := 0.0
		for d := 0; d < len(cx); d++ {
			df := e.Rect.Center(d) - cx[d]
			s += df * df
		}
		return s
	}
	sort.SliceStable(idx, func(a, b int) bool {
		da, db := dist(n.Entries[idx[a]]), dist(n.Entries[idx[b]])
		if da != db {
			return da > db
		}
		return n.Entries[idx[a]].Key > n.Entries[idx[b]].Key
	})
	take := make(map[int]bool, p)
	for _, i := range idx[:p] {
		take[i] = true
	}
	far := make([]Entry, 0, p)
	rest := make([]Entry, 0, len(n.Entries)-p)
	for i, e := range n.Entries {
		if take[i] {
			far = append(far, e)
		} else {
			rest = append(rest, e)
		}
	}
	n.Entries = rest
	return far
}

// split implements R* ChooseSplitAxis / ChooseSplitIndex.
func (t *Tree) split(n *Node) *Node {
	entries := append([]Entry(nil), n.Entries...)
	left, right := chooseSplit(entries, t.opts.MinEntries)
	n.Entries = left
	sib := t.newNode(n.Level)
	sib.Entries = right
	for i := range sib.Entries {
		if sib.Entries[i].Child != nil {
			sib.Entries[i].Child.Parent = sib
		}
	}
	for i := range n.Entries {
		if n.Entries[i].Child != nil {
			n.Entries[i].Child.Parent = n
		}
	}
	n.recalc()
	sib.recalc()
	return sib
}

func bboxOf(entries []Entry) geom.Box {
	var r geom.Box
	for i := range entries {
		r = r.Union(entries[i].Rect)
	}
	return r
}

func chooseSplit(entries []Entry, minFill int) ([]Entry, []Entry) {
	n := len(entries)
	dims := int(entries[0].Rect.Dims)
	loK := minFill
	hiK := n - minFill
	if hiK < loK {
		loK, hiK = n/2, n-n/2
	}

	bestAxis := 0
	bestAxisCost := math.Inf(1)
	for axis := 0; axis < dims; axis++ {
		cost := 0.0
		for _, byUpper := range []bool{false, true} {
			sorted := append([]Entry(nil), entries...)
			if byUpper {
				sort.SliceStable(sorted, func(i, j int) bool {
					if sorted[i].Rect.Hi[axis] != sorted[j].Rect.Hi[axis] {
						return sorted[i].Rect.Hi[axis] < sorted[j].Rect.Hi[axis]
					}
					return sorted[i].Rect.Lo[axis] < sorted[j].Rect.Lo[axis]
				})
			} else {
				sort.SliceStable(sorted, func(i, j int) bool {
					if sorted[i].Rect.Lo[axis] != sorted[j].Rect.Lo[axis] {
						return sorted[i].Rect.Lo[axis] < sorted[j].Rect.Lo[axis]
					}
					return sorted[i].Rect.Hi[axis] < sorted[j].Rect.Hi[axis]
				})
			}
			for k := loK; k <= hiK; k++ {
				a := bboxOf(sorted[:k])
				b := bboxOf(sorted[k:])
				cost += a.Perim() + b.Perim()
			}
		}
		if cost < bestAxisCost {
			bestAxisCost = cost
			bestAxis = axis
		}
	}

	bestOverlap := math.Inf(1)
	bestArea := math.Inf(1)
	var bestLeft, bestRight []Entry
	for _, byUpper := range []bool{false, true} {
		sorted := append([]Entry(nil), entries...)
		if byUpper {
			sort.SliceStable(sorted, func(i, j int) bool {
				if sorted[i].Rect.Hi[bestAxis] != sorted[j].Rect.Hi[bestAxis] {
					return sorted[i].Rect.Hi[bestAxis] < sorted[j].Rect.Hi[bestAxis]
				}
				lo, lj := sorted[i].Rect.Lo[bestAxis], sorted[j].Rect.Lo[bestAxis]
				if lo != lj {
					return lo < lj
				}
				return sorted[i].Key < sorted[j].Key
			})
		} else {
			sort.SliceStable(sorted, func(i, j int) bool {
				if sorted[i].Rect.Lo[bestAxis] != sorted[j].Rect.Lo[bestAxis] {
					return sorted[i].Rect.Lo[bestAxis] < sorted[j].Rect.Lo[bestAxis]
				}
				hi, hj := sorted[i].Rect.Hi[bestAxis], sorted[j].Rect.Hi[bestAxis]
				if hi != hj {
					return hi < hj
				}
				return sorted[i].Key < sorted[j].Key
			})
		}
		for k := loK; k <= hiK; k++ {
			a := bboxOf(sorted[:k])
			b := bboxOf(sorted[k:])
			ov := a.Overlap(b)
			ar := a.Area() + b.Area()
			if ov < bestOverlap || (ov == bestOverlap && ar < bestArea) {
				bestOverlap, bestArea = ov, ar
				bestLeft = append([]Entry(nil), sorted[:k]...)
				bestRight = append([]Entry(nil), sorted[k:]...)
			}
		}
	}
	if bestLeft == nil {
		k := n / 2
		bestLeft = append([]Entry(nil), entries[:k]...)
		bestRight = append([]Entry(nil), entries[k:]...)
	}
	return bestLeft, bestRight
}

// Delete removes the leaf item with the given key whose rect is r.
func (t *Tree) Delete(r geom.Box, key int64) bool {
	leaf, ok := t.findLeaf(t.Root, r, key)
	if !ok {
		return false
	}
	for i := range leaf.Entries {
		if leaf.Entries[i].Key == key && leaf.Entries[i].Child == nil {
			leaf.Entries = append(leaf.Entries[:i], leaf.Entries[i+1:]...)
			t.Size--
			break
		}
	}

	t.reinsertDone = make(map[int]bool)
	t.condense(leaf)
	return true
}

func (t *Tree) findLeaf(n *Node, r geom.Box, key int64) (*Node, bool) {
	if n == nil || !n.Rect.Intersects(r) {
		return nil, false
	}
	if n.isLeaf() {
		for i := range n.Entries {
			if n.Entries[i].Key == key && n.Entries[i].Child == nil {
				return n, true
			}
		}
		return nil, false
	}
	for i := range n.Entries {
		if n.Entries[i].Rect.Intersects(r) {
			if leaf, ok := t.findLeaf(n.Entries[i].Child, r, key); ok {
				return leaf, true
			}
		}
	}
	return nil, false
}

type pendingEntry struct {
	e     Entry
	level int
}

func (t *Tree) condense(leaf *Node) {
	var pending []pendingEntry
	var path []*Node
	cur := leaf
	for cur != nil {
		path = append(path, cur)
		if cur == t.Root {
			break
		}
		parent := cur.Parent
		if len(cur.Entries) < t.opts.MinEntries {
			t.CondensedNodes++
			t.removeChild(parent, cur)
			for _, e := range cur.Entries {
				pending = append(pending, pendingEntry{e: e, level: cur.Level})
			}
			cur.Entries = nil
			cur.Parent = nil
		}
		cur = parent
	}

	if t.Root != nil {
		if t.Root.isLeaf() {
			if len(t.Root.Entries) == 0 {
				t.Root = nil
			}
		} else {
			for len(t.Root.Entries) == 1 && !t.Root.isLeaf() {
				t.Root = t.Root.Entries[0].Child
				t.Root.Parent = nil
			}
			if len(t.Root.Entries) == 0 {
				t.Root = nil
			}
		}
	}

	// Refresh from the deepest node on the path that is still attached to the
	// tree. Nodes below it were dissolved; their entries are in pending.
	if t.Root != nil {
		attached := make(map[*Node]bool, len(path))
		for i := len(path) - 1; i >= 0; i-- {
			n := path[i]
			if n == t.Root {
				attached[n] = true
				continue
			}
			if n.Parent != nil && attached[n.Parent] && n.Parent.findEntry(n) != nil {
				attached[n] = true
			}
		}
		var start *Node
		for _, n := range path {
			if attached[n] {
				start = n
				break
			}
		}
		if start != nil {
			t.refreshUp(start)
		}
	}
	for _, p := range pending {
		t.reinsertPending(p)
	}
}

// refreshUp recomputes n and updates each ancestor's stored entry for it.
func (t *Tree) refreshUp(n *Node) {
	for n != nil {
		p := n.Parent
		if p == nil {
			n.recalc()
			return
		}
		n.recalc()
		p.findEntry(n).Rect = n.Rect
		n = p
	}
}

// reinsertPending puts a condensed entry back at its original level. If the
// tree lost so much height that the level no longer exists, the subtree is
// decomposed into individual items instead so parent/child levels stay
// consistent.
func (t *Tree) reinsertPending(p pendingEntry) {
	if t.Root == nil {
		t.insertEntry(p.e, p.level, false)
		return
	}
	if p.level > t.Root.Level {
		t.decomposeIntoItems(p.e)
		return
	}
	t.insertEntry(p.e, p.level, false)
}

func (t *Tree) decomposeIntoItems(e Entry) {
	if e.Child == nil {
		t.insertEntry(Entry{Rect: e.Rect, Key: e.Key, Data: e.Data}, 0, false)
		return
	}
	for _, ch := range e.Child.Entries {
		t.decomposeIntoItems(ch)
	}
}

func (t *Tree) removeChild(parent, child *Node) {
	if parent == nil {
		return
	}
	for i := range parent.Entries {
		if parent.Entries[i].Child == child {
			parent.Entries = append(parent.Entries[:i], parent.Entries[i+1:]...)
			return
		}
	}
}

// BulkItem is one packed leaf record.
type BulkItem struct {
	Rect geom.Box
	Key  int64
	Data any
}

// BulkLoad builds the tree bottom-up with STR packed slices.
func (t *Tree) BulkLoad(items []BulkItem) {
	t.Root = nil
	t.Size = 0
	t.nextNodeID = 0
	if len(items) == 0 {
		return
	}
	m := t.opts.MaxEntries
	dims := int(t.opts.Dims)

	type nodeRec struct {
		rect  geom.Box
		child *Node
		data  any
		key   int64
	}

	levelRecs := make([]nodeRec, len(items))
	for i := range items {
		levelRecs[i] = nodeRec{rect: items[i].Rect, data: items[i].Data, key: items[i].Key}
	}
	t.Size = len(items)

	level := 0
	for len(levelRecs) > m {
		groups := strPack(levelRecs, dims, m,
			func(r nodeRec) geom.Box { return r.rect },
			func(r nodeRec) [3]float64 {
				var c [3]float64
				for d := 0; d < dims; d++ {
					c[d] = r.rect.Center(d)
				}
				return c
			})
		next := make([]nodeRec, 0, len(groups))
		for _, g := range groups {
			nd := t.newNode(level)
			nd.Entries = make([]Entry, len(g))
			for i, r := range g {
				e := Entry{Rect: r.rect, Key: r.key, Data: r.data, Child: r.child}
				if r.child != nil {
					r.child.Parent = nd
					e.Data = nil
					e.Key = 0
				}
				nd.Entries[i] = e
			}
			nd.recalc()
			next = append(next, nodeRec{rect: nd.Rect, child: nd})
		}
		levelRecs = next
		level++
	}

	root := t.newNode(level)
	root.Entries = make([]Entry, len(levelRecs))
	for i, r := range levelRecs {
		e := Entry{Rect: r.rect, Key: r.key, Data: r.data, Child: r.child}
		if r.child != nil {
			r.child.Parent = root
			e.Data = nil
			e.Key = 0
		}
		root.Entries[i] = e
	}
	root.recalc()
	t.Root = root
}

// strPack packs sorted slices (Sort-Tile-Recursive): sort every record by the
// first axis, cut into ceil(sqrt(groups)) tiles whose sizes are whole numbers
// of m-entry groups, sort each tile by the next axis and pack it into nodes.
// Balanced tile sizes matter because a short trailing tile would leave a node
// under-full.
func strPack[T any](recs []T, dims, m int, rectOf func(T) geom.Box, centerOf func(T) [3]float64) [][]T {
	if len(recs) <= m {
		return [][]T{recs}
	}
	var out [][]T
	strPackInto(recs, dims, 0, m, rectOf, centerOf, &out)
	return out
}

func strPackInto[T any](recs []T, dims, axis, m int, rectOf func(T) geom.Box, centerOf func(T) [3]float64, out *[][]T) {
	if len(recs) <= m {
		*out = append(*out, recs)
		return
	}
	if axis >= dims {
		axis = dims - 1
	}
	sortAxis(recs, dims, axis, centerOf)

	groups := (len(recs) + m - 1) / m
	if groups <= 1 {
		*out = append(*out, recs)
		return
	}
	if axis == dims-1 {
		chunkBalanced(recs, m, out)
		return
	}

	slices := int(math.Ceil(math.Sqrt(float64(groups))))
	if slices > groups {
		slices = groups
	}
	if slices < 1 {
		slices = 1
	}
	base, extra := len(recs)/slices, len(recs)%slices
	start := 0
	for s := 0; s < slices && start < len(recs); s++ {
		size := base
		if s < extra {
			size++
		}
		end := start + size
		strPackInto(recs[start:end], dims, axis+1, m, rectOf, centerOf, out)
		start = end
	}
}

// sortAxis orders records by centre on axis, then the remaining axes.
func sortAxis[T any](recs []T, dims, axis int, centerOf func(T) [3]float64) {
	sort.SliceStable(recs, func(i, j int) bool {
		a, b := centerOf(recs[i]), centerOf(recs[j])
		for d := 0; d < dims; d++ {
			dd := (axis + d) % dims
			if a[dd] != b[dd] {
				return a[dd] < b[dd]
			}
		}
		return false
	})
}

// chunkBalanced splits recs into ceil(len/m) chunks whose sizes differ by at
// most one, so a trailing partial group cannot leave a node under-full.
func chunkBalanced[T any](recs []T, m int, out *[][]T) {
	g := (len(recs) + m - 1) / m
	if g <= 1 {
		*out = append(*out, recs)
		return
	}
	base, extra := len(recs)/g, len(recs)%g
	start := 0
	for i := 0; i < g; i++ {
		size := base
		if i < extra {
			size++
		}
		*out = append(*out, recs[start:start+size])
		start += size
	}
}

// Search reports all leaf items intersecting r.
func (t *Tree) Search(r geom.Box, fn func(Entry)) QueryStats {
	st := QueryStats{}
	if t.opts.Trace {
		st.PerLevelNodes = map[int]int{}
		st.PerLevelEntries = map[int]int{}
	}
	t.search(t.Root, r, fn, &st)
	return st
}

func (t *Tree) search(n *Node, r geom.Box, fn func(Entry), st *QueryStats) {
	if n == nil || !n.Rect.Intersects(r) {
		return
	}
	st.NodesVisited++
	if t.opts.Trace {
		st.Visited = append(st.Visited, n.ID)
		st.PerLevelNodes[n.Level]++
	}
	for i := range n.Entries {
		e := &n.Entries[i]
		st.EntriesExamined++
		if t.opts.Trace {
			st.PerLevelEntries[n.Level]++
		}
		if !e.Rect.Intersects(r) {
			continue
		}
		if e.Child != nil {
			t.search(e.Child, r, fn, st)
		} else {
			fn(*e)
		}
	}
}

type nearItem struct {
	dist  float64
	isSub bool
	entry Entry
	node  *Node
}

type nearHeap []nearItem

func (h nearHeap) Len() int { return len(h) }
func (h nearHeap) Less(i, j int) bool {
	if h[i].dist != h[j].dist {
		return h[i].dist < h[j].dist
	}
	// Expand subtrees before emitting items at the same distance, so a tied
	// item inside a tied subtree is discovered and ordered by key rather than
	// surfacing after its larger-key sibling.
	if h[i].isSub != h[j].isSub {
		return h[i].isSub
	}
	if h[i].isSub {
		return h[i].node.ID < h[j].node.ID
	}
	return h[i].entry.Key < h[j].entry.Key
}
func (h nearHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *nearHeap) Push(x any)   { *h = append(*h, x.(nearItem)) }
func (h *nearHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// NearestIter yields leaf items in non-decreasing distance order.
type NearestIter struct {
	t       *Tree
	q       [3]float64
	maxDist float64
	h       nearHeap
	done    bool
	Stats   QueryStats

	// emitted counts leaf items returned; kLimit > 0 stops expansion as soon
	// as kLimit items have been returned, since any later item is farther.
	emitted int
	kLimit  int
}

// SetLimit makes the iterator stop expanding subtrees once n items have been
// emitted. Results are identical to an unbounded iteration truncated at n;
// it only avoids visiting nodes whose distance can no longer matter.
func (it *NearestIter) SetLimit(n int) { it.kLimit = n }

// Nearest starts a best-first nearest-neighbour iterator.
// maxDist <= 0 means unbounded.
func (t *Tree) Nearest(q [3]float64, maxDist float64) *NearestIter {
	it := &NearestIter{t: t, q: q, maxDist: maxDist}
	if t.opts.Trace {
		it.Stats.PerLevelNodes = map[int]int{}
		it.Stats.PerLevelEntries = map[int]int{}
	}
	if t.Root != nil {
		d := t.Root.Rect.MinDistSq(q)
		if maxDist <= 0 || d <= maxDist*maxDist {
			it.h = append(it.h, nearItem{dist: d, isSub: true, node: t.Root})
		}
	}
	return it
}

// Next returns the next item. ok is false when exhausted or past maxDist.
func (it *NearestIter) Next() (dist float64, key int64, data any, ok bool) {
	for len(it.h) > 0 {
		top := it.h[0]
		if !top.isSub {
			if it.maxDist > 0 && top.dist > it.maxDist*it.maxDist {
				it.done = true
				return 0, 0, nil, false
			}
			heapPop(&it.h)
			it.emitted++
			if it.kLimit > 0 && it.emitted >= it.kLimit {
				it.h = it.h[:0]
			}
			return top.dist, top.entry.Key, top.entry.Data, true
		}
		heapPop(&it.h)
		n := top.node
		it.Stats.NodesVisited++
		if it.t.opts.Trace {
			it.Stats.Visited = append(it.Stats.Visited, n.ID)
			it.Stats.PerLevelNodes[n.Level]++
		}
		for i := range n.Entries {
			e := &n.Entries[i]
			it.Stats.EntriesExamined++
			if it.t.opts.Trace {
				it.Stats.PerLevelEntries[n.Level]++
			}
			d := e.Rect.MinDistSq(it.q)
			if it.maxDist > 0 && d > it.maxDist*it.maxDist {
				continue
			}
			if e.Child != nil {
				heapPush(&it.h, nearItem{dist: d, isSub: true, node: e.Child})
			} else {
				heapPush(&it.h, nearItem{dist: d, entry: *e})
			}
		}
	}
	it.done = true
	return 0, 0, nil, false
}

// NearestK is a convenience wrapper returning at most k results.
func (t *Tree) NearestK(q [3]float64, k int) ([]NearResult, QueryStats) {
	it := t.Nearest(q, 0)
	it.SetLimit(k)
	capHint := k
	if capHint > 1024 {
		capHint = 1024
	}
	out := make([]NearResult, 0, capHint)
	for len(out) < k {
		d, key, data, ok := it.Next()
		if !ok {
			break
		}
		out = append(out, NearResult{Dist: d, Key: key, Data: data})
	}
	return out, it.Stats
}

// NearResult is one nearest-neighbour hit.
type NearResult struct {
	Dist float64
	Key  int64
	Data any
}

// StatsSnapshot summarises the index shape.
type StatsSnapshot struct {
	Height          int
	NodeCount       int
	ItemCount       int
	LeafCount       int
	Levels          []LevelStats
	TotalOverlap    float64
	TotalCoverage   float64
	ReinsertOps     int64
	ReinsertEntries int64
	Splits          int64
	CondensedNodes  int64
}

// LevelStats describes one tree level. Entries at level L are the children of
// nodes at level L (items for L == 0).
type LevelStats struct {
	Level      int
	Nodes      int
	Entries    int
	Coverage   float64 // sum of entry rectangles' area/volume
	Overlap    float64 // sum over nodes of pairwise sibling overlap
	NodeArea   float64 // sum of node rectangles' area/volume
	EmptyNodes int
}

// Snapshot computes structural statistics.
func (t *Tree) Snapshot() StatsSnapshot {
	s := StatsSnapshot{ItemCount: t.Size}
	if t.Root == nil {
		return s
	}
	s.Height = t.Root.Level + 1
	byLevel := map[int]*LevelStats{}
	t.walkNodes(func(n *Node) {
		s.NodeCount++
		ls := byLevel[n.Level]
		if ls == nil {
			ls = &LevelStats{Level: n.Level}
			byLevel[n.Level] = ls
		}
		ls.Nodes++
		ls.NodeArea += n.Rect.Area()
		if len(n.Entries) == 0 {
			ls.EmptyNodes++
		}
		for i := range n.Entries {
			ls.Entries++
			ls.Coverage += n.Entries[i].Rect.Area()
			for j := i + 1; j < len(n.Entries); j++ {
				ls.Overlap += n.Entries[i].Rect.Overlap(n.Entries[j].Rect)
			}
		}
	})
	s.LeafCount = byLevel[0].Nodes
	for lvl := 0; lvl < s.Height; lvl++ {
		ls := byLevel[lvl]
		if ls == nil {
			ls = &LevelStats{Level: lvl}
		}
		s.Levels = append(s.Levels, *ls)
		s.TotalOverlap += ls.Overlap
		s.TotalCoverage += ls.Coverage
	}
	s.ReinsertOps = t.ReinsertOps
	s.ReinsertEntries = t.ReinsertEntries
	s.Splits = t.Splits
	s.CondensedNodes = t.CondensedNodes
	return s
}

// findEntry returns a pointer to the parent's entry referencing child.
func (n *Node) findEntry(child *Node) *Entry {
	for i := range n.Entries {
		if n.Entries[i].Child == child {
			return &n.Entries[i]
		}
	}
	return nil
}

func (t *Tree) walkNodes(fn func(*Node)) {
	var rec func(*Node)
	rec = func(n *Node) {
		fn(n)
		for i := range n.Entries {
			if n.Entries[i].Child != nil {
				rec(n.Entries[i].Child)
			}
		}
	}
	if t.Root != nil {
		rec(t.Root)
	}
}

// Nodes returns every node in the tree.
func (t *Tree) Nodes() []*Node {
	var out []*Node
	t.walkNodes(func(n *Node) { out = append(out, n) })
	return out
}

// Check validates structural invariants and reports the first violation.
func (t *Tree) Check() error {
	if t.Root == nil {
		if t.Size != 0 {
			return fmt.Errorf("empty tree reports size %d", t.Size)
		}
		return nil
	}
	if t.Root.Parent != nil {
		return fmt.Errorf("root has a parent")
	}
	items := 0
	var rec func(n *Node) error
	rec = func(n *Node) error {
		if len(n.Entries) == 0 {
			return fmt.Errorf("node %d level %d has no entries", n.ID, n.Level)
		}
		if n != t.Root && len(n.Entries) < t.opts.MinEntries {
			return fmt.Errorf("node %d level %d under-full: %d < %d", n.ID, n.Level, len(n.Entries), t.opts.MinEntries)
		}
		if n.Level > 0 && len(n.Entries) > t.opts.MaxEntries {
			return fmt.Errorf("node %d level %d over-full: %d > %d", n.ID, n.Level, len(n.Entries), t.opts.MaxEntries)
		}
		want := geom.Box{}
		for i := range n.Entries {
			e := &n.Entries[i]
			want = want.Union(e.Rect)
			if n.Level == 0 {
				if e.Child != nil {
					return fmt.Errorf("leaf entry has child")
				}
				items++
				continue
			}
			ch := e.Child
			if ch == nil {
				return fmt.Errorf("internal node %d has item entry", n.ID)
			}
			if ch.Parent != n {
				return fmt.Errorf("node %d parent pointer wrong", ch.ID)
			}
			if ch.Level != n.Level-1 {
				return fmt.Errorf("node %d level %d under level %d", ch.ID, ch.Level, n.Level)
			}
			if !boxesEqual(ch.Rect, e.Rect) {
				return fmt.Errorf("node %d entry rect != child rect", ch.ID)
			}
			if err := rec(ch); err != nil {
				return err
			}
		}
		if !boxesEqual(want, n.Rect) && len(n.Entries) > 0 {
			return fmt.Errorf("node %d rect not tight", n.ID)
		}
		return nil
	}
	if err := rec(t.Root); err != nil {
		return err
	}
	if items != t.Size {
		return fmt.Errorf("size %d but %d leaf items", t.Size, items)
	}
	return nil
}

func boxesEqual(a, b geom.Box) bool {
	d := a.Dims
	if b.Dims > d {
		d = b.Dims
	}
	for i := 0; i < int(d); i++ {
		if a.Lo[i] != b.Lo[i] || a.Hi[i] != b.Hi[i] {
			return false
		}
	}
	return true
}
