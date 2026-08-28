// mdd-core currently serves a read-only projection of durable state-event
// records. Live ingestion is added only after transactional storage is chosen.
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

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/core"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
)

func main() {
	eventsPath := flag.String("events", "", "path to saved MDD Go state records in NDJSON format")
	listen := flag.String("listen", "127.0.0.1:9080", "loopback HTTP listen address")
	ttl := flag.Duration("ttl", 30*time.Second, "fact freshness lifetime")
	flag.Parse()
	if *eventsPath == "" {
		fatalf("-events is required")
	}
	if !core.ValidateListenAddress(*listen) {
		fatalf("-listen must be a loopback address")
	}
	file, err := os.Open(*eventsPath)
	if err != nil {
		fatalf("open events: %v", err)
	}
	replay, err := events.NewReplay(*ttl)
	if err == nil {
		err = events.ReadJSONLines(file, replay, events.DefaultMaxRecordBytes)
	}
	closeErr := file.Close()
	if err != nil {
		fatalf("load events: %v", err)
	}
	if closeErr != nil {
		fatalf("close events: %v", closeErr)
	}
	if replay.LastReceivedAt().IsZero() {
		fatalf("event file contains no records")
	}
	server := &http.Server{
		Addr: *listen, Handler: core.NewServer(replay, time.Now),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fatalf("listen: %v", err)
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	shutdownSignal, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	log.Printf("mdd-core read-only replay listening on %s", listener.Addr())
	select {
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			fatalf("serve: %v", err)
		}
	case <-shutdownSignal.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := server.Shutdown(shutdownContext)
		cancel()
		if err != nil {
			fatalf("shutdown: %v", err)
		}
		if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
			fatalf("serve after shutdown: %v", err)
		}
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mdd-core: "+format+"\n", args...)
	os.Exit(2)
}
