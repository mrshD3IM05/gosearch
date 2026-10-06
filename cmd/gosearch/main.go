// Command gosearch runs the search engine's HTTP server.
//
// Usage:
//
//	gosearch [-addr :9200]
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gosearch/pkg/api"
	"gosearch/pkg/search"
	"gosearch/pkg/storage"
)

func main() {
	addr := flag.String("addr", ":9200", "HTTP listen address")
	flag.Parse()

	server := api.NewServer(storage.NewStore(), search.NewIndex())

	httpServer := &http.Server{
		Addr:    *addr,
		Handler: server.Handler(),
	}

	// Shut down gracefully on Ctrl-C / SIGTERM so in-flight requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("gosearch listening on %s", *addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
