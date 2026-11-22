package geom

import "testing"

func TestBoxOps(t *testing.T) {
	a := NewBox(2, [3]float64{0, 0, 0}, [3]float64{10, 10, 0})
	b := NewBox(2, [3]float64{5, 5, 0}, [3]float64{20, 20, 0})
	if got := a.Area(); got != 100 {
		t.Fatalf("area %v", got)
	}
	// Perim is half the perimeter in 2-D (the split-cost measure), i.e. 20
	// for a 10 x 10 box.
	if got := a.Perim(); got != 20 {
		t.Fatalf("perim %v", got)
	}
	if got := a.Overlap(b); got != 25 {
		t.Fatalf("overlap %v", got)
	}
	if !a.Intersects(b) {
		t.Fatal("should intersect")
	}
	if got := a.Enlargement(b); got != 300 {
		t.Fatalf("enlargement %v", got)
	}
	if got := a.MinDist([3]float64{13, 4, 0}); got != 3 {
		t.Fatalf("mindist %v", got)
	}
	if got := a.MinDistSq([3]float64{13, 4, 0}); got != 9 {
		t.Fatalf("mindist sq %v", got)
	}
	// touching counts as intersecting
	c := NewBox(2, [3]float64{10, 0, 0}, [3]float64{20, 10, 0})
	if !a.Intersects(c) {
		t.Fatal("touching boxes must intersect")
	}
}

func Test3D(t *testing.T) {
	a := NewBox(3, [3]float64{0, 0, 0}, [3]float64{2, 2, 2})
	b := NewBox(3, [3]float64{1, 1, 1}, [3]float64{3, 3, 5})
	if got := a.Area(); got != 8 {
		t.Fatalf("volume %v", got)
	}
	if got := a.Overlap(b); got != 1 {
		t.Fatalf("overlap %v", got)
	}
	if got := a.Perim(); got != 12 {
		t.Fatalf("perim %v", got)
	}
	// identical in x/y but disjoint in date
	c := NewBox(3, [3]float64{0, 0, 5}, [3]float64{2, 2, 6})
	if a.Intersects(c) || a.Overlap(c) != 0 {
		t.Fatal("date-disjoint boxes must not intersect")
	}
}

func square(x0, y0, s float64) [][2]float64 {
	return [][2]float64{{x0, y0}, {x0 + s, y0}, {x0 + s, y0 + s}, {x0, y0 + s}}
}

func TestPolyBoxIntersects(t *testing.T) {
	box := NewBox(2, [3]float64{0, 0, 0}, [3]float64{10, 10, 0})
	cases := []struct {
		name string
		poly [][2]float64
		want bool
	}{
		{"polygon contains box", square(-5, -5, 25), true},
		{"polygon inside box", square(2, 2, 3), true},
		{"polygon straddles box", [][2]float64{{-5, 5}, {15, 5}, {15, 6}, {-5, 6}}, true},
		{"disjoint", square(20, 20, 3), false},
		{"touching corner", square(10, 10, 3), true},
		{"shared edge", square(10, 0, 5), true},
		{"polygon hole-like coverage around box", [][2]float64{
			{-1, -1}, {11, -1}, {11, 11}, {-1, 11}}, true},
	}
	for _, c := range cases {
		if got := PolyBoxIntersects(c.poly, box); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// A vertical line segment just outside the box must not touch it.
	seg := [][2]float64{{-1, -1}, {-1, 11}, {-0.5, 11}, {-0.5, -1}}
	if PolyBoxIntersects(seg, box) {
		t.Errorf("polygon left of box should not intersect")
	}
	// A rotated square whose corners are all outside but which crosses the box.
	diamond := [][2]float64{{5, -3}, {13, 5}, {5, 13}, {-3, 5}}
	if !PolyBoxIntersects(diamond, box) {
		t.Errorf("diamond crossing box should intersect")
	}
}

func TestPolyRingArea(t *testing.T) {
	if got := PolyRingArea(square(0, 0, 2)); got != 4 {
		t.Fatalf("area %v", got)
	}
	// reverse winding
	if got := PolyRingArea([][2]float64{{0, 0}, {0, 2}, {2, 2}, {2, 0}}); got != 4 {
		t.Fatalf("reversed area %v", got)
	}
}

func TestPolyBBox(t *testing.T) {
	b := PolyBBox([][2]float64{{1, 5}, {3, -2}, {-4, 0}})
	if b.Lo[0] != -4 || b.Lo[1] != -2 || b.Hi[0] != 3 || b.Hi[1] != 5 {
		t.Fatalf("bbox %v", b)
	}
}
