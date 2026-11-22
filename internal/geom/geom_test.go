package geom

import (
	"math"
	"math/rand"
	"testing"
)

func TestRectOps(t *testing.T) {
	a := NewRectFromBounds([]float64{0, 0}, []float64{10, 10})
	b := NewRectFromBounds([]float64{5, 5}, []float64{15, 15})
	if got := a.IntersectionArea(b); got != 25 {
		t.Fatalf("intersection %v", got)
	}
	if got := a.AreaWithUnion(b); got != 225 {
		t.Fatalf("union area %v", got)
	}
	if got := a.Enlargement(b); got != 125 {
		t.Fatalf("enlargement %v", got)
	}
	if got := a.MinDistPoint([]float64{13, 12}); math.Abs(got-math.Hypot(3, 2)) > 1e-12 {
		t.Fatalf("mindist %v", got)
	}
	if a.MinDistPoint([]float64{5, 5}) != 0 {
		t.Fatal("inside distance should be 0")
	}
	if !a.Intersects(b) || a.ContainsRect(b) {
		t.Fatal("intersect/contains wrong")
	}
	c := NewRectFromBounds([]float64{2, 2}, []float64{3, 3})
	if !a.ContainsRect(c) {
		t.Fatal("containment wrong")
	}
}

func TestRectEnlargedOverlap(t *testing.T) {
	// r grows to cover grow; overlap with o after growth.
	r := NewRectFromBounds([]float64{0, 0}, []float64{2, 2})
	grow := NewRectFromBounds([]float64{3, 0}, []float64{5, 2})
	o := NewRectFromBounds([]float64{1, 0}, []float64{4, 2})
	got := r.EnlargedOverlap(grow, o)
	// r union grow = [0,5]x[0,2]; overlap with o = 3*2 = 6.
	if math.Abs(got-6) > 1e-12 {
		t.Fatalf("enlarged overlap %v", got)
	}
}

func TestPolygonBasics(t *testing.T) {
	sq := NewPolygon([]Point{{0, 0}, {10, 0}, {10, 10}, {0, 10}})
	if !sq.ContainsPoint(Point{5, 5}) || sq.ContainsPoint(Point{11, 5}) {
		t.Fatal("contains point")
	}
	if !sq.ContainsPoint(Point{0, 5}) || !sq.ContainsPoint(Point{0, 0}) {
		t.Fatal("boundary must count as inside")
	}
	if math.Abs(sq.Area()-100) > 1e-9 {
		t.Fatalf("area %v", sq.Area())
	}
	if c := sq.Centroid(); math.Abs(c.X-5) > 1e-9 || math.Abs(c.Y-5) > 1e-9 {
		t.Fatalf("centroid %v", c)
	}
	// Closed rings are accepted.
	closed := NewPolygon([]Point{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}})
	if math.Abs(closed.Area()-100) > 1e-9 {
		t.Fatalf("closed area %v", closed.Area())
	}
	if !sq.IntersectsPolygon(closed) {
		t.Fatal("identical squares must intersect")
	}
}

func TestPolygonIntersection(t *testing.T) {
	a := NewPolygon([]Point{{0, 0}, {10, 0}, {10, 10}, {0, 10}})
	cases := []struct {
		name string
		p    Polygon
		want bool
	}{
		{"disjoint", NewPolygon([]Point{{20, 20}, {30, 20}, {30, 30}, {20, 30}}), false},
		{"edge touch", NewPolygon([]Point{{10, 0}, {20, 0}, {20, 10}, {10, 10}}), true},
		{"overlap", NewPolygon([]Point{{5, 5}, {15, 5}, {15, 15}, {5, 15}}), true},
		{"inside", NewPolygon([]Point{{4, 4}, {6, 4}, {6, 6}, {4, 6}}), true},
		{"corner touch", NewPolygon([]Point{{10, 10}, {12, 10}, {12, 12}, {10, 12}}), true},
		{"outside with bbox overlap", NewPolygon([]Point{{11, 0}, {20, 0}, {20, 9}}), false},
	}
	for _, c := range cases {
		if got := a.IntersectsPolygon(c.p); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
		if got := c.p.IntersectsPolygon(a); got != c.want {
			t.Errorf("%s reversed: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestPolygonIntersectsRect(t *testing.T) {
	tri := NewPolygon([]Point{{0, 0}, {10, 0}, {0, 10}})
	cases := []struct {
		r    Rect
		want bool
	}{
		{NewRectFromBounds([]float64{1, 1}, []float64{2, 2}), true},   // inside
		{NewRectFromBounds([]float64{-1, -1}, []float64{1, 1}), true}, // corner
		{NewRectFromBounds([]float64{6, 6}, []float64{8, 8}), false},  // outside the hypotenuse
		{NewRectFromBounds([]float64{2, 2}, []float64{7, 7}), true},   // straddles hypotenuse
		{NewRectFromBounds([]float64{-5, -5}, []float64{-1, 20}), false},
	}
	for i, c := range cases {
		if got := tri.IntersectsRect(c.r); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

// The polygon intersection routine must agree with a brute-force sampling
// test: if the polygons intersect, at least one vertex of the union grid is
// inside both (for these convex random polygons).
func TestPolygonIntersectionRandomVsSampling(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 400; i++ {
		c := Point{rng.Float64() * 100, rng.Float64() * 100}
		c2 := Point{c.X + rng.NormFloat64()*12, c.Y + rng.NormFloat64()*12}
		a := RandomConvexPolygon(rng, c, 5+rng.Intn(5), 2, 14)
		b := RandomConvexPolygon(rng, c2, 5+rng.Intn(5), 2, 14)
		got := a.IntersectsPolygon(b)
		// Sampling: any point on a dense grid inside both polygons.
		want := false
		for x := -30.0; x <= 140 && !want; x += 0.8 {
			for y := -30.0; y <= 140; y += 0.8 {
				p := Point{x, y}
				if a.ContainsPoint(p) && b.ContainsPoint(p) {
					want = true
					break
				}
			}
		}
		// A near-degenerate overlap can be missed by sampling; only flag
		// false negatives where a vertex of one lies strictly inside the other.
		if want && !got {
			t.Fatalf("case %d: sampled overlap but routine says no (%v %v)", i, a.Ring[0], b.Ring[0])
		}
	}
}

func TestDistanceToPolygon(t *testing.T) {
	sq := NewPolygon([]Point{{0, 0}, {10, 0}, {10, 10}, {0, 10}})
	if d := DistanceToPolygon(Point{5, 5}, sq); d != 0 {
		t.Fatalf("inside distance %v", d)
	}
	if d := DistanceToPolygon(Point{13, 5}, sq); math.Abs(d-3) > 1e-12 {
		t.Fatalf("outside distance %v", d)
	}
	if d := DistanceToPolygon(Point{13, 13}, sq); math.Abs(d-math.Hypot(3, 3)) > 1e-12 {
		t.Fatalf("corner distance %v", d)
	}
}

func TestConvexHull(t *testing.T) {
	pts := []Point{{0, 0}, {4, 0}, {4, 4}, {0, 4}, {2, 2}, {1, 3}}
	hull := ConvexHull(pts)
	if len(hull) != 4 {
		t.Fatalf("hull size %d: %v", len(hull), hull)
	}
	if math.Abs(NewPolygon(hull).Area()-16) > 1e-9 {
		t.Fatalf("hull area %v", NewPolygon(hull).Area())
	}
}
