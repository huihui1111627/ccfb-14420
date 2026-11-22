package forest

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"ancienttrees/internal/geom"
)

func genSmall(t *testing.T) []*Record {
	t.Helper()
	gen := DefaultGenConfig(7)
	gen.N = 3000
	gen.Clusters = 12
	recs, clusters := Generate(gen, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC))
	if len(recs) != 3000 {
		t.Fatalf("generated %d", len(recs))
	}
	if len(clusters) != 12 {
		t.Fatalf("clusters %d", len(clusters))
	}
	// Determinism.
	recs2, _ := Generate(gen, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC))
	for i := range recs {
		if recs[i].X != recs2[i].X || recs[i].InspectAt != recs2[i].InspectAt {
			t.Fatalf("generator is not deterministic at %d", i)
		}
	}
	return recs
}

func TestGenerateInvariants(t *testing.T) {
	recs := genSmall(t)
	for _, r := range recs {
		if r.Crown.RingLen() < 3 {
			t.Fatalf("record %d crown has %d vertices", r.ID, r.Crown.RingLen())
		}
		mbr := r.Crown.BoundingRect()
		if !mbr.Equal(r.MBR) {
			t.Fatalf("record %d MBR mismatch", r.ID)
		}
		if r.MBR3.D != 3 || r.MBR3.Min(2) != r.MBR3.Max(2) {
			t.Fatalf("record %d bad 3D MBR", r.ID)
		}
		wantZ := DateAxis(r.InspectAt)
		if r.MBR3.Min(2) != wantZ {
			t.Fatalf("record %d date axis %v want %v", r.ID, r.MBR3.Min(2), wantZ)
		}
		if r.Height <= 0 || r.DBH <= 0 || r.Age <= 0 {
			t.Fatalf("record %d non-positive measurements", r.ID)
		}
		if !mbr.ContainsPoint([]float64{r.X, r.Y}) {
			t.Fatalf("record %d centre outside its MBR", r.ID)
		}
	}
}

func TestClusteredDistribution(t *testing.T) {
	recs := genSmall(t)
	// The generator must produce tight clusters: median distance to the
	// record's district centroid is small compared with a uniform spread.
	byD := map[int][]*Record{}
	for _, r := range recs {
		if r.District >= 0 {
			byD[r.District] = append(byD[r.District], r)
		}
	}
	if len(byD) < 8 {
		t.Fatalf("only %d populated districts", len(byD))
	}
	var spreads []float64
	for _, rs := range byD {
		var cx, cy float64
		for _, r := range rs {
			cx += r.X
			cy += r.Y
		}
		cx /= float64(len(rs))
		cy /= float64(len(rs))
		var s float64
		for _, r := range rs {
			s += math.Hypot(r.X-cx, r.Y-cy)
		}
		spreads = append(spreads, s/float64(len(rs)))
	}
	sort.Float64s(spreads)
	if spreads[len(spreads)/2] > 1200 {
		t.Fatalf("clusters too loose: median spread %.0f m", spreads[len(spreads)/2])
	}
}

func TestRangeQueriesMatchBruteForce(t *testing.T) {
	recs := genSmall(t)
	rng := rand.New(rand.NewSource(2))
	s2, _ := NewSurvey(recs, 2)
	s2.BulkLoad()
	for i := 0; i < 200; i++ {
		r := recs[rng.Intn(len(recs))]
		w := 50 + rng.Float64()*1500
		box := geom.NewRectFromBounds(
			[]float64{r.X - w, r.Y - w}, []float64{r.X + w, r.Y + w})
		got := s2.Index.SearchRect(box)
		want := BruteForceRange(recs, box)
		if len(got) != len(want) {
			t.Fatalf("query %d: %d vs %d", i, len(got), len(want))
		}
	}
}

func TestCrownQueriesMatchBruteForce(t *testing.T) {
	recs := genSmall(t)
	rng := rand.New(rand.NewSource(3))
	s, _ := NewSurvey(recs, 2)
	for _, r := range recs {
		s.InsertRecord(r)
	}
	for i := 0; i < 120; i++ {
		r := recs[rng.Intn(len(recs))]
		w := 20 + rng.Float64()*300
		h := 20 + rng.Float64()*300
		red := geom.NewRectFromBounds(
			[]float64{r.X - w/2, r.Y - h/2}, []float64{r.X + w/2, r.Y + h/2})
		got := s.CrownsIntersecting(red)
		want := BruteForceCrowns(recs, red)
		if len(got) != len(want) {
			t.Fatalf("rect red line %d: %d vs %d", i, len(got), len(want))
		}
		poly := GenerateRedLine(rng, r.X, r.Y, w, h, rng.Float64()*math.Pi)
		gp := s.CrownsIntersectingPolygon(poly)
		wp := BruteForceCrownPolygon(recs, poly)
		if len(gp) != len(wp) {
			t.Fatalf("polygon red line %d: %d vs %d", i, len(gp), len(wp))
		}
	}
}

func TestNearestMatchesBruteForce(t *testing.T) {
	recs := genSmall(t)
	rng := rand.New(rand.NewSource(4))
	s, _ := NewSurvey(recs, 2)
	for _, r := range recs {
		s.InsertRecord(r)
	}
	for i := 0; i < 60; i++ {
		var p geom.Point
		if i%2 == 0 {
			r := recs[rng.Intn(len(recs))]
			p = geom.Point{X: r.X, Y: r.Y}
		} else {
			p = geom.Point{X: (rng.Float64()*2 - 1) * 16000, Y: (rng.Float64()*2 - 1) * 16000}
		}
		k := 1 + rng.Intn(15)
		got := s.NearestK(p, k)
		want := BruteForceKNN(recs, p, k)
		if len(got) != len(want) {
			t.Fatalf("knn %d: %d vs %d", i, len(got), len(want))
		}
		for j := range want {
			if got[j].Record.ID != want[j].Record.ID {
				t.Fatalf("knn %d pos %d: id %d vs %d", i, j, got[j].Record.ID, want[j].Record.ID)
			}
			if math.Abs(got[j].Dist-want[j].Dist) > 1e-9 {
				t.Fatalf("knn %d pos %d: dist %v vs %v", i, j, got[j].Dist, want[j].Dist)
			}
		}
		// Streaming form must agree.
		stream := s.NearestStream(p, k)
		if len(stream) != len(want) {
			t.Fatalf("stream %d: %d vs %d", i, len(stream), len(want))
		}
		for j := range want {
			if stream[j].Record.ID != want[j].Record.ID {
				t.Fatalf("stream %d pos %d: id %d vs %d", i, j, stream[j].Record.ID, want[j].Record.ID)
			}
		}
	}
}

func TestTreesWithin(t *testing.T) {
	recs := genSmall(t)
	rng := rand.New(rand.NewSource(6))
	s, _ := NewSurvey(recs, 2)
	for _, r := range recs {
		s.InsertRecord(r)
	}
	for i := 0; i < 40; i++ {
		r := recs[rng.Intn(len(recs))]
		p := geom.Point{X: r.X, Y: r.Y}
		rad := 20 + rng.Float64()*120
		got := s.TreesWithin(p, rad)
		var want []NearestRecord
		for _, rec := range recs {
			d := geom.DistanceToPolygon(p, rec.Crown)
			if d <= rad {
				want = append(want, NearestRecord{Record: rec, Dist: d})
			}
		}
		sort.SliceStable(want, func(a, b int) bool {
			if want[a].Dist != want[b].Dist {
				return want[a].Dist < want[b].Dist
			}
			return want[a].Record.ID < want[b].Record.ID
		})
		if len(got) != len(want) {
			t.Fatalf("radius query %d: %d vs %d", i, len(got), len(want))
		}
		for j := range want {
			if got[j].Record.ID != want[j].Record.ID {
				t.Fatalf("radius query %d pos %d id mismatch", i, j)
			}
		}
	}
}

func Test3DRangeWithDateFilter(t *testing.T) {
	recs := genSmall(t)
	rng := rand.New(rand.NewSource(10))
	s, _ := NewSurvey(recs, 3)
	for _, r := range recs {
		s.InsertRecord(r)
	}
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 100; i++ {
		r := recs[rng.Intn(len(recs))]
		box2 := geom.NewRectFromBounds(
			[]float64{r.X - 400, r.Y - 400}, []float64{r.X + 400, r.Y + 400})
		from := now - int64(rng.Float64()*6*365)*DayMillis
		to := from + int64(30+rng.Float64()*400)*DayMillis
		box := QueryBox3D(box2, from, to)
		got := s.Index.SearchRect(box)
		want := 0
		for _, rec := range recs {
			if rec.MBR.Intersects(box2) && rec.InspectAt >= from && rec.InspectAt <= to {
				want++
			}
		}
		if len(got) != want {
			t.Fatalf("3D query %d: %d vs %d", i, len(got), want)
		}
	}
}

func TestDateAxisKeepsAxesComparable(t *testing.T) {
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC).UnixMilli()
	sixYears := int64(6 * 365 * DayMillis)
	span := DateAxis(now) - DateAxis(now-sixYears)
	// The inspection window should be within ~1 order of magnitude of a
	// 40 km city extent, not 1e7 times larger as raw milliseconds would be.
	if span < 20000 || span > 400000 {
		t.Fatalf("6-year date axis span %.0f is not comparable to metres", span)
	}
	// A 90-day slice must be a thin but non-degenerate band.
	z0, z1 := DateRangeAxis(now-90*DayMillis, now)
	if z1-z0 < 100 {
		t.Fatalf("90-day band too thin: %.1f", z1-z0)
	}
}

// In 3D mode the crowned-tree and nearest-neighbour queries must still be
// planar: they use the dedicated 2D index, not the date axis.
func TestCrownQueriesIn3DStayPlanar(t *testing.T) {
	recs := genSmall(t)
	rng := rand.New(rand.NewSource(21))
	s, err := NewSurvey(recs, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		s.InsertRecord(r)
	}
	for i := 0; i < 40; i++ {
		r := recs[rng.Intn(len(recs))]
		poly := GenerateRedLine(rng, r.X, r.Y, 80+rng.Float64()*400, 60+rng.Float64()*300, rng.Float64()*math.Pi)
		got := s.CrownsIntersectingPolygon(poly)
		want := BruteForceCrownPolygon(recs, poly)
		if len(got) != len(want) {
			t.Fatalf("3D polygon red line %d: %d vs %d", i, len(got), len(want))
		}
		p := geom.Point{X: r.X, Y: r.Y}
		k := 1 + rng.Intn(10)
		gotK := s.NearestK(p, k)
		wantK := BruteForceKNN(recs, p, k)
		if len(gotK) != len(wantK) {
			t.Fatalf("3D knn %d: %d vs %d", i, len(gotK), len(wantK))
		}
		// A tree's own crown contains its trunk, so the nearest tree to a
		// trunk must be that tree (distance 0) at least sometimes; the strict
		// check is the id match against brute force.
		for j := range wantK {
			if gotK[j].Record.ID != wantK[j].Record.ID {
				t.Fatalf("3D knn %d pos %d: %d vs %d", i, j, gotK[j].Record.ID, wantK[j].Record.ID)
			}
		}
	}
	// Deleting must remove the record from both indexes.
	victim := recs[0]
	if !s.DeleteRecord(victim) {
		t.Fatal("delete failed")
	}
	for _, d := range s.Space2D.SearchRect(victim.MBR) {
		if d.(*Record).ID == victim.ID {
			t.Fatal("record still present in the planar index after delete")
		}
	}
	for _, d := range s.Index.SearchRect(forestQueryBox(victim)) {
		if d.(*Record).ID == victim.ID {
			t.Fatal("record still present in the range index after delete")
		}
	}
}

func forestQueryBox(r *Record) geom.Rect {
	return geom.NewRectFromBounds(
		[]float64{r.MBR.Min(0), r.MBR.Min(1), math.Inf(-1)},
		[]float64{r.MBR.Max(0), r.MBR.Max(1), math.Inf(1)},
	)
}
