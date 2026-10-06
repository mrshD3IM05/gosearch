// Command webtest serves the standalone web console (web/) for live testing
// the gosearch backend.
//
// Since the pkg split, the backend no longer embeds a frontend. This tiny
// static server turns /web into a live-test unit: run it next to the real
// backend, edit web/index.html, reload the browser, and test the engine —
// no build step, no embed, no cache to fight.
//
// The console talks to the backend on API_BASE (default http://localhost:9200);
// the backend answers CORS preflights, so any origin works.
//
// Usage:
//
//	gosearch  -addr :9200          # backend, terminal 1
//	webtest   -addr :5000          # console,  terminal 2 → http://localhost:5000
//
//	webtest [-addr :5000] [-dir web]
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":5000", "HTTP listen address")
	dir := flag.String("dir", "web", "directory containing the web console")
	flag.Parse()

	log.Printf("webtest: serving %s on http://localhost%s (backend is %s)",
		*dir, *addr, "http://localhost:9200")
	log.Fatal(http.ListenAndServe(*addr, http.FileServer(http.Dir(*dir))))
}
