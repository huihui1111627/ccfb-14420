// Package geom implements fixed-dimension axis-aligned boxes and the exact
// 2-D polygon intersection test used to go from "tree canopy bounding box hit"
// to "construction red line really cuts the canopy".
package geom

import (
	"math"
)

// Box is an axis-aligned bounding box. Dims is configurable (2 = x,y;
// 3 = x,y,z ...). Lo/Hi carry only the first Dim coordinates.
type Box struct {
	Lo   [3]float64
	Hi   [3]float64
	Dims uint8
}

// NewBox builds a box from two arbitrary opposite corners.
func NewBox(dims uint8, a, b [3]float64) Box {
	box := Box{Dims: dims}
	for i := 0; i < int(dims); i++ {
		if a[i] <= b[i] {
			box.Lo[i], box.Hi[i] = a[i], b[i]
		} else {
			box.Lo[i], box.Hi[i] = b[i], a[i]
		}
	}
	return box
}

// Point returns a degenerate box containing p (extra coordinates ignored).
func Point(dims uint8, p [3]float64) Box {
	return NewBox(dims, p, p)
}

func (b Box) dims() int { return int(b.Dims) }

// Union returns the smallest box containing both.
func (b Box) Union(o Box) Box {
	if b.Dims == 0 {
		return o
	}
	if o.Dims == 0 {
		return b
	}
	u := Box{Dims: b.Dims}
	if o.Dims > b.Dims {
		u.Dims = o.Dims
	}
	for i := 0; i < u.dims(); i++ {
		u.Lo[i] = math.Min(b.Lo[i], o.Lo[i])
		u.Hi[i] = math.Max(b.Hi[i], o.Hi[i])
	}
	return u
}

// Area is the hyper-volume (1, 2, 3 or higher dimensional product of sides).
func (b Box) Area() float64 {
	a := 1.0
	for i := 0; i < b.dims(); i++ {
		a *= b.Hi[i] - b.Lo[i]
	}
	return a
}

// Perim is the "surface" measure used for split-axis selection: the sum of
// the products of all edges except each one in turn. In 2-D this is the
// usual perimeter; in 3-D it is half the surface area.
func (b Box) Perim() float64 {
	s := 0.0
	for i := 0; i < b.dims(); i++ {
		p := 1.0
		for j := 0; j < b.dims(); j++ {
			if i != j {
				p *= b.Hi[j] - b.Lo[j]
			}
		}
		s += p
	}
	return s
}

// Len is the extent along axis i.
func (b Box) Len(i int) float64 { return b.Hi[i] - b.Lo[i] }

// Center i.
func (b Box) Center(i int) float64 { return (b.Lo[i] + b.Hi[i]) / 2 }

// Intersects reports whether two boxes overlap (touching counts as overlap).
func (b Box) Intersects(o Box) bool {
	n := b.dims()
	if o.dims() < n {
		n = o.dims()
	}
	for i := 0; i < n; i++ {
		if b.Lo[i] > o.Hi[i] || o.Lo[i] > b.Hi[i] {
			return false
		}
	}
	return true
}

// Overlap returns the hyper-volume of intersection.
func (b Box) Overlap(o Box) float64 {
	n := b.dims()
	if o.dims() < n {
		n = o.dims()
	}
	a := 1.0
	for i := 0; i < n; i++ {
		x := math.Min(b.Hi[i], o.Hi[i]) - math.Max(b.Lo[i], o.Lo[i])
		if x <= 0 {
			return 0
		}
		a *= x
	}
	return a
}

// Enlargement is the growth in Area needed to cover o.
func (b Box) Enlargement(o Box) float64 {
	return b.Union(o).Area() - b.Area()
}

// MinDist is the Euclidean distance from p to the box.
func (b Box) MinDist(p [3]float64) float64 {
	return math.Sqrt(b.MinDistSq(p))
}

// MinDistSq is the squared Euclidean distance from p to the box. Nearest
// neighbour ordering uses this so ties compare bit-for-bit with brute force.
func (b Box) MinDistSq(p [3]float64) float64 {
	s := 0.0
	for i := 0; i < b.dims(); i++ {
		d := 0.0
		if p[i] < b.Lo[i] {
			d = b.Lo[i] - p[i]
		} else if p[i] > b.Hi[i] {
			d = p[i] - b.Hi[i]
		}
		s += d * d
	}
	return s
}

// ToArray copies the box into 3-element arrays for JSON / UI output.
func (b Box) ToArray() (lo, hi [3]float64) { return b.Lo, b.Hi }

// Segment is a 2-D line segment (used for red-line edges).
type Segment struct{ A, B [2]float64 }

// PointInPoly is the even-odd ray casting test. Boundary results are
// unspecified by themselves; exact polygon/box intersection calls it only for
// interior points, with box/edge crossing handled separately.
func PointInPoly(p [2]float64, poly [][2]float64) bool {
	in := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		pi, pj := poly[i], poly[j]
		if (pi[1] > p[1]) != (pj[1] > p[1]) {
			x := (pj[0]-pi[0])*(p[1]-pi[1])/(pj[1]-pi[1]) + pi[0]
			if p[0] < x {
				in = !in
			}
		}
	}
	return in
}

func cross(ax, ay, bx, by float64) float64 { return ax*by - ay*bx }

// segIntersect reports whether segments p1p2 and p3p4 intersect, touching
// included. Collinear overlap is covered by the on-segment tests.
func segIntersect(p1, p2, p3, p4 [2]float64) bool {
	d1 := cross(p4[0]-p3[0], p4[1]-p3[1], p1[0]-p3[0], p1[1]-p3[1])
	d2 := cross(p4[0]-p3[0], p4[1]-p3[1], p2[0]-p3[0], p2[1]-p3[1])
	d3 := cross(p2[0]-p1[0], p2[1]-p1[1], p3[0]-p1[0], p3[1]-p1[1])
	d4 := cross(p2[0]-p1[0], p2[1]-p1[1], p4[0]-p1[0], p4[1]-p1[1])
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	return (d1 == 0 && onSeg(p3, p4, p1)) ||
		(d2 == 0 && onSeg(p3, p4, p2)) ||
		(d3 == 0 && onSeg(p1, p2, p3)) ||
		(d4 == 0 && onSeg(p1, p2, p4))
}

func onSeg(a, b, p [2]float64) bool {
	return math.Min(a[0], b[0]) <= p[0] && p[0] <= math.Max(a[0], b[0]) &&
		math.Min(a[1], b[1]) <= p[1] && p[1] <= math.Max(a[1], b[1])
}

// PolyBoxIntersects is the exact test: true iff the closed polygon and the
// closed 2-D box share at least one point.
func PolyBoxIntersects(poly [][2]float64, b Box) bool {
	if len(poly) == 0 {
		return false
	}
	lo, hi := [2]float64{b.Lo[0], b.Lo[1]}, [2]float64{b.Hi[0], b.Hi[1]}
	// 1. any polygon vertex inside the box
	for _, p := range poly {
		if p[0] >= lo[0] && p[0] <= hi[0] && p[1] >= lo[1] && p[1] <= hi[1] {
			return true
		}
	}
	// 2. any box corner inside the polygon
	corners := [4][2]float64{{lo[0], lo[1]}, {hi[0], lo[1]}, {hi[0], hi[1]}, {lo[0], hi[1]}}
	for _, c := range corners {
		if PointInPoly(c, poly) {
			return true
		}
	}
	// 3. any polygon edge crossing any box edge
	edges := [4][2][2]float64{
		{{lo[0], lo[1]}, {hi[0], lo[1]}},
		{{hi[0], lo[1]}, {hi[0], hi[1]}},
		{{hi[0], hi[1]}, {lo[0], hi[1]}},
		{{lo[0], hi[1]}, {lo[0], lo[1]}},
	}
	n := len(poly)
	for i := 0; i < n; i++ {
		a, b2 := poly[i], poly[(i+1)%n]
		for _, e := range edges {
			if segIntersect(a, b2, e[0], e[1]) {
				return true
			}
		}
	}
	return false
}

// PolyRingArea is the unsigned area of a simple polygon.
func PolyRingArea(poly [][2]float64) float64 {
	s := 0.0
	n := len(poly)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		s += poly[i][0]*poly[j][1] - poly[j][0]*poly[i][1]
	}
	if s < 0 {
		s = -s
	}
	return s / 2
}

// PolyBBox returns the bounding box of a 2-D polygon.
func PolyBBox(poly [][2]float64) Box {
	b := Box{Dims: 2, Lo: [3]float64{math.Inf(1), math.Inf(1), 0}, Hi: [3]float64{math.Inf(-1), math.Inf(-1), 0}}
	for _, p := range poly {
		for i := 0; i < 2; i++ {
			if p[i] < b.Lo[i] {
				b.Lo[i] = p[i]
			}
			if p[i] > b.Hi[i] {
				b.Hi[i] = p[i]
			}
		}
	}
	return b
}
