// Command verify runs the exhaustive differential test of the ancient-tree
// R*-tree against brute force and writes a JSON report.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ancienttrees/internal/verify"
)

func main() {
	seed := flag.Int64("seed", 20261122, "random seed (fully reproducible run)")
	n := flag.Int("n", 45000, "number of ancient trees")
	ops := flag.Int("ops", 260000, "number of mixed operations")
	clusters := flag.Int("clusters", 55, "number of urban clusters")
	days := flag.Int("days", 365*6, "inspection date lookback in days")
	out := flag.String("out", "", "JSON report path (default: print summary only)")
	quiet := flag.Bool("quiet", false, "no progress output")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var lastLine time.Time
	opt := verify.DefaultOptions(*seed)
	opt.N = *n
	opt.Ops = *ops
	opt.Clusters = *clusters
	opt.DaysBack = *days
	opt.Progress = func(p verify.Progress) {
		if *quiet {
			return
		}
		if time.Since(lastLine) < 150*time.Millisecond && p.Done != p.Total {
			return
		}
		lastLine = time.Now()
		fmt.Fprintf(os.Stderr, "\r[%-14s] %6.1f%%  %s        ", p.Phase, p.Percent, p.Message)
		if p.Done == p.Total {
			fmt.Fprintln(os.Stderr)
		}
	}

	rep, err := verify.Run(ctx, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nverify failed:", err)
		os.Exit(1)
	}
	if !*quiet {
		fmt.Fprintln(os.Stderr)
	}
	printSummary(rep)
	if *out != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot marshal report:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("report:", *out)
	}
	if !rep.Passed {
		os.Exit(2)
	}
}

func printSummary(r *verify.Report) {
	fmt.Printf("== ancient-tree R*-tree verification ==\n")
	fmt.Printf("trees            %d   mixed ops %d   seed %d\n", r.Records, r.OpsRun, r.Options.Seed)
	fmt.Printf("result           %s\n", r.Message)
	fmt.Printf("duration         %.1fs\n", float64(r.DurationMS)/1000)
	fmt.Printf("ops              %v\n", r.OpStats)
	fmt.Printf("\n-- insertion-built 2D tree --\n")
	fmt.Printf("height %d  nodes %d  leaves %d\n", r.Index2D.Height, r.Index2D.Nodes, r.Index2D.Leaves)
	fmt.Printf("avg nodes/query %.2f   avg leaf entries/query %.2f\n", r.Index2D.AvgNodesPerQuery, r.Index2D.AvgLeafPerQuery)
	fmt.Printf("\n-- STR bulk load vs insertion --\n")
	fmt.Printf("height %d vs %d   nodes %d vs %d\n", r.Bulk.BulkHeight, r.Bulk.InsertHeight, r.Bulk.BulkNodes, r.Bulk.InsertNodes)
	fmt.Printf("leaf fill %.3f vs %.3f   avg nodes/query %.2f vs %.2f   queries compared %d\n",
		r.Bulk.BulkLeafFill, r.Bulk.InsertLeafFill, r.Bulk.BulkAvgNodes, r.Bulk.InsertAvgNodes, r.Bulk.QueriesCompared)
	fmt.Printf("\n-- forced reinsertion on/off (same %d queries) --\n", r.ReinsertAB.Queries)
	fmt.Printf("avg nodes/query  %.2f vs %.2f  (%.1f%% fewer with reinsertion)\n",
		r.ReinsertAB.OnAvgNodes, r.ReinsertAB.OffAvgNodes, r.ReinsertAB.NodesImprovement)
	fmt.Printf("avg leaf/query   %.2f vs %.2f\n", r.ReinsertAB.OnAvgLeaf, r.ReinsertAB.OffAvgLeaf)
	fmt.Printf("splits           %d vs %d   reinsert ops %d (%d entries)\n",
		r.ReinsertAB.OnSplits, r.ReinsertAB.OffSplits, r.ReinsertAB.OnReinsertOps, r.ReinsertAB.OnReinserted)
	fmt.Printf("total overlap    %.6g vs %.6g   cover %.6g vs %.6g\n",
		r.ReinsertAB.OnOverlapArea, r.ReinsertAB.OffOverlapArea, r.ReinsertAB.OnCoverArea, r.ReinsertAB.OffCoverArea)
	fmt.Printf("same answers     %v\n", r.ReinsertAB.SameAnswers)
	fmt.Printf("\n-- 3D (date axis) --\n")
	fmt.Printf("height %d  nodes %d  avg nodes/query %.2f\n", r.Index3D.Height, r.Index3D.Nodes, r.Index3D.AvgNodesPerQuery)
	fmt.Printf("\n-- build time --\n")
	fmt.Printf("insertion %dms   bulk %dms   A/B %dms\n", r.BuildStats.InsertionMS, r.BuildStats.BulkMS, r.BuildStats.ABInsertMS)
	if len(r.Failures) > 0 {
		fmt.Printf("\n!! %d failures\n", len(r.Failures))
		for i, f := range r.Failures {
			if i >= 10 {
				fmt.Printf("... and %d more\n", len(r.Failures)-10)
				break
			}
			fmt.Printf("  [%s] step %d want %d got %d %s %s\n", f.Op, f.Step, f.Want, f.Got, f.Detail, f.Extra)
		}
	}
	if len(r.Structure.Failures) > 0 {
		fmt.Printf("structure: %v\n", r.Structure.Failures)
	}
}
