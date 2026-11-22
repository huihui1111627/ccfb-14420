package app

import (
	"math/rand"
	"testing"

	"ancienttrees/internal/geom"
)

func buildBoth(t *testing.T, data *Dataset) *Store {
	t.Helper()
	s := BuildStore(data, IndexOptions{MaxEntries: 16, ForceReinsert: true, ReinsertPercent: 30}, true, 1)
	if err := s.T2.Check(); err != nil {
		t.Fatalf("2D invariant: %v", err)
	}
	if err := s.T3.Check(); err != nil {
		t.Fatalf("3D invariant: %v", err)
	}
	return s
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRangeMatchesBruteForce(t *testing.T) {
	data := NewDataset(11, 4000, 40)
	s := buildBoth(t, data)
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 400; i++ {
		q := data.RandomQueryRect(rng, 1500)
		got, _ := s.QueryRange(q, false)
		if want := data.BruteRectIDs(q); !sameInts(got, want) {
			t.Fatalf("2D range mismatch at %d: got %d want %d", i, len(got), len(want))
		}
		q3 := data.RandomQueryBox3(rng, 1500)
		got3, _ := s.QueryRange3(q3, false)
		if want := data.BruteBox3IDs(q3); !sameInts(got3, want) {
			t.Fatalf("3D range mismatch at %d: got %d want %d", i, len(got3), len(want))
		}
	}
}

func TestPolygonMatchesBruteForce(t *testing.T) {
	data := NewDataset(12, 4000, 40)
	s := buildBoth(t, data)
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 200; i++ {
		line := data.RandomLine(rng)
		got, _ := s.QueryPoly(line, false)
		if want := data.BrutePolyHits(line.Rect()); !sameInts(got, want) {
			t.Fatalf("polygon mismatch at %d: got %d want %d", i, len(got), len(want))
		}
	}
}

func TestPolygonIsSubsetOfBoxHits(t *testing.T) {
	data := NewDataset(13, 2000, 20)
	s := buildBoth(t, data)
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 200; i++ {
		line := data.RandomLine(rng)
		boxHits, _ := s.QueryRange(line.Rect(), false)
		polyHits, _ := s.QueryPoly(line, false)
		inBox := map[int]bool{}
		for _, id := range boxHits {
			inBox[id] = true
		}
		for _, id := range polyHits {
			if !inBox[id] {
				t.Fatalf("exact hit %d not in bounding-box candidates", id)
			}
		}
	}
}

func TestNearestMatchesBruteForce(t *testing.T) {
	data := NewDataset(14, 4000, 20)
	s := buildBoth(t, data)
	rng := rand.New(rand.NewSource(6))
	for i := 0; i < 200; i++ {
		p := data.RandomPoint(rng)
		k := 1 + rng.Intn(15)
		got, _ := s.QueryNearest(2, p, k, 0, 0, false)
		gotIDs := make([]int, len(got))
		for j := range got {
			gotIDs[j] = got[j].ID
		}
		if want := data.BruteNearestIDs(p, k, 0, 2); !sameInts(gotIDs, want) {
			t.Fatalf("2D nearest mismatch at %d", i)
		}

		p3 := data.RandomPoint3(rng)
		dist := 50 + rng.Float64()*500
		got3, _ := s.QueryNearest(3, p3, -1, dist, 100000, false)
		got3IDs := make([]int, len(got3))
		for j := range got3 {
			got3IDs[j] = got3[j].ID
		}
		if want := data.BruteNearestIDs(p3, -1, dist, 3); !sameInts(got3IDs, want) {
			t.Fatalf("3D maxdist nearest mismatch at %d: got %d want %d", i, len(got3IDs), len(want))
		}
	}
}

func TestInsertDeleteStayConsistent(t *testing.T) {
	data := NewDataset(15, 3000, 10)
	s := buildBoth(t, data)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1500; i++ {
		switch {
		case i%3 == 0:
			tt := data.AddRandomTree(rng)
			s.Insert(tt)
		case i%3 == 1:
			tt := data.RandomTree(rng)
			if s.Remove(tt.ID) == nil {
				t.Fatalf("remove %d returned nil", tt.ID)
			}
		default:
			q := data.RandomQueryRect(rng, 900)
			got, _ := s.QueryRange(q, false)
			if want := data.BruteRectIDs(q); !sameInts(got, want) {
				t.Fatalf("post-mutation range mismatch at %d", i)
			}
		}
	}
	if err := s.T2.Check(); err != nil {
		t.Fatalf("2D invariant after churn: %v", err)
	}
	if err := s.T3.Check(); err != nil {
		t.Fatalf("3D invariant after churn: %v", err)
	}
	// Whole-set equality via a city-wide query.
	all := geom.NewBox(2, [3]float64{0, 0, 0}, [3]float64{20000, 20000, 0})
	got, _ := s.QueryRange(all, false)
	if len(got) != data.LiveCount() {
		t.Fatalf("city-wide query %d vs live %d", len(got), data.LiveCount())
	}
}

func TestSnapshotCounts(t *testing.T) {
	data := NewDataset(16, 5000, 5)
	s := buildBoth(t, data)
	st := s.Stats(2)
	if st.ItemCount != data.LiveCount() {
		t.Fatalf("item count %d vs live %d", st.ItemCount, data.LiveCount())
	}
	sum := 0
	for _, l := range st.Levels {
		if l.Level > 0 {
			sum += l.Nodes * l.Entries // not meaningful; just ensure populated
		}
	}
	if len(st.Levels) != st.Height {
		t.Fatalf("levels %d vs height %d", len(st.Levels), st.Height)
	}
	if st.TotalCoverage <= 0 || st.TotalOverlap < 0 {
		t.Fatalf("coverage %v overlap %v", st.TotalCoverage, st.TotalOverlap)
	}
}

func TestSmallVerificationPasses(t *testing.T) {
	rep := RunVerification(VerifyConfig{Trees: 800, Ops: 8000, Seed: 21, MaxEntries: 8, Workers: 4})
	if !rep.Passed {
		t.Fatalf("verification failed: %v", rep.MismatchLog)
	}
	if rep.Queries < 1000 {
		t.Fatalf("only %d queries ran", rep.Queries)
	}
}
