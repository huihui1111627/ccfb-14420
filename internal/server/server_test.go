package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ancienttrees/internal/app"
	"ancienttrees/internal/geom"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	w := NewWorld(7, 1500, app.IndexOptions{MaxEntries: 8, ForceReinsert: true, ReinsertPercent: 30})
	return httptest.NewServer(Handler(w))
}

func TestStatsEndpoint(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	var out map[string]any
	getJSON(t, ts.URL+"/api/stats?dims=2", &out)
	if out["itemCount"].(float64) <= 0 {
		t.Fatalf("bad stats: %v", out)
	}
	if out["forceReinsert"] != true {
		t.Fatalf("forceReinsert missing")
	}
	if _, ok := out["levels"].([]any); !ok {
		t.Fatalf("levels missing")
	}
}

func TestQueryEndpointsMatchBruteForce(t *testing.T) {
	w := NewWorld(9, 1500, app.IndexOptions{MaxEntries: 8, ForceReinsert: false, ReinsertPercent: 30})
	ts := httptest.NewServer(Handler(w))
	defer ts.Close()

	var rng struct{}
	_ = rng
	for _, kind := range []string{"range", "nearest"} {
		url := ts.URL + "/api/query?kind=" + kind + "&dims=2&x0=4000&y0=4000&x1=5500&y1=5500&x=4800&y=4800&k=10"
		var out queryResponse
		getJSON(t, url, &out)
		if out.NodesVisited <= 0 || out.EntriesExamined < out.NodesVisited {
			t.Fatalf("%s visited stats look wrong: %+v", kind, out)
		}
		if out.Count != len(out.Results) {
			t.Fatalf("%s count mismatch", kind)
		}
	}

	// range answers must equal brute force
	var out queryResponse
	getJSON(t, ts.URL+"/api/query?kind=range&dims=2&x0=4000&y0=4000&x1=5500&y1=5500", &out)
	ids := make([]int, len(out.Results))
	for i, r := range out.Results {
		ids[i] = r.ID
	}
	want := w.Data.BruteRectIDs(geom.NewBox(2, [3]float64{4000, 4000, 0}, [3]float64{5500, 5500, 0}))
	if len(ids) != len(want) {
		t.Fatalf("api range %d vs brute %d", len(ids), len(want))
	}
	for i := range ids {
		if ids[i] != want[i] {
			t.Fatalf("api range mismatch at %d", i)
		}
	}
}

func TestPolygonAndThreeD(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	var out queryResponse
	getJSON(t, ts.URL+"/api/query?kind=polygon&dims=2&line=0", &out)
	if out.Query == nil || out.Query.Poly == nil {
		t.Fatalf("polygon query shape missing")
	}
	var out3 queryResponse
	getJSON(t, ts.URL+"/api/query?kind=range&dims=3&x0=0&y0=0&x1=20000&y1=20000&d0=0&d1=12", &out3)
	if out3.Dims != 3 || out3.Count == 0 {
		t.Fatalf("3D range returned %+v", out3)
	}
}

func TestInsertDeleteEndpoints(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	var stats map[string]any
	getJSON(t, ts.URL+"/api/stats", &stats)
	before := stats["itemCount"].(float64)

	body := strings.NewReader(`{"x": 5200, "y": 5600}`)
	res, err := http.Post(ts.URL+"/api/insert", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	var ins treePayload
	json.NewDecoder(res.Body).Decode(&ins)
	res.Body.Close()

	getJSON(t, ts.URL+"/api/stats", &stats)
	if stats["itemCount"].(float64) != before+1 {
		t.Fatalf("insert did not grow index")
	}

	res, err = http.Post(ts.URL+"/api/delete", "application/json",
		strings.NewReader(`{"id": `+itoa(ins.ID)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	getJSON(t, ts.URL+"/api/stats", &stats)
	if stats["itemCount"].(float64) != before {
		t.Fatalf("delete did not shrink index")
	}
}

func TestPageServed(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b := make([]byte, 4096)
	n, _ := res.Body.Read(b)
	if !strings.Contains(string(b[:n]), "R*") {
		t.Fatalf("page did not look right")
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
