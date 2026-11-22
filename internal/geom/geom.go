// Package geom contains the dimension-generic geometry used by the R*-tree.
//
// Everything is defined over D-dimensional axis-aligned boxes, where D is a
// runtime constant (2 for the spatial index, 3 when the inspection date is
// used as an extra axis). A rectangle is stored in a 2*D float64 array:
//
//	[lo_0, lo_1, ..., lo_{D-1}, hi_0, hi_1, ..., hi_{D-1}]
//
// Rects are treated as closed sets.
package geom

import (
	"math"
	"math/rand"
)

// Rect is a D-dimensional axis-aligned box (closed).
type Rect struct {
	D   int
	min []float64
	max []float64
}

// NewRect allocates a rect initialised to the empty set (+inf / -inf).
func NewRect(d int) Rect {
	r := Rect{D: d, min: make([]float64, d), max: make([]float64, d)}
	for i := 0; i < d; i++ {
		r.min[i] = math.Inf(1)
		r.max[i] = math.Inf(-1)
	}
	return r
}

// NewRectFromBounds builds a rect from min and max slices; the slices are
// copied.
func NewRectFromBounds(min, max []float64) Rect {
	d := len(min)
	r := Rect{D: d, min: append([]float64(nil), min...), max: append([]float64(nil), max...)}
	return r
}

// PointRect builds a degenerate rect (a point) at coords.
func PointRect(coords []float64) Rect {
	d := len(coords)
	return Rect{D: d, min: append([]float64(nil), coords...), max: append([]float64(nil), coords...)}
}

// Clone returns an independent copy.
func (r Rect) Clone() Rect {
	return Rect{D: r.D, min: append([]float64(nil), r.min...), max: append([]float64(nil), r.max...)}
}

// Min returns the i-th low coordinate.
func (r Rect) Min(i int) float64 { return r.min[i] }

// Max returns the i-th high coordinate.
func (r Rect) Max(i int) float64 { return r.max[i] }

// Mins returns the internal min slice (read-only).
func (r Rect) Mins() []float64 { return r.min }

// Maxs returns the internal max slice (read-only).
func (r Rect) Maxs() []float64 { return r.max }

// IsEmpty reports whether the rect contains no point.
func (r Rect) IsEmpty() bool {
	for i := 0; i < r.D; i++ {
		if r.min[i] > r.max[i] {
			return true
		}
	}
	return false
}

// Extend grows r to cover o.
func (r *Rect) Extend(o Rect) {
	for i := 0; i < r.D; i++ {
		if o.min[i] < r.min[i] {
			r.min[i] = o.min[i]
		}
		if o.max[i] > r.max[i] {
			r.max[i] = o.max[i]
		}
	}
}

// ExtendAll grows r to cover every rect in os.
func (r *Rect) ExtendAll(os []Rect) {
	for _, o := range os {
		r.Extend(o)
	}
}

// Intersects reports whether r and o share at least one point.
func (r Rect) Intersects(o Rect) bool {
	for i := 0; i < r.D; i++ {
		if r.max[i] < o.min[i] || o.max[i] < r.min[i] {
			return false
		}
	}
	return true
}

// ContainsPoint reports whether p lies inside r.
func (r Rect) ContainsPoint(p []float64) bool {
	for i := 0; i < r.D; i++ {
		if p[i] < r.min[i] || p[i] > r.max[i] {
			return false
		}
	}
	return true
}

// ContainsRect reports whether r fully covers o.
func (r Rect) ContainsRect(o Rect) bool {
	for i := 0; i < r.D; i++ {
		if o.min[i] < r.min[i] || o.max[i] > r.max[i] {
			return false
		}
	}
	return true
}

// Area is the product of the side lengths; zero for degenerate rects.
func (r Rect) Area() float64 {
	a := 1.0
	for i := 0; i < r.D; i++ {
		e := r.max[i] - r.min[i]
		if e <= 0 {
			return 0
		}
		a *= e
	}
	return a
}

// Margin is the perimeter in 2D (2*(w+h)), the perimeter of the enclosing
// box in 3D (4*(w+h+d)) for "3D perimeter", and 2*D*side generally.
func (r Rect) Margin() float64 {
	s := 0.0
	for i := 0; i < r.D; i++ {
		e := r.max[i] - r.min[i]
		if e > 0 {
			s += e
		}
	}
	return 2 * float64(r.D) * s
}

// AreaWithUnion returns Area(r union o) in O(D), which is more accurate than
// computing the union rect first.
func (r Rect) AreaWithUnion(o Rect) float64 {
	a := 1.0
	for i := 0; i < r.D; i++ {
		lo := math.Min(r.min[i], o.min[i])
		hi := math.Max(r.max[i], o.max[i])
		e := hi - lo
		if e <= 0 {
			return 0
		}
		a *= e
	}
	return a
}

// Enlargement is the increase in area needed to cover o.
func (r Rect) Enlargement(o Rect) float64 {
	return r.AreaWithUnion(o) - r.Area()
}

// IntersectionArea is the area of the overlap of r and o.
func (r Rect) IntersectionArea(o Rect) float64 {
	a := 1.0
	for i := 0; i < r.D; i++ {
		lo := math.Max(r.min[i], o.min[i])
		hi := math.Min(r.max[i], o.max[i])
		e := hi - lo
		if e <= 0 {
			return 0
		}
		a *= e
	}
	return a
}

// EnlargedOverlap is overlap(r, o) after r has been grown to cover grow, i.e.
// the overlap between (r union grow) and o. This is the criterion used when
// choosing a subtree with the overlap heuristic. The naive computation
// AreaWithUnion(grow).IntersectionArea(o) would force an allocation per
// candidate, so it is done directly.
func (r Rect) EnlargedOverlap(grow, o Rect) float64 {
	a := 1.0
	for i := 0; i < r.D; i++ {
		lo := math.Max(math.Min(r.min[i], grow.min[i]), o.min[i])
		hi := math.Min(math.Max(r.max[i], grow.max[i]), o.max[i])
		e := hi - lo
		if e <= 0 {
			return 0
		}
		a *= e
	}
	return a
}

// Center returns the coordinates of the box centre.
func (r Rect) Center() []float64 {
	c := make([]float64, r.D)
	for i := 0; i < r.D; i++ {
		c[i] = (r.min[i] + r.max[i]) / 2
	}
	return c
}

// MinDistPoint is the minimum euclidean distance from the point p to r;
// zero when p is inside.
func (r Rect) MinDistPoint(p []float64) float64 {
	s := 0.0
	for i := 0; i < r.D; i++ {
		v := 0.0
		if p[i] < r.min[i] {
			v = r.min[i] - p[i]
		} else if p[i] > r.max[i] {
			v = p[i] - r.max[i]
		}
		s += v * v
	}
	return math.Sqrt(s)
}

// MinDistRect is the minimum distance between two boxes.
func (r Rect) MinDistRect(o Rect) float64 {
	s := 0.0
	for i := 0; i < r.D; i++ {
		v := 0.0
		if r.max[i] < o.min[i] {
			v = o.min[i] - r.max[i]
		} else if o.max[i] < r.min[i] {
			v = r.min[i] - o.max[i]
		}
		s += v * v
	}
	return math.Sqrt(s)
}

// CenterDistSq is the squared distance between the centres of r and o.
func (r Rect) CenterDistSq(o Rect) float64 {
	s := 0.0
	for i := 0; i < r.D; i++ {
		rc := (r.min[i] + r.max[i]) / 2
		oc := (o.min[i] + o.max[i]) / 2
		d := rc - oc
		s += d * d
	}
	return s
}

// Equal reports whether r and o have identical bounds (exact float compare).
func (r Rect) Equal(o Rect) bool {
	if r.D != o.D {
		return false
	}
	for i := 0; i < r.D; i++ {
		if r.min[i] != o.min[i] || r.max[i] != o.max[i] {
			return false
		}
	}
	return true
}

// Validate reports an error message when r is malformed. An empty rect is
// valid.
func (r Rect) Validate() string {
	for i := 0; i < r.D; i++ {
		if math.IsNaN(r.min[i]) || math.IsNaN(r.max[i]) {
			return "NaN bound"
		}
		if r.min[i] > r.max[i] && !(math.IsInf(r.min[i], 1) && math.IsInf(r.max[i], -1)) {
			return "min > max"
		}
	}
	return ""
}

// RandPoint returns a uniform point inside r.
func (r Rect) RandPoint(rng *rand.Rand) []float64 {
	p := make([]float64, r.D)
	for i := 0; i < r.D; i++ {
		p[i] = r.min[i] + rng.Float64()*(r.max[i]-r.min[i])
	}
	return p
}

// UnionRects computes the union of rects, returning an empty rect for an
// empty slice. D is taken from the first rect.
func UnionRects(rects []Rect, d int) Rect {
	u := NewRect(d)
	for _, r := range rects {
		u.Extend(r)
	}
	return u
}
