// Command gosearch runs the search engine's HTTP server.
//
// Usage:
//
//	gosearch [-addr :9200] [-db sn.db]
//
// With -db, the server links the SQLite file and exposes the /api/db/tables and
// /api/db/search endpoints used by the console's database-search panel.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"gosearch/pkg/api"
	"gosearch/pkg/gosearcher"
	"gosearch/pkg/search"
	"gosearch/pkg/storage"
)

func main() {
	addr := flag.String("addr", ":9200", "HTTP listen address")
	dbPath := flag.String("db", "", "path to a SQLite file to link for table search (e.g. sn.db)")
	flag.Parse()

	server := api.NewServer(storage.NewStore(), search.NewIndex())

	if *dbPath != "" {
		db, err := sql.Open("sqlite", *dbPath)
		if err != nil {
			log.Fatalf("opening %s: %v", *dbPath, err)
		}
		defer db.Close()

		gs := gosearcher.NewGoSearcher().LinkDB(db)
		// Fail fast on a dud path or unreadable file instead of serving 500s.
		if _, err := gs.ListTables(context.Background()); err != nil {
			log.Fatalf("linked database %s is unusable: %v", *dbPath, err)
		}
		server.WithDB(gs)
		log.Printf("linked database %s for table search", *dbPath)
	}

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
