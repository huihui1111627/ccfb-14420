package app

import (
	"fmt"
	"math/rand"
	"testing"

	"ancienttrees/internal/geom"
	"ancienttrees/internal/rtree"
)

func TestDuplicatePointsNearest(t *testing.T) {
	tr := rtree.New(rtree.Options{Dims: 2, MaxEntries: 8, ForceReinsert: true})
	data := NewDataset(1, 100, 2)
	trees := data.Live()
	var boxes []geom.Box
	var keys []int64
	for i := 0; i < 3000; i++ {
		base := trees[i%len(trees)]
		// identical box for all duplicates of the same base
		b := base.BBox2
		k := int64(100000 + i)
		boxes = append(boxes, b)
		keys = append(keys, k)
		tr.Insert(b, k, nil)
	}
	if err := tr.Check(); err != nil {
		t.Fatal(err)
	}
	if tr.Snapshot().TotalOverlap < 0 {
		t.Fatal("bad overlap")
	}
	rng := rand.New(rand.NewSource(2))
	for trial := 0; trial < 200; trial++ {
		q := [3]float64{rng.Float64() * 20000, rng.Float64() * 20000, 0}
		k := 1 + rng.Intn(50)
		got, _ := tr.NearestK(q, k)
		var want []dupPair
		for i := range boxes {
			want = append(want, dupPair{boxes[i].MinDistSq(q), keys[i]})
		}
		sortPairs(want)
		for i := range got {
			if got[i].Dist != want[i].d || got[i].Key != want[i].k {
				t.Fatalf("trial %d pos %d: got %+v want %+v", trial, i, got[i], want[i])
			}
		}
	}
	fmt.Println("duplicate nearest ok")
}

func TestDuplicatePointsDeletes(t *testing.T) {
	tr := rtree.New(rtree.Options{Dims: 3, MaxEntries: 6, ForceReinsert: false})
	base := geom.NewBox(3, [3]float64{100, 100, 10}, [3]float64{110, 110, 10})
	for i := 0; i < 500; i++ {
		tr.Insert(base, int64(i), nil)
	}
	rng := rand.New(rand.NewSource(4))
	perm := rng.Perm(500)
	for i, k := range perm {
		if !tr.Delete(base, int64(k)) {
			t.Fatalf("delete %d failed at %d", k, i)
		}
		if err := tr.Check(); err != nil {
			t.Fatalf("after delete %d: %v", i, err)
		}
	}
	if tr.Size != 0 || tr.Root != nil {
		t.Fatalf("tree not empty: size=%d root=%v", tr.Size, tr.Root)
	}
	fmt.Println("duplicate deletes ok")
}

type dupPair struct {
	d float64
	k int64
}

func sortPairs(p []dupPair) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0; j-- {
			if p[j].d < p[j-1].d || (p[j].d == p[j-1].d && p[j].k < p[j-1].k) {
				p[j], p[j-1] = p[j-1], p[j]
			} else {
				break
			}
		}
	}
}

func TestIdenticalBoxesChurn(t *testing.T) {
	for _, m := range []int{4, 8, 16} {
		tr := rtree.New(rtree.Options{Dims: 2, MaxEntries: m, ForceReinsert: true})
		base := geom.NewBox(2, [3]float64{5, 5, 0}, [3]float64{6, 6, 0})
		rng := rand.New(rand.NewSource(int64(m)))
		for i := 0; i < 2000; i++ {
			tr.Insert(base, int64(i), nil)
		}
		for i := 0; i < 1500; i++ {
			tr.Delete(base, int64(i))
			if i%371 == 0 {
				if err := tr.Check(); err != nil {
					t.Fatalf("M=%d after delete %d: %v", m, i, err)
				}
			}
		}
		if err := tr.Check(); err != nil {
			t.Fatalf("M=%d final: %v", m, err)
		}
		n := 0
		tr.Search(geom.NewBox(2, [3]float64{0, 0, 0}, [3]float64{10, 10, 0}), func(e rtree.Entry) { n++ })
		if n != 500 {
			t.Fatalf("M=%d expected 500 items, got %d", m, n)
		}
		_ = rng
	}
	fmt.Println("identical churn ok")
}

func TestSplitSpacing3D(t *testing.T) {
	// Trees on a grid where dates are identical: the split must still be
	// able to separate them along x or y rather than only on the date axis.
	tr := rtree.New(rtree.Options{Dims: 3, MaxEntries: 6})
	for i := 0; i < 40; i++ {
		for j := 0; j < 40; j++ {
			x := float64(i) * 100
			y := float64(j) * 100
			b := geom.NewBox(3, [3]float64{x, y, 12}, [3]float64{x + 5, y + 5, 12})
			tr.Insert(b, int64(i*40+j), nil)
		}
	}
	if err := tr.Check(); err != nil {
		t.Fatal(err)
	}
	st := tr.Snapshot()
	fmt.Printf("3D flat-date tree: height=%d nodes=%d overlap=%g\n", st.Height, st.NodeCount, st.TotalOverlap)
	if st.Height < 3 {
		t.Fatalf("expected a multi-level tree, got height %d", st.Height)
	}
}

func TestManyIdenticalItemsTerminate(t *testing.T) {
	tr := rtree.New(rtree.Options{Dims: 2, MaxEntries: 4, ForceReinsert: true})
	base := geom.NewBox(2, [3]float64{1, 1, 0}, [3]float64{2, 2, 0})
	for i := 0; i < 20000; i++ {
		tr.Insert(base, int64(i), nil)
	}
	if err := tr.Check(); err != nil {
		t.Fatal(err)
	}
	if tr.Size != 20000 {
		t.Fatalf("size %d", tr.Size)
	}
	st := tr.Snapshot()
	fmt.Printf("identical 20k: height=%d nodes=%d reinsertOps=%d splits=%d\n", st.Height, st.NodeCount, st.ReinsertOps, st.Splits)
	if st.Height > 8 {
		t.Fatalf("degenerate height %d", st.Height)
	}
}
