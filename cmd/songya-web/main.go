package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aristath/songya"
	"github.com/aristath/songya/web"
)

func main() {
	storePath := flag.String("store", "", "Songya store directory")
	address := flag.String("listen", "127.0.0.1:8664", "HTTP listening address")
	flag.Parse()
	if *storePath == "" {
		log.Fatal("provide --store /path/to/store")
	}
	store, err := songya.Open(*storePath)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatal(err)
	}
	application := web.Handler(store)
	defer application.Close()
	server := &http.Server{Handler: application, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		// Stop persistent event streams and retain unfinished chat work before
		// waiting for the HTTP server to drain.
		_ = application.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("Songya is listening at http://%s\n", listener.Addr())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-done
}
