// Command treeindex serves the ancient-tree spatial index and its structure
// visualisation page.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"ancienttrees/internal/app"
	"ancienttrees/internal/server"
)

func main() {
	var (
		addr  = flag.String("addr", ":8080", "listen address")
		trees = flag.Int("trees", 20000, "generated trees")
		seed  = flag.Int64("seed", 20261123, "random seed")
		M     = flag.Int("m", 16, "max entries per node")
		reins = flag.Bool("reinsert", true, "forced reinsertion instead of immediate splits")
		pct   = flag.Int("reinsert-pct", 30, "forced reinsert percentage")
		bulk  = flag.Bool("bulk", true, "STR bulk load instead of per-row insert")
	)
	flag.Parse()

	opts := app.IndexOptions{MaxEntries: *M, ForceReinsert: *reins, ReinsertPercent: *pct}
	start := time.Now()
	data := app.NewDataset(*seed, *trees, *trees/25+1)
	store := app.BuildStore(data, opts, *bulk, *seed+1)
	world := &server.World{Data: data, Store: store, Opts: opts, Seed: *seed}
	mode := "STR bulk"
	if !*bulk {
		mode = "incremental"
	}
	fmt.Printf("indexed %d trees (%s, M=%d, reinsert=%v) in %v\n",
		data.LiveCount(), mode, *M, *reins, time.Since(start).Round(time.Millisecond))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(world),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("serving http://localhost%s/\n", *addr)
	log.Fatal(srv.ListenAndServe())
}
