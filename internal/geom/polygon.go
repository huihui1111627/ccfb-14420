package geom

import (
	"math"
	"math/rand"
	"sort"
)

// Polygon is a simple (non self-intersecting) polygon in two dimensions.
// The ring may be closed (last == first) or open; both are accepted by all
// operations.
type Polygon struct {
	Ring []Point
}

// Point is a 2D coordinate.
type Point struct{ X, Y float64 }

// NewPolygon copies ring into a Polygon.
func NewPolygon(ring []Point) Polygon {
	return Polygon{Ring: append([]Point(nil), ring...)}
}

// RingLen returns the number of vertices, ignoring a repeated closing vertex.
func (p Polygon) RingLen() int {
	n := len(p.Ring)
	if n > 1 && p.Ring[0] == p.Ring[n-1] {
		return n - 1
	}
	return n
}

// Bounds returns the axis-aligned bounding box of the polygon.
func (p Polygon) Bounds() (min, max Point) {
	min = Point{math.Inf(1), math.Inf(1)}
	max = Point{math.Inf(-1), math.Inf(-1)}
	for _, v := range p.Ring {
		if v.X < min.X {
			min.X = v.X
		}
		if v.Y < min.Y {
			min.Y = v.Y
		}
		if v.X > max.X {
			max.X = v.X
		}
		if v.Y > max.Y {
			max.Y = v.Y
		}
	}
	return
}

// BoundingRect returns the bounding box as a 2D geom.Rect.
func (p Polygon) BoundingRect() Rect {
	min, max := p.Bounds()
	return NewRectFromBounds([]float64{min.X, min.Y}, []float64{max.X, max.Y})
}

// Centroid is the area centroid (falls back to the vertex average for
// degenerate rings).
func (p Polygon) Centroid() Point {
	n := p.RingLen()
	if n == 0 {
		return Point{}
	}
	var a, cx, cy float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		cross := p.Ring[i].X*p.Ring[j].Y - p.Ring[j].X*p.Ring[i].Y
		a += cross
		cx += (p.Ring[i].X + p.Ring[j].X) * cross
		cy += (p.Ring[i].Y + p.Ring[j].Y) * cross
	}
	if math.Abs(a) < 1e-12 {
		var sx, sy float64
		for i := 0; i < n; i++ {
			sx += p.Ring[i].X
			sy += p.Ring[i].Y
		}
		return Point{sx / float64(n), sy / float64(n)}
	}
	a *= 0.5
	return Point{cx / (6 * a), cy / (6 * a)}
}

// SignedArea is the shoelace area; positive for counter-clockwise rings.
func (p Polygon) SignedArea() float64 {
	n := p.RingLen()
	var a float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		a += p.Ring[i].X*p.Ring[j].Y - p.Ring[j].X*p.Ring[i].Y
	}
	return a / 2
}

// Area is the absolute area.
func (p Polygon) Area() float64 { return math.Abs(p.SignedArea()) }

// Segment is a 2D line segment.
type Segment struct{ A, B Point }

// Edges lists the polygon edges.
func (p Polygon) Edges() []Segment {
	n := p.RingLen()
	out := make([]Segment, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Segment{p.Ring[i], p.Ring[(i+1)%n]})
	}
	return out
}

// pointInRing is the even-odd ray casting test.
func pointInRing(pt Point, ring []Point) bool {
	n := len(ring)
	if n < 3 {
		return false
	}
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		pi, pj := ring[i], ring[j]
		if (pi.Y > pt.Y) != (pj.Y > pt.Y) {
			xi := (pj.X-pi.X)*(pt.Y-pi.Y)/(pj.Y-pi.Y) + pi.X
			if pt.X < xi {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

// ContainsPoint reports whether pt is inside or on the boundary of p.
func (p Polygon) ContainsPoint(pt Point) bool {
	n := p.RingLen()
	if n < 3 {
		return false
	}
	res := pointInRing(pt, p.Ring[:n])
	if res {
		return true
	}
	for i := 0; i < n; i++ {
		if pointOnSegment(pt, p.Ring[i], p.Ring[(i+1)%n]) {
			return true
		}
	}
	return false
}

func pointOnSegment(p, a, b Point) bool {
	cross := (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
	if math.Abs(cross) > 1e-12 {
		return false
	}
	dot := (p.X-a.X)*(b.X-a.X) + (p.Y-a.Y)*(b.Y-a.Y)
	if dot < 0 {
		return false
	}
	l2 := (b.X-a.X)*(b.X-a.X) + (b.Y-a.Y)*(b.Y-a.Y)
	return dot <= l2
}

func orient(a, b, c Point) float64 {
	return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
}

// segmentsIntersect reports whether closed segments ab and cd meet.
func segmentsIntersect(a, b, c, d Point) bool {
	o1 := orient(a, b, c)
	o2 := orient(a, b, d)
	o3 := orient(c, d, a)
	o4 := orient(c, d, b)
	if ((o1 > 0 && o2 < 0) || (o1 < 0 && o2 > 0)) &&
		((o3 > 0 && o4 < 0) || (o3 < 0 && o4 > 0)) {
		return true
	}
	// Collinear / touching cases.
	eps := 1e-12
	if math.Abs(o1) <= eps && pointOnSegment(c, a, b) {
		return true
	}
	if math.Abs(o2) <= eps && pointOnSegment(d, a, b) {
		return true
	}
	if math.Abs(o3) <= eps && pointOnSegment(a, c, d) {
		return true
	}
	if math.Abs(o4) <= eps && pointOnSegment(b, c, d) {
		return true
	}
	return false
}

// IntersectsPolygon reports whether the two polygons share any point
// (boundaries included).
func (p Polygon) IntersectsPolygon(q Polygon) bool {
	n, m := p.RingLen(), q.RingLen()
	if n < 3 || m < 3 {
		return false
	}
	// Fast reject on bounding boxes.
	pr, qr := p.BoundingRect(), q.BoundingRect()
	if !pr.Intersects(qr) {
		return false
	}
	// Any edge crossing means intersection.
	for i := 0; i < n; i++ {
		a, b := p.Ring[i], p.Ring[(i+1)%n]
		for j := 0; j < m; j++ {
			c, d := q.Ring[j], q.Ring[(j+1)%m]
			if segmentsIntersect(a, b, c, d) {
				return true
			}
		}
	}
	// No crossing: one polygon is fully inside the other if any vertex of
	// one is inside the other.
	if pointInRing(q.Ring[0], p.Ring[:n]) || pointInRing(p.Ring[0], q.Ring[:m]) {
		return true
	}
	// Fully contained but with all vertices outside? Impossible for simple
	// polygons without edge crossings, so we are done.
	return false
}

// IntersectsRect reports whether the polygon overlaps the axis-aligned 2D
// rect (closed). The caller normally pre-filters with the polygon's bounding
// rect.
func (p Polygon) IntersectsRect(r Rect) bool {
	n := p.RingLen()
	if n < 3 {
		return false
	}
	minX, minY, maxX, maxY := r.Min(0), r.Min(1), r.Max(0), r.Max(1)
	corners := [4]Point{{minX, minY}, {maxX, minY}, {maxX, maxY}, {minX, maxY}}
	// Polygon vertex inside rect.
	for i := 0; i < n; i++ {
		v := p.Ring[i]
		if v.X >= minX && v.X <= maxX && v.Y >= minY && v.Y <= maxY {
			return true
		}
	}
	// Rect corner inside polygon.
	for _, c := range corners {
		if pointInRing(c, p.Ring[:n]) {
			return true
		}
	}
	// Edge crossings.
	for i := 0; i < n; i++ {
		a, b := p.Ring[i], p.Ring[(i+1)%n]
		for k := 0; k < 4; k++ {
			if segmentsIntersect(a, b, corners[k], corners[(k+1)%4]) {
				return true
			}
		}
	}
	return false
}

// RandomConvexPolygon samples a convex polygon of n vertices around centre
// with radii in [rMin, rMax], uniformly over angle. Points are CCW.
func RandomConvexPolygon(rng *rand.Rand, centre Point, n int, rMin, rMax float64) Polygon {
	if n < 3 {
		n = 3
	}
	// Sample sorted angles and radii, then take the convex hull to guarantee
	// convexity (the hull of a radial star is still contained in the disc
	// bounds however the hull can drop vertices; we therefore construct a
	// guaranteed-convex ring by sorting angles and using a radius that
	// varies slowly).
	// Spread the vertices around the full circle (with jitter) so the centre
	// is always inside the ring: a fully random set of angles can fall inside
	// one half-plane, giving a crown that does not contain its trunk.
	angles := make([]float64, n)
	jitter := 2 * math.Pi / float64(n) * 0.4
	for i := range angles {
		angles[i] = (float64(i)+rng.Float64()*0.2-0.1)*2*math.Pi/float64(n) + (rng.Float64()*2-1)*jitter
	}
	sort.Float64s(angles)
	pts := make([]Point, n)
	// Choose radii and hull the result. The crown is not required to be
	// convex for the engine, but a convex crown is realistic and makes the
	// polygon test cheap and the comparison robust.
	for i := range pts {
		rr := rMin + rng.Float64()*(rMax-rMin)
		pts[i] = Point{centre.X + rr*math.Cos(angles[i]), centre.Y + rr*math.Sin(angles[i])}
	}
	hull := ConvexHull(pts)
	if len(hull) < 3 {
		// Degenerate sample; fall back to an ellipse-ish triangle.
		hull = []Point{
			{centre.X + rMax, centre.Y},
			{centre.X - rMax*0.5, centre.Y + rMax*0.866},
			{centre.X - rMax*0.5, centre.Y - rMax*0.866},
		}
	}
	return NewPolygon(hull)
}

// ConvexHull returns the counter-clockwise convex hull (monotone chain).
func ConvexHull(pts []Point) []Point {
	if len(pts) < 3 {
		return append([]Point(nil), pts...)
	}
	ps := append([]Point(nil), pts...)
	// Sort by x then y.
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0; j-- {
			if ps[j].X < ps[j-1].X || (ps[j].X == ps[j-1].X && ps[j].Y < ps[j-1].Y) {
				ps[j], ps[j-1] = ps[j-1], ps[j]
			} else {
				break
			}
		}
	}
	cross := func(o, a, b Point) float64 {
		return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X)
	}
	var lower []Point
	for _, p := range ps {
		for len(lower) >= 2 && cross(lower[len(lower)-2], lower[len(lower)-1], p) <= 1e-12 {
			lower = lower[:len(lower)-1]
		}
		lower = append(lower, p)
	}
	var upper []Point
	for i := len(ps) - 1; i >= 0; i-- {
		p := ps[i]
		for len(upper) >= 2 && cross(upper[len(upper)-2], upper[len(upper)-1], p) <= 1e-12 {
			upper = upper[:len(upper)-1]
		}
		upper = append(upper, p)
	}
	lower = lower[:len(lower)-1]
	upper = upper[:len(upper)-1]
	return append(lower, upper...)
}
