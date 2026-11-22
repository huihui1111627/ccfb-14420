package rtree

import (
	"math"
	"math/rand"
	"sort"
	"testing"

	"ancienttrees/internal/geom"
)

func randRect(rng *rand.Rand, dim int, scale float64) geom.Rect {
	lo := make([]float64, dim)
	hi := make([]float64, dim)
	for d := 0; d < dim; d++ {
		lo[d] = rng.Float64() * scale
		hi[d] = lo[d] + rng.Float64()*scale*0.05
	}
	return geom.NewRectFromBounds(lo, hi)
}

func bruteSearch(entries []geom.Rect, q geom.Rect) []int {
	var out []int
	for i, r := range entries {
		if r.Intersects(q) {
			out = append(out, i)
		}
	}
	return out
}

func checkSearchSet(t *testing.T, tr *Tree, entries []geom.Rect, q geom.Rect) {
	t.Helper()
	got := tr.SearchRect(q)
	gotIDs := make([]int, 0, len(got))
	for _, d := range got {
		gotIDs = append(gotIDs, d.(int))
	}
	sort.Ints(gotIDs)
	want := bruteSearch(entries, q)
	sort.Ints(want)
	if len(gotIDs) != len(want) {
		t.Fatalf("search count %d want %d", len(gotIDs), len(want))
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("search id mismatch at %d: got %d want %d", i, gotIDs[i], want[i])
		}
	}
}

func TestInsertSearchAllDims(t *testing.T) {
	for _, dim := range []int{2, 3} {
		rng := rand.New(rand.NewSource(int64(dim)))
		tr, err := New(DefaultConfig(dim))
		if err != nil {
			t.Fatal(err)
		}
		entries := make([]geom.Rect, 3000)
		for i := range entries {
			entries[i] = randRect(rng, dim, 1000)
			tr.Insert(entries[i], i)
		}
		if len(tr.Validate()) != 0 {
			t.Fatalf("dim %d invalid: %v", dim, tr.Validate())
		}
		for q := 0; q < 150; q++ {
			checkSearchSet(t, tr, entries, randRect(rng, dim, 1000))
		}
	}
}

func TestReinsertionDisabled(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	cfg := DefaultConfig(2)
	cfg.ForcedReinsert = false
	tr, _ := New(cfg)
	var entries []geom.Rect
	for i := 0; i < 4000; i++ {
		r := randRect(rng, 2, 1000)
		entries = append(entries, r)
		tr.Insert(r, i)
	}
	if p := tr.Validate(); len(p) != 0 {
		t.Fatal(p)
	}
	for q := 0; q < 80; q++ {
		checkSearchSet(t, tr, entries, randRect(rng, 2, 1000))
	}
}

func TestDeleteAndCondense(t *testing.T) {
	for _, dim := range []int{2, 3} {
		rng := rand.New(rand.NewSource(int64(100 + dim)))
		tr, _ := New(DefaultConfig(dim))
		var entries []geom.Rect
		alive := map[int]bool{}
		for i := 0; i < 2500; i++ {
			r := randRect(rng, dim, 500)
			entries = append(entries, r)
			alive[i] = true
			tr.Insert(r, i)
		}
		order := rng.Perm(len(entries))
		for k, idx := range order {
			if k%3 == 0 {
				continue
			}
			if !tr.DeleteFirst(entries[idx], func(d any) bool { return d.(int) == idx }) {
				t.Fatalf("delete %d failed", idx)
			}
			delete(alive, idx)
			if k%200 == 0 {
				if p := tr.Validate(); len(p) != 0 {
					t.Fatalf("after %d deletes: %v", k, p)
				}
			}
		}
		if p := tr.Validate(); len(p) != 0 {
			t.Fatal(p)
		}
		if tr.Len() != len(alive) {
			t.Fatalf("len %d want %d", tr.Len(), len(alive))
		}
		for q := 0; q < 100; q++ {
			qr := randRect(rng, dim, 500)
			got := tr.SearchRect(qr)
			want := 0
			for id := range alive {
				if entries[id].Intersects(qr) {
					want++
				}
			}
			if len(got) != want {
				t.Fatalf("dim %d query %d: got %d want %d", dim, q, len(got), want)
			}
		}
	}
}

func TestBulkLoadMatchesBrute(t *testing.T) {
	for _, dim := range []int{2, 3} {
		rng := rand.New(rand.NewSource(int64(200 + dim)))
		tr, _ := New(DefaultConfig(dim))
		n := 5000
		items := make([]BulkLoadItem, n)
		entries := make([]geom.Rect, n)
		for i := 0; i < n; i++ {
			r := randRect(rng, dim, 800)
			items[i] = BulkLoadItem{Rect: r, Data: i}
			entries[i] = r
		}
		tr.BulkLoad(items)
		if tr.Len() != n {
			t.Fatalf("len %d", tr.Len())
		}
		if p := tr.Validate(); len(p) != 0 {
			t.Fatalf("dim %d bulk invalid: %v", dim, p)
		}
		for q := 0; q < 120; q++ {
			checkSearchSet(t, tr, entries, randRect(rng, dim, 800))
		}
		// Insertions on top of a bulk-loaded tree must keep it valid.
		for i := 0; i < 500; i++ {
			r := randRect(rng, dim, 800)
			entries = append(entries, r)
			tr.Insert(r, n+i)
		}
		if p := tr.Validate(); len(p) != 0 {
			t.Fatal(p)
		}
		for q := 0; q < 50; q++ {
			checkSearchSet(t, tr, entries, randRect(rng, dim, 800))
		}
	}
}

func TestKNNMatchesBrute(t *testing.T) {
	for _, dim := range []int{2, 3} {
		rng := rand.New(rand.NewSource(int64(300 + dim)))
		tr, _ := New(DefaultConfig(dim))
		var entries []geom.Rect
		for i := 0; i < 3000; i++ {
			r := randRect(rng, dim, 1000)
			entries = append(entries, r)
			tr.Insert(r, i)
		}
		for q := 0; q < 60; q++ {
			p := make([]float64, dim)
			for d := 0; d < dim; d++ {
				p[d] = rng.Float64() * 1000
			}
			k := 1 + rng.Intn(12)
			got, _ := tr.KNN(p, k, nil)
			if len(got) != k {
				t.Fatalf("knn len %d want %d", len(got), k)
			}
			type pair struct {
				d    float64
				data int
			}
			var want []pair
			for i, r := range entries {
				want = append(want, pair{r.MinDistPoint(p), i})
			}
			sort.Slice(want, func(i, j int) bool {
				if want[i].d != want[j].d {
					return want[i].d < want[j].d
				}
				return want[i].data < want[j].data
			})
			for i := 0; i < k; i++ {
				if math.Abs(got[i].Dist-want[i].d) > 1e-9 {
					t.Fatalf("knn dist %d: got %v want %v", i, got[i].Dist, want[i].d)
				}
			}
		}
	}
}

func TestKNNIteratorMatchesKNN(t *testing.T) {
	rng := rand.New(rand.NewSource(44))
	tr, _ := New(DefaultConfig(2))
	var entries []geom.Rect
	for i := 0; i < 2500; i++ {
		r := randRect(rng, 2, 1000)
		entries = append(entries, r)
		tr.Insert(r, i)
	}
	for q := 0; q < 40; q++ {
		p := []float64{rng.Float64() * 1000, rng.Float64() * 1000}
		k := 1 + rng.Intn(20)
		want, _ := tr.KNN(p, k, nil)
		it := tr.NewKNNIterator(p, nil)
		var got []NearestHit
		for len(got) < k && it.Next() {
			got = append(got, it.Last)
		}
		it.Finish()
		if len(got) != len(want) {
			t.Fatalf("iterator len %d want %d", len(got), len(want))
		}
		for i := range want {
			if math.Abs(got[i].Dist-want[i].Dist) > 1e-9 {
				t.Fatalf("iter dist %d got %v want %v", i, got[i].Dist, want[i].Dist)
			}
		}
		// Full drain order must be monotone.
		full := tr.NewKNNIterator(p, nil)
		prev := -1.0
		count := 0
		for full.Next() {
			if full.Last.Dist < prev-1e-9 {
				t.Fatalf("iterator not monotone: %v then %v", prev, full.Last.Dist)
			}
			prev = full.Last.Dist
			count++
		}
		full.Finish()
		if count != len(entries) {
			t.Fatalf("drained %d want %d", count, len(entries))
		}
	}
}

func TestTreeStatsAndOverlap(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	tr, _ := New(DefaultConfig(2))
	for i := 0; i < 8000; i++ {
		r := randRect(rng, 2, 1000)
		tr.Insert(r, i)
	}
	st := tr.ComputeOverlapStats()
	if st.Records != 8000 || len(st.Levels) == 0 {
		t.Fatalf("bad stats %+v", st)
	}
	for _, lv := range st.Levels {
		if lv.CoverArea <= 0 {
			t.Fatalf("level %d non-positive cover", lv.Level)
		}
		if lv.OverlapArea > 0 && lv.OverlapRatio <= 0 {
			t.Fatalf("ratio missing")
		}
	}
}

func TestStructureMatchesSansReinsert(t *testing.T) {
	// Forced reinsertion must actually change the tree; everything else
	// (range answers) must stay identical.
	rng := rand.New(rand.NewSource(91))
	cfgA := DefaultConfig(2)
	cfgB := DefaultConfig(2)
	cfgB.ForcedReinsert = false
	ta, _ := New(cfgA)
	tb, _ := New(cfgB)
	var entries []geom.Rect
	for i := 0; i < 6000; i++ {
		r := randRect(rng, 2, 1000)
		entries = append(entries, r)
		ta.Insert(r, i)
		tb.Insert(r, i)
	}
	if p := ta.Validate(); len(p) != 0 {
		t.Fatal(p)
	}
	if p := tb.Validate(); len(p) != 0 {
		t.Fatal(p)
	}
	if ta.Stats.ReinsertOps.Load() == 0 {
		t.Fatal("expected forced reinsertion to fire")
	}
	if tb.Stats.ReinsertOps.Load() != 0 {
		t.Fatal("expected no reinsertion when disabled")
	}
	for q := 0; q < 100; q++ {
		qr := randRect(rng, 2, 1000)
		a := ta.SearchRect(qr)
		b := tb.SearchRect(qr)
		if len(a) != len(b) {
			t.Fatalf("query %d differs: %d vs %d", q, len(a), len(b))
		}
	}
}

func TestDeleteNonExistent(t *testing.T) {
	tr, _ := New(DefaultConfig(2))
	r := geom.NewRectFromBounds([]float64{0, 0}, []float64{1, 1})
	tr.Insert(r, 1)
	if tr.DeleteFirst(geom.NewRectFromBounds([]float64{2, 2}, []float64{3, 3}), nil) {
		t.Fatal("deleted absent record")
	}
	if tr.Len() != 1 {
		t.Fatal("len changed")
	}
}

func TestRootCollapseAfterDeletes(t *testing.T) {
	tr, _ := New(DefaultConfig(2))
	var rects []geom.Rect
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 400; i++ {
		r := randRect(rng, 2, 100)
		rects = append(rects, r)
		tr.Insert(r, i)
	}
	for i := 0; i < 390; i++ {
		tr.DeleteFirst(rects[i], func(d any) bool { return d.(int) == i })
	}
	if p := tr.Validate(); len(p) != 0 {
		t.Fatal(p)
	}
	if tr.Len() != 10 {
		t.Fatalf("len %d", tr.Len())
	}
}
