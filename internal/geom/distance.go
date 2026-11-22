package geom

import "math"

// DistancePointSegment is the euclidean distance from p to segment ab.
func DistancePointSegment(p, a, b Point) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	t := ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / l2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(p.X-(a.X+t*dx), p.Y-(a.Y+t*dy))
}

// DistanceToPolygon is 0 when p is inside the polygon, otherwise the minimum
// distance to its boundary.
func DistanceToPolygon(p Point, poly Polygon) float64 {
	n := poly.RingLen()
	if n == 0 {
		return math.Inf(1)
	}
	if n >= 3 && poly.ContainsPoint(p) {
		return 0
	}
	best := math.Inf(1)
	for i := 0; i < n; i++ {
		d := DistancePointSegment(p, poly.Ring[i], poly.Ring[(i+1)%n])
		if d < best {
			best = d
		}
	}
	return best
}

// DistToRectSq is the squared distance from p to the closed rect.
func DistToRectSq(p Point, r Rect) float64 {
	var s float64
	if p.X < r.Min(0) {
		d := r.Min(0) - p.X
		s += d * d
	} else if p.X > r.Max(0) {
		d := p.X - r.Max(0)
		s += d * d
	}
	if p.Y < r.Min(1) {
		d := r.Min(1) - p.Y
		s += d * d
	} else if p.Y > r.Max(1) {
		d := p.Y - r.Max(1)
		s += d * d
	}
	return s
}
