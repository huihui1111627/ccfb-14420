package app

import (
	"sort"

	"ancienttrees/internal/geom"
)

// FlatRectHit is a brute-force range hit.
type FlatRectHit struct {
	ID   int
	Rect geom.Box
}

// BruteRect returns every live tree whose 2-D bounding box intersects q,
// sorted by id. This is the reference answer for 2-D range queries.
func (d *Dataset) BruteRect(q geom.Box) []FlatRectHit {
	qlo0, qhi0 := q.Lo[0], q.Hi[0]
	qlo1, qhi1 := q.Lo[1], q.Hi[1]
	out := make([]FlatRectHit, 0, 32)
	for i := range d.flat {
		r := &d.flat[i]
		if r.hi[0] < qlo0 || r.lo[0] > qhi0 || r.hi[1] < qlo1 || r.lo[1] > qhi1 {
			continue
		}
		out = append(out, FlatRectHit{ID: int(r.id), Rect: d.Trees[int(r.id)].BBox2})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BruteBox3 returns every live tree whose 3-D box (x,y,date) intersects q.
func (d *Dataset) BruteBox3(q geom.Box) []FlatRectHit {
	qlo0, qhi0 := q.Lo[0], q.Hi[0]
	qlo1, qhi1 := q.Lo[1], q.Hi[1]
	qlo2, qhi2 := q.Lo[2], q.Hi[2]
	out := make([]FlatRectHit, 0, 32)
	for i := range d.flat {
		r := &d.flat[i]
		if r.hi[0] < qlo0 || r.lo[0] > qhi0 ||
			r.hi[1] < qlo1 || r.lo[1] > qhi1 ||
			r.date < qlo2 || r.date > qhi2 {
			continue
		}
		out = append(out, FlatRectHit{ID: int(r.id), Rect: d.Trees[int(r.id)].BBox3})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BrutePolyHits returns the trees whose exact canopy polygon intersects the
// given box, sorted by id.
func (d *Dataset) BrutePolyHits(box geom.Box) []int {
	out := make([]int, 0, 16)
	for i := range d.flat {
		r := &d.flat[i]
		if r.hi[0] < box.Lo[0] || r.lo[0] > box.Hi[0] ||
			r.hi[1] < box.Lo[1] || r.lo[1] > box.Hi[1] {
			continue
		}
		t := d.Trees[int(r.id)]
		if geom.PolyBoxIntersects(t.Crown, box) {
			out = append(out, t.ID)
		}
	}
	sort.Ints(out)
	return out
}

// BruteRectIDs is BruteRect returning only ids.
func (d *Dataset) BruteRectIDs(q geom.Box) []int {
	hits := d.BruteRect(q)
	out := make([]int, len(hits))
	for i := range hits {
		out[i] = hits[i].ID
	}
	return out
}

// BruteBox3IDs is BruteBox3 returning only ids.
func (d *Dataset) BruteBox3IDs(q geom.Box) []int {
	hits := d.BruteBox3(q)
	out := make([]int, len(hits))
	for i := range hits {
		out[i] = hits[i].ID
	}
	return out
}

// BruteNearestIDs is BruteNearest returning only ids. k < 0 means all within
// maxDist; maxDist <= 0 means unbounded.
func (d *Dataset) BruteNearestIDs(p [3]float64, k int, maxDist float64, dims uint8) []int {
	hits := d.BruteNearest(p, k, maxDist, dims)
	out := make([]int, len(hits))
	for i := range hits {
		out[i] = hits[i].ID
	}
	return out
}

// BruteNearest returns up to k trees nearest to p by box distance, ordered by
// (distance, id). maxDist <= 0 means unbounded. dims selects the boxes used.
func (d *Dataset) BruteNearest(p [3]float64, k int, maxDist float64, dims uint8) []NearHit {
	if k >= 0 {
		return d.bruteNearestTopK(p, k, maxDist, dims)
	}
	hits := make([]NearHit, 0, 64)
	limit := maxDist * maxDist
	for i := range d.flat {
		r := &d.flat[i]
		dist := pointBoxDistSq(p, r, dims)
		if maxDist > 0 && dist > limit {
			continue
		}
		hits = append(hits, NearHit{ID: int(r.id), DistSq: dist})
	}
	sortNear(hits)
	return hits
}

// bruteNearestTopK scans once while keeping only the k best in a max-heap, so
// the reference cost does not grow with the result-set size of a full sort.
func (d *Dataset) bruteNearestTopK(p [3]float64, k int, maxDist float64, dims uint8) []NearHit {
	if k <= 0 {
		return nil
	}
	limit := maxDist * maxDist
	h := nearMaxHeap{}
	for i := range d.flat {
		r := &d.flat[i]
		dist := pointBoxDistSq(p, r, dims)
		if maxDist > 0 && dist > limit {
			continue
		}
		h.push(nearHeapItem{dist, int(r.id)})
		if len(h) > k {
			h.popMax()
		}
	}
	out := make([]NearHit, len(h))
	for i := range h {
		out[i] = NearHit{ID: h[i].id, DistSq: h[i].dist}
	}
	sortNear(out)
	return out
}

func pointBoxDistSq(p [3]float64, r *flatRec, dims uint8) float64 {
	dist := 0.0
	for d := 0; d < int(dims); d++ {
		var v, lo, hi float64
		if d == 2 {
			v, lo, hi = p[d], r.date, r.date
		} else {
			v, lo, hi = p[d], r.lo[d], r.hi[d]
		}
		delta := 0.0
		if v < lo {
			delta = lo - v
		} else if v > hi {
			delta = v - hi
		}
		dist += delta * delta
	}
	return dist
}

func sortNear(hits []NearHit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].DistSq != hits[j].DistSq {
			return hits[i].DistSq < hits[j].DistSq
		}
		return hits[i].ID < hits[j].ID
	})
}

type nearHeapItem struct {
	dist float64
	id   int
}

// nearMaxHeap keeps the worst of the current best-k at index 0.
type nearMaxHeap []nearHeapItem

func (h nearMaxHeap) worse(a, b nearHeapItem) bool {
	if a.dist != b.dist {
		return a.dist > b.dist
	}
	return a.id > b.id
}

func (h *nearMaxHeap) push(x nearHeapItem) {
	*h = append(*h, x)
	i := len(*h) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !h.worse((*h)[i], (*h)[parent]) {
			break
		}
		(*h)[i], (*h)[parent] = (*h)[parent], (*h)[i]
		i = parent
	}
}

func (h *nearMaxHeap) popMax() {
	old := *h
	n := len(old) - 1
	old[0] = old[n]
	*h = old[:n]
	i := 0
	for {
		l, r2 := 2*i+1, 2*i+2
		worst := i
		if l < len(*h) && h.worse((*h)[l], (*h)[worst]) {
			worst = l
		}
		if r2 < len(*h) && h.worse((*h)[r2], (*h)[worst]) {
			worst = r2
		}
		if worst == i {
			return
		}
		(*h)[i], (*h)[worst] = (*h)[worst], (*h)[i]
		i = worst
	}
}

// NearHit is a brute-force nearest result.
type NearHit struct {
	ID     int
	DistSq float64
}
