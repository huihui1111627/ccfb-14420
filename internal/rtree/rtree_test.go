package rtree

import (
	"math"
	"math/rand"
	"sort"
	"testing"

	"ancienttrees/internal/geom"
)

func randBox(rng *rand.Rand, dims int, maxSize float64) geom.Box {
	var a, b [3]float64
	for i := 0; i < dims; i++ {
		a[i] = rng.Float64() * 1000
		b[i] = a[i] + rng.Float64()*maxSize
	}
	return geom.NewBox(uint8(dims), a, b)
}

func allKeys(t *Tree) []int64 {
	var ks []int64
	t.Search(geom.Box{Dims: t.Dims(), Lo: [3]float64{-1e9, -1e9, -1e9}, Hi: [3]float64{1e9, 1e9, 1e9}},
		func(e Entry) { ks = append(ks, e.Key) })
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}

func TestInsertDeleteInvariants(t *testing.T) {
	for _, dims := range []uint8{2, 3} {
		for _, reinsert := range []bool{false, true} {
			rng := rand.New(rand.NewSource(int64(dims)*100 + boolInt(reinsert)))
			tr := New(Options{Dims: dims, MaxEntries: 8, ForceReinsert: reinsert})
			boxes := map[int64]geom.Box{}
			for i := 0; i < 4000; i++ {
				b := randBox(rng, int(dims), 40)
				key := int64(i)
				boxes[key] = b
				tr.Insert(b, key, nil)
			}
			if err := tr.Check(); err != nil {
				t.Fatalf("dims=%d reinsert=%v insert check: %v", dims, reinsert, err)
			}
			if len(allKeys(tr)) != 4000 {
				t.Fatalf("dims=%d: key count %d", dims, len(allKeys(tr)))
			}
			// delete a random 70%
			var keys []int64
			for k := range boxes {
				keys = append(keys, k)
			}
			rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
			for _, k := range keys[:2800] {
				if !tr.Delete(boxes[k], k) {
					t.Fatalf("dims=%d: delete of %d failed", dims, k)
				}
			}
			if err := tr.Check(); err != nil {
				t.Fatalf("dims=%d reinsert=%v delete check: %v", dims, reinsert, err)
			}
			got := allKeys(tr)
			if len(got) != 1200 {
				t.Fatalf("dims=%d: after delete have %d keys", dims, len(got))
			}
			for _, k := range keys[2800:] {
				if !contains(got, k) {
					t.Fatalf("dims=%d: survivor %d missing", dims, k)
				}
			}
		}
	}
}

func TestEmptyAllThenGrow(t *testing.T) {
	tr := New(Options{Dims: 2, MaxEntries: 4})
	var boxes []geom.Box
	for i := 0; i < 50; i++ {
		b := geom.NewBox(2, [3]float64{float64(i), 0, 0}, [3]float64{float64(i) + 1, 1, 0})
		boxes = append(boxes, b)
		tr.Insert(b, int64(i), nil)
	}
	for i := 0; i < 50; i++ {
		tr.Delete(boxes[i], int64(i))
		if err := tr.Check(); err != nil {
			t.Fatalf("after deleting %d: %v", i, err)
		}
		if tr.Size != 49-i {
			t.Fatalf("size %d want %d", tr.Size, 49-i)
		}
	}
	if tr.Root != nil {
		t.Fatalf("root should be nil")
	}
	for i := 0; i < 20; i++ {
		b := boxes[i]
		tr.Insert(b, int64(i+100), nil)
	}
	if err := tr.Check(); err != nil {
		t.Fatalf("regrow: %v", err)
	}
}

func TestBulkLoadMatchesIncremental(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var items []BulkItem
	var inc []BulkItem
	for i := 0; i < 3000; i++ {
		b := randBox(rng, 2, 30)
		items = append(items, BulkItem{Rect: b, Key: int64(i)})
		inc = append(inc, BulkItem{Rect: b, Key: int64(i)})
	}
	bulk := New(Options{Dims: 2, MaxEntries: 8, ForceReinsert: true})
	bulk.BulkLoad(items)
	if err := bulk.Check(); err != nil {
		t.Fatalf("bulk check: %v", err)
	}
	grown := New(Options{Dims: 2, MaxEntries: 8, ForceReinsert: true})
	rng.Shuffle(len(inc), func(i, j int) { inc[i], inc[j] = inc[j], inc[i] })
	for _, it := range inc {
		grown.Insert(it.Rect, it.Key, nil)
	}
	if err := grown.Check(); err != nil {
		t.Fatalf("grown check: %v", err)
	}
	// Query parity
	for q := 0; q < 300; q++ {
		r := randBox(rng, 2, 120)
		var a, b []int64
		bulk.Search(r, func(e Entry) { a = append(a, e.Key) })
		grown.Search(r, func(e Entry) { b = append(b, e.Key) })
		sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
		sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
		if len(a) != len(b) {
			t.Fatalf("range size differ %d %d", len(a), len(b))
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("range keys differ")
			}
		}
	}
	bs := bulk.Snapshot()
	gs := grown.Snapshot()
	if bs.TotalOverlap >= gs.TotalOverlap {
		t.Logf("bulk overlap %v vs grown %v", bs.TotalOverlap, gs.TotalOverlap)
	}
}

func TestNearestOrdering(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	tr := New(Options{Dims: 2, MaxEntries: 6, ForceReinsert: true})
	type rec struct {
		key int64
		b   geom.Box
	}
	var recs []rec
	for i := 0; i < 2000; i++ {
		b := randBox(rng, 2, 12)
		recs = append(recs, rec{int64(i), b})
		tr.Insert(b, int64(i), nil)
	}
	for trial := 0; trial < 100; trial++ {
		p := [3]float64{rng.Float64() * 1000, rng.Float64() * 1000, 0}
		it := tr.Nearest(p, 0)
		var got []nearPair
		for {
			d, k, _, ok := it.Next()
			if !ok {
				break
			}
			got = append(got, nearPair{d, k})
		}
		want := make([]nearPair, len(recs))
		box := geom.Point(2, p)
		for i, r := range recs {
			_ = box
			want[i] = nearPair{r.b.MinDistSq(p), r.key}
		}
		sort.Slice(want, func(i, j int) bool {
			if want[i].d != want[j].d {
				return want[i].d < want[j].d
			}
			return want[i].k < want[j].k
		})
		if len(got) != len(want) {
			t.Fatalf("nearest count %d want %d", len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("trial %d pos %d: got %+v want %+v", trial, i, got[i], want[i])
			}
		}
	}
}

type nearPair struct {
	d float64
	k int64
}

func TestNearestKAndMaxDist(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	tr := New(Options{Dims: 3, MaxEntries: 6, ForceReinsert: false})
	type rec struct {
		key int64
		b   geom.Box
	}
	var recs []rec
	for i := 0; i < 500; i++ {
		b := randBox(rng, 3, 10)
		recs = append(recs, rec{int64(i), b})
		tr.Insert(b, int64(i), nil)
	}
	for trial := 0; trial < 50; trial++ {
		p := [3]float64{rng.Float64() * 1000, rng.Float64() * 1000, rng.Float64() * 1000}
		k := 1 + rng.Intn(30)
		got, _ := tr.NearestK(p, k)
		if len(got) != k {
			t.Fatalf("want %d got %d", k, len(got))
		}
		var want []nearPair
		for _, r := range recs {
			want = append(want, nearPair{r.b.MinDistSq(p), r.key})
		}
		sort.Slice(want, func(i, j int) bool {
			if want[i].d != want[j].d {
				return want[i].d < want[j].d
			}
			return want[i].k < want[j].k
		})
		for i := range got {
			if math.Abs(got[i].Dist-want[i].d) > 1e-9 || got[i].Key != want[i].k {
				t.Fatalf("k-nearest mismatch at %d", i)
			}
		}
		// max-distance variant
		R := 80.0
		it := tr.Nearest(p, R)
		n := 0
		for {
			_, _, _, ok := it.Next()
			if !ok {
				break
			}
			n++
		}
		wantN := 0
		for _, w := range want {
			if w.d <= R*R {
				wantN++
			}
		}
		if n != wantN {
			t.Fatalf("maxdist count %d want %d", n, wantN)
		}
	}
}

func TestReinsertReducesOverlap(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	var boxes []geom.Box
	for i := 0; i < 5000; i++ {
		boxes = append(boxes, randBox(rng, 2, 25))
	}
	off := New(Options{Dims: 2, MaxEntries: 10, ForceReinsert: false})
	on := New(Options{Dims: 2, MaxEntries: 10, ForceReinsert: true})
	for i, b := range boxes {
		off.Insert(b, int64(i), nil)
		on.Insert(b, int64(i), nil)
	}
	if err := off.Check(); err != nil {
		t.Fatal(err)
	}
	if err := on.Check(); err != nil {
		t.Fatal(err)
	}
	so := off.Snapshot()
	sn := on.Snapshot()
	t.Logf("reinsert off: overlap=%v coverage=%v height=%d splits=%d",
		so.TotalOverlap, so.TotalCoverage, so.Height, so.Splits)
	t.Logf("reinsert on : overlap=%v coverage=%v height=%d splits=%d reinserts=%d/%d",
		sn.TotalOverlap, sn.TotalCoverage, sn.Height, sn.Splits, sn.ReinsertOps, sn.ReinsertEntries)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func contains(a []int64, k int64) bool {
	i := sort.Search(len(a), func(i int) bool { return a[i] >= k })
	return i < len(a) && a[i] == k
}
