// Command serve runs the ancient-tree R*-tree structure explorer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ancienttrees/internal/viz"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8420", "listen address")
	n := flag.Int("n", 45000, "number of ancient trees in the demo city")
	dim := flag.Int("dim", 2, "index dimension: 2 (planar) or 3 (planar + inspection date)")
	seed := flag.Int64("seed", 20261122, "generator seed")
	open := flag.Bool("open", false, "print the page URL on startup")
	flag.Parse()

	if *dim != 2 && *dim != 3 {
		fmt.Fprintln(os.Stderr, "-dim must be 2 or 3")
		os.Exit(2)
	}
	srv, err := viz.NewServer(*n, *dim, *seed)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	url := "http://" + ln.Addr().String() + "/"
	if *open {
		fmt.Println("古树名木 R* 树索引结构:", url)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
