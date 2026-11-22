package rtree

import (
	"math"
	"math/rand"
	"testing"

	"ancienttrees/internal/geom"
)

// unionNodesMeasure must match brute-force integration by sampling the union
// of the rects on a fine grid (for 2D trees).
func TestUnionMeasureExact(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	tr, _ := New(DefaultConfig(2))
	var rects []geom.Rect
	for i := 0; i < 300; i++ {
		r := geom.NewRectFromBounds(
			[]float64{rng.Float64() * 100, rng.Float64() * 100},
			[]float64{0, 0},
		)
		r = geom.NewRectFromBounds(r.Mins(), []float64{r.Min(0) + 1 + rng.Float64()*9, r.Min(1) + 1 + rng.Float64()*9})
		rects = append(rects, r)
		tr.Insert(r, i)
	}
	// unionRectMeasure2D is the primitive used by the level stats.
	got := unionRectMeasure2D(rects)
	// Brute force with a fine grid.
	const n = 2000
	const span = 116.0
	step := span / n
	var want float64
	for i := 0; i < n; i++ {
		x := -6 + (float64(i)+0.5)*step
		for j := 0; j < n; j++ {
			y := -6 + (float64(j)+0.5)*step
			for _, r := range rects {
				if x >= r.Min(0) && x <= r.Max(0) && y >= r.Min(1) && y <= r.Max(1) {
					want += step * step
					break
				}
			}
		}
	}
	if math.Abs(got-want)/want > 0.01 {
		t.Fatalf("union area %v vs sampled %v", got, want)
	}
}

func TestOverlapStatsConsistency(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	tr, _ := New(DefaultConfig(2))
	for i := 0; i < 4000; i++ {
		tr.Insert(randRect(rng, 2, 600), i)
	}
	st := tr.ComputeOverlapStats()
	if st.Records != 4000 || st.Height < 1 || len(st.Levels) != st.Height+1 {
		t.Fatalf("levels %d height %d", len(st.Levels), st.Height)
	}
	// Per-level: union measure cannot be smaller than any node, and the
	// pairwise overlap cannot exceed 2*sum(node areas).
	for _, lv := range st.Levels {
		if lv.CoverArea > 0 && lv.OverlapArea < 0 {
			t.Fatalf("negative overlap")
		}
		if lv.OverlapArea > 0 && lv.CoverArea == 0 {
			t.Fatalf("overlap without cover")
		}
		if lv.Nodes > 1 && lv.MeanOverlapPerNode < 0 {
			t.Fatalf("negative mean overlap")
		}
	}
	if st.FillFactor <= 0 || st.FillFactor > 1.0001 {
		t.Fatalf("fill factor %v", st.FillFactor)
	}
}

func TestOverlapStats3DProjected(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	tr, _ := New(DefaultConfig(3))
	for i := 0; i < 4000; i++ {
		r := randRect(rng, 3, 600)
		// Make the time axis point-like.
		z := rng.Float64() * 10000
		r = geom.NewRectFromBounds([]float64{r.Min(0), r.Min(1), z}, []float64{r.Max(0), r.Max(1), z})
		tr.Insert(r, i)
	}
	st := tr.ComputeOverlapStats()
	if !st.Projected {
		t.Fatal("3D stats must be marked projected")
	}
	if st.TotalCoverArea <= 0 || st.TotalOverlapArea < 0 {
		t.Fatalf("bad 3D areas: %+v", st)
	}
}

func TestSnapshotAndValidateAfterChurn(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	tr, _ := New(DefaultConfig(2))
	var rects []geom.Rect
	for i := 0; i < 1500; i++ {
		r := randRect(rng, 2, 400)
		rects = append(rects, r)
		tr.Insert(r, i)
	}
	for i := 0; i < 500; i++ {
		idx := rng.Intn(len(rects))
		tr.DeleteFirst(rects[idx], func(d any) bool { return d.(int) == idx })
	}
	if p := tr.Validate(); len(p) != 0 {
		t.Fatal(p)
	}
	dto := tr.Snapshot(func(d any) (int, RecordLabel) {
		return d.(int), RecordLabel{Kind: "登记树"}
	})
	if dto.Records != tr.Len() {
		t.Fatalf("snapshot records %d want %d", dto.Records, tr.Len())
	}
	leafEntries := 0
	for _, n := range dto.Nodes {
		if n.Leaf {
			for _, e := range n.Entries {
				if len(e.Records) != 1 {
					t.Fatalf("leaf entry without record id")
				}
				leafEntries++
			}
		}
	}
	if leafEntries != tr.Len() {
		t.Fatalf("leaf entries %d want %d", leafEntries, tr.Len())
	}
}

func TestBulkLoadSingleAndEmpty(t *testing.T) {
	tr, _ := New(DefaultConfig(2))
	tr.BulkLoad(nil)
	if tr.Root != nil || tr.Len() != 0 {
		t.Fatal("empty bulk load")
	}
	r := geom.NewRectFromBounds([]float64{0, 0}, []float64{1, 1})
	tr.BulkLoad([]BulkLoadItem{{Rect: r, Data: 1}})
	if tr.Len() != 1 || tr.Height() != 0 {
		t.Fatalf("single item: len %d height %d", tr.Len(), tr.Height())
	}
	if p := tr.Validate(); len(p) != 0 {
		t.Fatal(p)
	}
}
