package verify

import (
	"context"
	"testing"
	"time"
)

// TestSmallRunMatchesBruteForce is the same differential test as the full CLI
// run, at a size that fits in a unit test. It must find zero divergences.
func TestSmallRunMatchesBruteForce(t *testing.T) {
	opt := Options{
		Seed: 20261122, N: 2500, Ops: 12000, Clusters: 10, DaysBack: 365 * 6,
	}
	rep, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed {
		for i, f := range rep.Failures {
			if i >= 8 {
				break
			}
			t.Errorf("failure [%s] step %d: %s want %d got %d %s", f.Op, f.Step, f.Detail, f.Want, f.Got, f.Extra)
		}
		t.Fatalf("%d failures", len(rep.Failures))
	}
	if rep.OpsRun < opt.Ops/2 {
		t.Fatalf("only %d ops ran", rep.OpsRun)
	}
	// Every operation family must have been exercised.
	for _, op := range []string{"range2d", "range3d", "knn", "stream", "redline_rect", "redline_polygon", "delete", "insert"} {
		if rep.OpStats[op] == 0 {
			t.Errorf("operation %q never ran", op)
		}
	}
	if !rep.ReinsertAB.SameAnswers {
		t.Error("A/B answered differently")
	}
	if rep.ReinsertAB.OnReinsertOps == 0 {
		t.Error("forced reinsertion never fired")
	}
	if rep.Bulk.QueriesCompared == 0 {
		t.Error("bulk comparison did not run")
	}
}

func TestCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	opt := Options{Seed: 1, N: 30000, Ops: 100000, Clusters: 40, DaysBack: 365}
	_, err := Run(ctx, opt)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}
