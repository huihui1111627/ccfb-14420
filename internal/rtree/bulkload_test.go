package rtree

import (
	"math"
	"math/rand"
	"testing"

	"ancienttrees/internal/geom"
)

// A parent group built by STR over child MBRs should be spatially compact:
// the sum of group MBR areas must not exceed a modest multiple of the sum of
// child areas (a low "waste factor"). This is what keeps upper-level overlap
// low after STR packing.
func TestBulkParentGroupsAreCompact(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	// Simulate ~2800 child MBRs shaped like clustered leaf boxes.
	var rects []geom.Rect
	for i := 0; i < 2800; i++ {
		cx := math.Round(rng.NormFloat64() * 4)
		cy := math.Round(rng.NormFloat64() * 4)
		x := cx*2500 + rng.NormFloat64()*900
		y := cy*2500 + rng.NormFloat64()*900
		w := 400 + rng.Float64()*600
		h := 400 + rng.Float64()*600
		rects = append(rects, geom.NewRectFromBounds(
			[]float64{x - w/2, y - h/2}, []float64{x + w/2, y + h/2}))
	}
	groups := bulkLevelGroups(rects, 16, 2)
	// STR tiles into whole slabs, so the count can exceed the minimum
	// ceil(n/max) = 175; it must stay close to it and never exceed max.
	if len(groups) < 175 || len(groups) > 196 {
		t.Fatalf("groups %d outside the expected tiling range", len(groups))
	}
	var sumChild, sumGroup float64
	for _, g := range groups {
		if len(g) == 0 || len(g) > 16 {
			t.Fatalf("group size %d", len(g))
		}
		b := geom.NewRect(2)
		for _, i := range g {
			b.Extend(rects[i])
			sumChild += rects[i].Area()
		}
		sumGroup += b.Area()
	}
	waste := sumGroup / sumChild
	// A random 16-way partition of these boxes wastes far more than this;
	// STR keeps the sum of group MBR areas within a small factor.
	if waste > 8 {
		t.Fatalf("parent grouping waste factor %.2f is too high", waste)
	}
}

func TestBulkChildCoverage(t *testing.T) {
	// Every record must appear exactly once in the leaves after bulk loading.
	rng := rand.New(rand.NewSource(9))
	tr, _ := New(DefaultConfig(2))
	var items []BulkLoadItem
	seen := map[int]bool{}
	for i := 0; i < 5000; i++ {
		items = append(items, BulkLoadItem{Rect: randRect(rng, 2, 900), Data: i})
	}
	tr.BulkLoad(items)
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Leaf {
			for _, e := range n.Entries {
				id := e.Data.(int)
				if seen[id] {
					t.Fatalf("duplicate id %d", id)
				}
				seen[id] = true
			}
			return
		}
		for _, e := range n.Entries {
			walk(e.child)
		}
	}
	walk(tr.Root)
	if len(seen) != 5000 {
		t.Fatalf("seen %d", len(seen))
	}
}
