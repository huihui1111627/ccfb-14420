package viz

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ancienttrees/internal/geom"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(4000, 2, 42)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func doJSON(t *testing.T, h http.Handler, method, path, body string, out any) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil && rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: bad JSON: %v", path, err)
		}
	}
	return rec.Code
}

func TestEndpointsSmoke(t *testing.T) {
	s := testServer(t)
	h := s.Handler()

	var meta metaResponse
	if code := doJSON(t, h, "GET", "/api/meta", "", &meta); code != 200 {
		t.Fatalf("meta %d", code)
	}
	if meta.Records != 4000 || meta.Dim != 2 || meta.Height < 1 {
		t.Fatalf("bad meta: %+v", meta)
	}
	if len(meta.Overlap.Levels) != meta.Height+1 {
		t.Fatalf("overlap levels %d want %d", len(meta.Overlap.Levels), meta.Height+1)
	}
	if !meta.Build.Ready || meta.Build.BulkNodes == 0 {
		t.Fatalf("build comparison missing: %+v", meta.Build)
	}

	var tree struct {
		Nodes []struct {
			ID      int       `json:"id"`
			Level   int       `json:"level"`
			Leaf    bool      `json:"leaf"`
			Bounds  []float64 `json:"bounds"`
			Entries []struct {
				Child   int   `json:"child"`
				Records []int `json:"records"`
			} `json:"entries"`
		} `json:"nodes"`
		Height int `json:"height"`
	}
	if code := doJSON(t, h, "GET", "/api/tree", "", &tree); code != 200 {
		t.Fatalf("tree %d", code)
	}
	if len(tree.Nodes) != meta.Nodes {
		t.Fatalf("tree nodes %d want %d", len(tree.Nodes), meta.Nodes)
	}
	records := 0
	for _, n := range tree.Nodes {
		if n.Leaf {
			for _, e := range n.Entries {
				records += len(e.Records)
			}
		}
	}
	if records != 4000 {
		t.Fatalf("leaf records %d", records)
	}

	var q queryResponse
	body := `{"min":[-2000,-2000],"max":[2000,2000]}`
	if code := doJSON(t, h, "POST", "/api/query", body, &q); code != 200 {
		t.Fatalf("query %d", code)
	}
	if q.BruteCount != len(q.IDs) {
		t.Fatalf("query disagrees with brute force: %d vs %d", len(q.IDs), q.BruteCount)
	}
	if q.NodesVisited != len(q.Trace) {
		t.Fatalf("trace %d want %d", len(q.Trace), q.NodesVisited)
	}
	if q.NodesVisited == 0 || q.NodesVisited >= meta.Nodes {
		t.Fatalf("implausible visited count %d of %d", q.NodesVisited, meta.Nodes)
	}
	var traceHits int
	for _, v := range q.Trace {
		if v.Level < 0 || v.Level > tree.Height {
			t.Fatalf("bad trace level %d", v.Level)
		}
	}
	_ = traceHits

	var knn knnResponse
	if code := doJSON(t, h, "POST", "/api/knn", `{"x":0,"y":0,"k":6}`, &knn); code != 200 {
		t.Fatalf("knn %d", code)
	}
	if len(knn.Items) != 6 {
		t.Fatalf("knn items %d", len(knn.Items))
	}
	for i := 1; i < len(knn.Items); i++ {
		if knn.Items[i].Dist < knn.Items[i-1].Dist-1e-9 {
			t.Fatalf("knn not sorted")
		}
	}

	var red redlineResponse
	if code := doJSON(t, h, "POST", "/api/redline",
		`{"coords":[[-1000,-1000],[1000,-900],[900,1000],[-1100,800]]}`, &red); code != 200 {
		t.Fatalf("redline %d", code)
	}
	if len(red.CrownHits) != red.BruteHits {
		t.Fatalf("redline %d vs brute %d", len(red.CrownHits), red.BruteHits)
	}
	if len(red.CrownHits) > len(red.MBRHits) {
		t.Fatalf("crown hits exceed MBR hits")
	}
}

func TestRecordsEndpoint(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	var recs []recordDTO
	if code := doJSON(t, h, "GET", "/api/records?sample=50", "", &recs); code != 200 {
		t.Fatalf("records %d", code)
	}
	if len(recs) != 50 {
		t.Fatalf("sample %d", len(recs))
	}
	for _, r := range recs {
		if len(r.Crown) < 3 {
			t.Fatalf("record %d crown", r.ID)
		}
		want := geom.NewRectFromBounds(
			[]float64{r.MBR.Min(0), r.MBR.Min(1)},
			[]float64{r.MBR.Max(0), r.MBR.Max(1)},
		)
		if !r.MBR.Equal(want) {
			t.Fatalf("record %d bad mbr", r.ID)
		}
	}
	if code := doJSON(t, h, "GET", "/api/records?ids=1,2,3", "", &recs); code != 200 {
		t.Fatalf("records by id %d", code)
	}
	if len(recs) != 3 {
		t.Fatalf("by ids %d", len(recs))
	}
	if code := doJSON(t, h, "GET", "/api/records?minx=-500&miny=-500&maxx=500&maxy=500", "", &recs); code != 200 {
		t.Fatalf("records bbox %d", code)
	}
	for _, r := range recs {
		if !r.MBR.Intersects(geom.NewRectFromBounds([]float64{-500, -500}, []float64{500, 500})) {
			t.Fatalf("record %d outside bbox", r.ID)
		}
	}
}

func TestMutateAndDim(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	var mr mutateResponse
	if code := doJSON(t, h, "POST", "/api/mutate",
		`{"action":"insert","count":200,"cx":0,"cy":0,"radius":150}`, &mr); code != 200 {
		t.Fatalf("insert %d", code)
	}
	if mr.Inserted != 200 || mr.Records != 4200 {
		t.Fatalf("insert response %+v", mr)
	}
	if code := doJSON(t, h, "POST", "/api/mutate",
		`{"action":"delete","count":100,"cx":0,"cy":0,"radius":600}`, &mr); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if mr.Deleted != 100 || mr.Records != 4100 {
		t.Fatalf("delete response %+v", mr)
	}
	// Query must still match brute force after churn.
	var q queryResponse
	if code := doJSON(t, h, "POST", "/api/query", `{"min":[-3000,-3000],"max":[3000,3000]}`, &q); code != 200 {
		t.Fatalf("query after churn %d", code)
	}
	if q.BruteCount != len(q.IDs) {
		t.Fatalf("after churn: %d vs %d", len(q.IDs), q.BruteCount)
	}

	var dm map[string]any
	if code := doJSON(t, h, "POST", "/api/dim", `{"dim":3}`, &dm); code != 200 {
		t.Fatalf("dim %d", code)
	}
	if dm["dim"].(float64) != 3 {
		t.Fatalf("dim response %+v", dm)
	}
	// A 3D query with a date window must agree with brute force.
	var q3 queryResponse
	body := `{"min":[-3000,-3000],"max":[3000,3000],"fromDate":1700000000000,"toDate":1790000000000}`
	if code := doJSON(t, h, "POST", "/api/query", body, &q3); code != 200 {
		t.Fatalf("3D query %d", code)
	}
	if q3.BruteCount != len(q3.IDs) {
		t.Fatalf("3D: %d vs %d", len(q3.IDs), q3.BruteCount)
	}
	// The tree snapshot in 3D must be projected to the plane.
	var meta metaResponse
	doJSON(t, h, "GET", "/api/meta", "", &meta)
	if meta.Dim != 3 || !meta.Overlap.Projected {
		t.Fatalf("3D meta %+v", meta.Overlap.Projected)
	}
}

func TestABEndpoint(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	var ab abResponse
	if code := doJSON(t, h, "GET", "/api/ab", "", &ab); code != 200 {
		t.Fatalf("ab %d", code)
	}
	if !ab.SameAnswers {
		t.Fatal("reinsertion on/off changed answers")
	}
	if ab.OnReinsertOps == 0 {
		t.Fatal("expected reinsertion activity")
	}
	if ab.OnAvgNodes <= 0 || ab.OffAvgNodes <= 0 {
		t.Fatalf("bad averages %+v", ab)
	}
	if math.IsNaN(ab.NodesImprovement) {
		t.Fatal("nan improvement")
	}
}

func TestProjectRect(t *testing.T) {
	r := geom.NewRectFromBounds([]float64{1, 2, 3}, []float64{4, 5, 6})
	p := projectRect(r)
	if p.D != 2 || p.Min(0) != 1 || p.Max(0) != 4 || p.Min(1) != 2 || p.Max(1) != 5 {
		t.Fatalf("projection %v", p)
	}
}

func TestWithinEndpointMatchesBruteForce(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	// Pick a real tree so the disk has hits.
	var recs []recordDTO
	doJSON(t, h, "GET", "/api/records?sample=20", "", &recs)
	for _, rad := range []float64{40, 100, 250} {
		var res withinResponse
		body := `{"x":` + ftoa(recs[0].X) + `,"y":` + ftoa(recs[0].Y) + `,"radius":` + ftoa(rad) + `}`
		if code := doJSON(t, h, "POST", "/api/within", body, &res); code != 200 {
			t.Fatalf("within %d", code)
		}
		if res.BruteHits != len(res.Items) {
			t.Fatalf("radius %.0f: %d vs brute %d", rad, len(res.Items), res.BruteHits)
		}
		for i := 1; i < len(res.Items); i++ {
			if res.Items[i].Dist < res.Items[i-1].Dist-1e-9 {
				t.Fatalf("within results not sorted")
			}
		}
		for _, it := range res.Items {
			if it.Dist > rad+1e-9 {
				t.Fatalf("item %.3f m beyond radius %.0f", it.Dist, rad)
			}
		}
	}
}

func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestOverlapAndRebuildEndpoints(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	var ov struct {
		Levels     []map[string]any `json:"levels"`
		FillFactor float64          `json:"fillFactor"`
	}
	if code := doJSON(t, h, "GET", "/api/overlap", "", &ov); code != 200 {
		t.Fatalf("overlap %d", code)
	}
	if len(ov.Levels) == 0 || ov.FillFactor <= 0 {
		t.Fatalf("overlap payload %+v", ov)
	}
	var rb map[string]any
	if code := doJSON(t, h, "POST", "/api/rebuild", "", &rb); code != 200 {
		t.Fatalf("rebuild %d", code)
	}
	if rb["records"].(float64) != 4000 {
		t.Fatalf("rebuild records %v", rb["records"])
	}
	// Rebuild must not change answers.
	var q queryResponse
	doJSON(t, h, "POST", "/api/query", `{"min":[-2000,-2000],"max":[2000,2000]}`, &q)
	if q.BruteCount != len(q.IDs) {
		t.Fatalf("after rebuild %d vs %d", len(q.IDs), q.BruteCount)
	}
}

func TestJSONIsGzipped(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest("GET", "/api/tree", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("encoding %q", enc)
	}
}

func TestStaticPageServed(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("page %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"R*", "/api/query", "/api/knn", "canvas"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}
