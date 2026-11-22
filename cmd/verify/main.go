// Command verify runs the interleaved workload and checks every index answer
// against a brute-force scan, then prints a report.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ancienttrees/internal/app"
)

func main() {
	var (
		n        = flag.Int("n", 30000, "number of generated trees")
		ops      = flag.Int("ops", 300000, "random operations")
		seed     = flag.Int64("seed", 20261123, "random seed")
		M        = flag.Int("m", 16, "max entries per node")
		pct      = flag.Int("reinsert-pct", 30, "forced reinsert percentage")
		workers  = flag.Int("workers", runtime.NumCPU(), "unused worker hint")
		jsonOut  = flag.String("json", "", "write the full JSON report to this path")
		cmpSeeds = flag.String("compare-seeds", "", "comma-separated seeds: compare clean incremental builds with reinsert on/off and exit")
	)
	flag.Parse()

	if *cmpSeeds != "" {
		var seeds []int64
		for _, part := range strings.Split(*cmpSeeds, ",") {
			v, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil {
				fmt.Fprintln(os.Stderr, "bad seed:", part)
				os.Exit(2)
			}
			seeds = append(seeds, v)
		}
		cmp := app.CompareReinsert(*n, *M, *pct, seeds)
		fmt.Printf("%-8s %18s %18s\n", "seed", "reinsert-on", "reinsert-off")
		for i := range cmp.Seeds {
			fmt.Printf("%-8d %18.0f %18.0f\n", cmp.Seeds[i], cmp.On[i], cmp.Off[i])
		}
		if *jsonOut != "" {
			b, _ := json.MarshalIndent(cmp, "", "  ")
			if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "write json:", err)
				os.Exit(2)
			}
		}
		return
	}

	last := time.Now()
	cfg := app.VerifyConfig{
		Trees: *n, Ops: *ops, Seed: *seed,
		MaxEntries: *M, ReinsertPercent: *pct, Workers: *workers,
		Progress: func(msg string) {
			if time.Since(last) < 2*time.Second {
				return
			}
			last = time.Now()
			fmt.Fprintln(os.Stderr, msg)
		},
	}
	rep := app.RunVerification(cfg)
	printReport(rep)
	if *jsonOut != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal json:", err)
			os.Exit(2)
		}
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write json:", err)
			os.Exit(2)
		}
	}
	if !rep.Passed {
		os.Exit(1)
	}
}

func printReport(r *app.VerifyReport) {
	fmt.Printf("== verification ==\n")
	fmt.Printf("trees=%d ops=%d seed=%d M=%d\n", r.ConfigEcho.Trees, r.OpsRun, r.ConfigEcho.Seed, r.ConfigEcho.MaxEntries)
	fmt.Printf("inserts=%d deletes=%d queries=%d live=%d elapsed=%dms\n",
		r.Inserts, r.Deletes, r.Queries, r.LiveTrees, r.ElapsedMS)
	fmt.Printf("mismatches=%d\n", r.Mismatches)
	for _, m := range r.MismatchLog {
		fmt.Println("  !", m)
	}
	fmt.Printf("\n%-28s %12s %12s %12s\n", "query", "reinsert-on", "reinsert-off", "result")
	for _, k := range []string{"range2d", "range3d", "polygon", "nearestK", "nearestIter", "nearestMaxDist"} {
		var on, off app.QuerySummary
		for _, m := range r.Modes {
			if m.ForceReinsert {
				on = m.Queries[k]
			} else {
				off = m.Queries[k]
			}
		}
		fmt.Printf("%-28s %12.2f %12.2f %12.2f\n", k, on.AvgNodes, off.AvgNodes, on.AvgResults)
	}
	fmt.Printf("\n%-28s %18s %18s\n", "structure", "reinsert-on", "reinsert-off")
	var on, off app.ModeSummary
	for _, m := range r.Modes {
		if m.ForceReinsert {
			on = m
		} else {
			off = m
		}
	}
	fmt.Printf("%-28s %18d %18d\n", "height", on.Height, off.Height)
	fmt.Printf("%-28s %18d %18d\n", "nodes", on.Nodes, off.Nodes)
	fmt.Printf("%-28s %18.0f %18.0f\n", "overlap area", on.TotalOverlap, off.TotalOverlap)
	fmt.Printf("%-28s %18.0f %18.0f\n", "coverage area", on.TotalCoverage, off.TotalCoverage)
	fmt.Printf("%-28s %18.4f %18.4f\n", "overlap / item", on.OverlapPerItem, off.OverlapPerItem)
	fmt.Printf("%-28s %18d %18d\n", "splits", on.Splits, off.Splits)
	fmt.Printf("%-28s %18d %18d\n", "forced reinsert ops", on.ReinsertOps, off.ReinsertOps)
	fmt.Printf("%-28s %18d %18d\n", "forced reinsert entries", on.ReinsertEntries, off.ReinsertEntries)
	fmt.Printf("%-28s %18d %18d\n", "condensed nodes", on.CondensedNodes, off.CondensedNodes)

	if on.Dims3 != nil {
		d3 := on.Dims3
		fmt.Printf("\n== 3D index (same mode, reinsert-on) ==\n")
		fmt.Printf("height=%d nodes=%d leaves=%d items=%d\n", d3.Height, d3.Nodes, d3.Leaves, d3.Items)
		fmt.Printf("overlap volume=%.4g coverage volume=%.4g splits=%d reinserts=%d/%d\n",
			d3.Overlap, d3.Coverage, d3.Splits, d3.ReinsertOps, d3.ReinsertEntries)
	}
	for _, m := range r.Modes {
		fmt.Printf("\nper-level overlap (%s, 2D):\n", m.Name)
		for _, l := range m.Levels {
			ratio := 0.0
			if l.Coverage > 0 {
				ratio = 100 * l.Overlap / l.Coverage
			}
			fmt.Printf("  L%d nodes=%-6d entries=%-7d overlap=%-14.4g coverage=%-14.4g %5.1f%%\n",
				l.Level, l.Nodes, l.Entries, l.Overlap, l.Coverage, ratio)
		}
	}

	fmt.Printf("\n== bulk (STR) vs incremental ==\n")
	for _, b := range r.Builds {
		fmt.Printf("%dD height bulk=%d incr=%d | nodes bulk=%d incr=%d | overlap bulk=%.0f incr=%.0f | avg nodes/query bulk=%.2f incr=%.2f | parity %d/%d\n",
			b.Dims, b.BulkHeight, b.IncrHeight, b.BulkNodes, b.IncrNodes,
			b.BulkOverlap, b.IncrOverlap, b.BulkAvgNodes, b.IncrAvgNodes,
			b.QueriesChecked-b.QueryMismatches, b.QueriesChecked)
	}
	if r.Passed {
		fmt.Printf("\nRESULT: PASS — every indexed answer matched brute force\n")
	} else {
		fmt.Printf("\nRESULT: FAIL\n")
	}
}
