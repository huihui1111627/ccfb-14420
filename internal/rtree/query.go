package rtree

import (
	"container/heap"
	"math"
	"sort"

	"ancienttrees/internal/geom"
)

// Visit is one node touched by a query. Depth is the depth at which it was
// reached; Hits is the number of matching records found below the node during
// the query. Visits are appended in the order nodes were expanded.
type Visit struct {
	ID     int       `json:"id"`
	Level  int       `json:"level"`
	Depth  int       `json:"depth"`
	Bounds geom.Rect `json:"bounds"`
	Hits   int       `json:"hits"`
}

// SearchResult holds the records whose MBR intersects the query rect.
type SearchResult struct {
	Data  []any
	Rect  []geom.Rect
	Trace []Visit // nil unless tracing was enabled
	// NodesVisited counts the index nodes whose entries were inspected.
	NodesVisited int
	// LeafEntries is the number of leaf records tested after the MBR pass.
	LeafEntries int
}

// VisitOption configures a query.
type VisitOption struct {
	Trace bool
	// Filter is applied to leaf records after the MBR test; it may be nil.
	Filter func(data any, rect geom.Rect) bool
}

func (t *Tree) recordQuery(nodes, leaves, results int) {
	t.Stats.Queries.Add(1)
	t.Stats.NodesVisited.Add(int64(nodes))
	t.Stats.LeafEntries.Add(int64(leaves))
	t.Stats.Results.Add(int64(results))
}

// Search returns every record whose bounding rect intersects rect, in
// depth-first order.
//
// When tracing is enabled the visited nodes are appended in expansion order.
// Each visit records the node id, level and depth, plus Hits: the number of
// matching records found below that node during the query. A node whose MBR
// does not intersect is never visited, so the trace shows exactly what the
// query pruned.
func (t *Tree) Search(rect geom.Rect, opt *VisitOption) SearchResult {
	var res SearchResult
	if t.Root == nil {
		t.recordQuery(0, 0, 0)
		return res
	}
	var trace bool
	var filter func(any, geom.Rect) bool
	if opt != nil {
		trace = opt.Trace
		filter = opt.Filter
	}

	// visit recursively appends the trace entry for n before descending, then
	// fills in Hits on the way back up. The trace is in pre-order and the
	// depth-first expansion order matches the recursion order.
	var visit func(n *Node, depth int) int
	visit = func(n *Node, depth int) int {
		res.NodesVisited++
		vi := -1
		if trace {
			vi = len(res.Trace)
			res.Trace = append(res.Trace, Visit{
				ID: n.ID, Level: n.Level, Depth: depth, Bounds: n.Bounds,
			})
		}
		hits := 0
		if n.Leaf {
			for i := range n.Entries {
				e := n.Entries[i]
				if !e.Rect.Intersects(rect) {
					continue
				}
				res.LeafEntries++
				if filter != nil && !filter(e.Data, e.Rect) {
					continue
				}
				res.Data = append(res.Data, e.Data)
				res.Rect = append(res.Rect, e.Rect)
				hits++
			}
		} else {
			for i := range n.Entries {
				e := n.Entries[i]
				if !e.Bounds().Intersects(rect) {
					continue
				}
				hits += visit(e.child, depth+1)
			}
		}
		if trace {
			res.Trace[vi].Hits = hits
		}
		return hits
	}
	visit(t.Root, 0)
	t.recordQuery(res.NodesVisited, res.LeafEntries, len(res.Data))
	return res
}

// SearchRect is a convenience wrapper returning only the data.
func (t *Tree) SearchRect(rect geom.Rect) []any {
	return t.Search(rect, nil).Data
}

// NearestHit is one result of a nearest-neighbour query.
type NearestHit struct {
	Data any
	Rect geom.Rect
	Dist float64 // minimum euclidean distance from the query point to Rect
}

// hitsHeap orders hits worst-first (max-heap on distance) when used as the
// bounded buffer of the k-NN scan.
type hitsHeap []NearestHit

func (h hitsHeap) Len() int           { return len(h) }
func (h hitsHeap) Less(i, j int) bool { return h[i].Dist > h[j].Dist }
func (h hitsHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitsHeap) Push(x any)        { *h = append(*h, x.(NearestHit)) }
func (h *hitsHeap) Pop() any          { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

// knnItem is a node in the best-first queue.
type knnItem struct {
	n    *Node
	dist float64
}

type knnQueue []knnItem

func (q knnQueue) Len() int           { return len(q) }
func (q knnQueue) Less(i, j int) bool { return q[i].dist < q[j].dist }
func (q knnQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *knnQueue) Push(x any)        { *q = append(*q, x.(knnItem)) }
func (q *knnQueue) Pop() any          { old := *q; n := len(old); x := old[n-1]; *q = old[:n-1]; return x }

// KNN returns the k records nearest to p, sorted by increasing distance. Nodes
// are visited in best-first order by minimum distance from p to their MBR; the
// scan stops as soon as the closest unexplored node is farther than the k-th
// best record.
func (t *Tree) KNN(p []float64, k int, opt *VisitOption) ([]NearestHit, SearchResult) {
	var res SearchResult
	if k <= 0 || t.Root == nil {
		t.recordQuery(0, 0, 0)
		return nil, res
	}
	var trace bool
	var filter func(any, geom.Rect) bool
	if opt != nil {
		trace = opt.Trace
		filter = opt.Filter
	}
	best := &hitsHeap{}
	heap.Init(best)
	pq := &knnQueue{{t.Root, t.Root.Bounds.MinDistPoint(p)}}
	heap.Init(pq)
	for pq.Len() > 0 {
		it := heap.Pop(pq).(knnItem)
		if best.Len() == k && it.dist > (*best)[0].Dist {
			break // nothing left can improve on the current k-th result
		}
		n := it.n
		res.NodesVisited++
		if trace {
			res.Trace = append(res.Trace, Visit{ID: n.ID, Level: n.Level, Bounds: n.Bounds})
		}
		if n.Leaf {
			for i := range n.Entries {
				e := n.Entries[i]
				d := e.Rect.MinDistPoint(p)
				if best.Len() == k && d > (*best)[0].Dist {
					continue
				}
				res.LeafEntries++
				if filter != nil && !filter(e.Data, e.Rect) {
					continue
				}
				heap.Push(best, NearestHit{Data: e.Data, Rect: e.Rect, Dist: d})
				if best.Len() > k {
					heap.Pop(best)
				}
			}
			continue
		}
		for i := range n.Entries {
			e := n.Entries[i]
			d := e.Bounds().MinDistPoint(p)
			if best.Len() == k && d > (*best)[0].Dist {
				continue
			}
			heap.Push(pq, knnItem{e.child, d})
		}
	}
	out := make([]NearestHit, best.Len())
	copy(out, *best)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Dist < out[j].Dist })
	t.recordQuery(res.NodesVisited, res.LeafEntries, len(out))
	return out, res
}

// KNNIterator yields nearest records in increasing distance order. Each Next
// resumes the best-first search where the previous call stopped, so the caller
// can take the first k results or keep pulling indefinitely.
//
// The iterator state is a node queue ordered by minimum possible distance and
// a heap of already-found records. The next record is the smallest found
// record once its distance is no greater than the closest unexplored node.
type KNNIterator struct {
	t     *Tree
	p     []float64
	opt   *VisitOption
	pq    *knnQueue
	found *hitMinHeap
	done  bool
	// Last is the record returned by the most recent Next.
	Last NearestHit
	// Result accumulates the visit statistics of the query. Its Trace, when
	// requested, lists nodes in best-first expansion order and its
	// NodesVisited/LeafEntries cover only the part of the query that ran
	// before Next returned false or Finish was called.
	Result SearchResult
	// Emitted is the number of Next calls that returned true.
	Emitted int
}

type hitMinHeap []NearestHit

func (h hitMinHeap) Len() int           { return len(h) }
func (h hitMinHeap) Less(i, j int) bool { return h[i].Dist < h[j].Dist }
func (h hitMinHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitMinHeap) Push(x any)        { *h = append(*h, x.(NearestHit)) }
func (h *hitMinHeap) Pop() any          { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

// NewKNNIterator starts a streaming nearest-neighbour query from p.
func (t *Tree) NewKNNIterator(p []float64, opt *VisitOption) *KNNIterator {
	it := &KNNIterator{t: t, p: p, opt: opt, pq: &knnQueue{}, found: &hitMinHeap{}}
	heap.Init(it.pq)
	heap.Init(it.found)
	if t.Root == nil {
		it.done = true
		return it
	}
	heap.Push(it.pq, knnItem{t.Root, t.Root.Bounds.MinDistPoint(p)})
	return it
}

func (it *KNNIterator) expandUntilDecided() bool {
	var trace bool
	var filter func(any, geom.Rect) bool
	if it.opt != nil {
		trace = it.opt.Trace
		filter = it.opt.Filter
	}
	for {
		if it.found.Len() > 0 {
			if it.pq.Len() == 0 || (*it.found)[0].Dist <= (*it.pq)[0].dist {
				return true
			}
		} else if it.pq.Len() == 0 {
			return false
		}
		item := heap.Pop(it.pq).(knnItem)
		n := item.n
		it.Result.NodesVisited++
		if trace {
			it.Result.Trace = append(it.Result.Trace, Visit{ID: n.ID, Level: n.Level, Bounds: n.Bounds})
		}
		if n.Leaf {
			for i := range n.Entries {
				e := n.Entries[i]
				it.Result.LeafEntries++
				if filter != nil && !filter(e.Data, e.Rect) {
					continue
				}
				heap.Push(it.found, NearestHit{Data: e.Data, Rect: e.Rect, Dist: e.Rect.MinDistPoint(it.p)})
			}
			continue
		}
		for i := range n.Entries {
			e := n.Entries[i]
			heap.Push(it.pq, knnItem{e.child, e.Bounds().MinDistPoint(it.p)})
		}
	}
}

// Next returns false when the tree is exhausted.
func (it *KNNIterator) Next() bool {
	if it.done {
		return false
	}
	if !it.expandUntilDecided() {
		it.done = true
		it.t.recordQuery(it.Result.NodesVisited, it.Result.LeafEntries, it.Emitted)
		return false
	}
	it.Last = heap.Pop(it.found).(NearestHit)
	it.Emitted++
	return true
}

// LowerBound returns a lower bound on the MBR distance of every record not yet
// emitted. It is the minimum of the closest discovered leaf record and the
// closest unexplored node MBR. Returns false when the iterator is exhausted.
//
// The bound lets callers stop refining exact distances once the k-th exact
// result is strictly closer than anything left in the stream.
func (it *KNNIterator) LowerBound() (float64, bool) {
	if it.done {
		return 0, false
	}
	lb := math.Inf(1)
	has := false
	if it.found.Len() > 0 {
		lb = (*it.found)[0].Dist
		has = true
	}
	if it.pq.Len() > 0 {
		d := (*it.pq)[0].dist
		if !has || d < lb {
			lb = d
		}
		has = true
	}
	return lb, has
}

// Finish records statistics when the iterator is abandoned before exhaustion.
// It is safe to call multiple times.
func (it *KNNIterator) Finish() {
	if !it.done {
		it.done = true
		it.t.recordQuery(it.Result.NodesVisited, it.Result.LeafEntries, it.Emitted)
	}
}
