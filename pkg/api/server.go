// Package api exposes the search engine over HTTP.
//
// Phase 1 endpoints (document storage):
//
//	PUT    /index/{index}/{id}   index (create/replace) a JSON document
//	GET    /index/{index}/{id}   retrieve a document
//	DELETE /index/{index}/{id}   delete a document
//	GET    /index                list index names (debugging)
//	GET    /healthz              liveness probe
//
// Phase 3 endpoint (search):
//
//	GET    /api/search?index=NAME&q=QUERY&limit=N
//	       AND-search over the inverted index, hits sorted by TF-IDF score
//
// Database table search (only when a database is linked via WithDB:
// cmd/gosearch -db sn.db):
//
//	GET    /api/db/tables
//	       list table/view names for building table pickers
//	GET    /api/db/search?table=T&q=QUERY&search=C1,C2&return=R1,R2&limit=N
//	       search rows of a table live. `search` picks the matching columns,
//	       `return` the columns in the result (empty = all). Both are CSV.
//
// Tooling endpoint:
//
//	POST   /samples?index=NAME   bulk-load the embedded course-title corpus
//
// The standalone web console in /web is a live-test unit served separately by
// cmd/webtest (or opened straight from disk). Because it runs on a different
// origin, every backend response carries permissive CORS headers and answers
// OPTIONS preflights, so the console can call this API directly.
//
// Route patterns use the method-aware net/http ServeMux introduced in
// Go 1.22, so no third-party router is needed.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"gosearch/pkg/gosearcher"
	"gosearch/pkg/samples"
	"gosearch/pkg/search"
	"gosearch/pkg/storage"
)

// The web console is no longer embedded here; the pkg split moved it to /web.

// maxBodyBytes limits request bodies; documents are expected to be small.
const maxBodyBytes = 4 << 20 // 4 MiB

// defaultSearchLimit / maxSearchLimit bound the limit query parameter.
const (
	defaultSearchLimit = 20
	maxSearchLimit     = 100
)

// Server wires HTTP handlers to the document store and the inverted index.
// The two are kept in sync here: every successful write updates both. An
// optional GoSearcher (WithDB) adds the /api/db/* table-search endpoints.
type Server struct {
	store   *storage.Store
	idx     *search.Index
	db      *gosearcher.GoSearcher
	mux     *http.ServeMux
	handler http.Handler
}

// NewServer builds the HTTP routing tree around store and idx. The returned
// handler is wrapped in a CORS layer so the standalone web console (served on
// a different origin by cmd/webtest) can call it cross-origin.
func NewServer(store *storage.Store, idx *search.Index) *Server {
	s := &Server{store: store, idx: idx}
	s.register()
	return s
}

// WithDB attaches a database-backed GoSearcher so the /api/db/* endpoints come
// up. Call it before serving: it rebuilds the routing tree. Returns the
// receiver for chaining.
func (s *Server) WithDB(db *gosearcher.GoSearcher) *Server {
	s.db = db
	s.register()
	return s
}

// register builds the routing tree. It is called by NewServer and again by
// WithDB once a database is attached.
func (s *Server) register() {
	s.mux = http.NewServeMux()

	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	s.mux.HandleFunc("GET /index", s.handleListIndices)
	s.mux.HandleFunc("PUT /index/{index}/{id}", s.handlePut)
	s.mux.HandleFunc("GET /index/{index}/{id}", s.handleGet)
	s.mux.HandleFunc("DELETE /index/{index}/{id}", s.handleDelete)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("POST /samples", s.handleLoadSamples)

	if s.db != nil {
		s.mux.HandleFunc("GET /api/db/tables", s.handleDBTables)
		s.mux.HandleFunc("GET /api/db/search", s.handleDBSearch)
	}

	s.handler = withCORS(s.mux)
}

// indexDoc writes a document to both the store and the inverted index in one
// place. Every document-creating path must go through here so the two can
// never drift silently. The usual caveat applies: no cross-structure
// transaction, a crash between the calls can leave the index stale.
func (s *Server) indexDoc(index, id string, fields map[string]any) {
	s.store.Put(index, id, storage.Document{ID: id, Fields: fields})
	s.idx.Add(index, id, fields)
}

// withCORS wraps a handler with permissive CORS headers so the separate web
// console can call the API from any origin (e.g. http://localhost:5000 served
// by cmd/webtest, or a file:// page). Browser preflight OPTIONS requests are
// answered here, before the route table sees them.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns the root handler (useful for httptest and ListenAndServe).
func (s *Server) Handler() http.Handler { return s.handler }

// handlePut stores a JSON document body under the path's index and id.
//
// Body shape is either the full document {"id":...,"fields":{...}} or a bare
// fields object such as {"name":"widget"}. Bare objects are the friendlier
// default (like Elasticsearch's PUT /index/_doc/{id} with a plain source),
// so any object without a "fields" key is treated as the fields themselves.
// Known trade-off: a top-level field literally named "fields" must itself be
// a JSON object or it will be misinterpreted as the fields container.
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	index := r.PathValue("index")
	id := r.PathValue("id")

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber() // keep integers as json.Number instead of forcing float64

	var val any
	if err := dec.Decode(&val); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	// Reject trailing garbage after the first JSON value.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "body must contain a single JSON object")
		return
	}

	obj, ok := val.(map[string]any)
	if !ok {
		writeError(w, http.StatusBadRequest, "body must be a JSON object")
		return
	}

	doc := storage.Document{ID: id}
	if f, has := obj["fields"]; has {
		fields, ok := f.(map[string]any)
		if !ok {
			writeError(w, http.StatusBadRequest, "`fields` must be a JSON object")
			return
		}
		doc.Fields = fields
	} else {
		doc.Fields = obj
	}

	// Keep store and inverted index in sync (see indexDoc for caveats).
	s.indexDoc(index, id, doc.Fields)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": "created_or_replaced",
		"index":  index,
		"_id":    id,
	})
}

// handleGet returns a stored document or 404.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	index, id := r.PathValue("index"), r.PathValue("id")

	doc, ok := s.store.Get(index, id)
	if !ok {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// handleDelete removes a document or returns 404 when it does not exist.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	index, id := r.PathValue("index"), r.PathValue("id")

	if !s.store.Delete(index, id) {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	s.idx.Delete(index, id) // mirror the storage delete; best-effort as above
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": "deleted",
		"index":  index,
		"_id":    id,
	})
}

// handleListIndices returns the names of all non-empty indices.
func (s *Server) handleListIndices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"indices": s.store.Indices(),
	})
}

// handleLoadSamples bulk-indexes the embedded course-title corpus into the
// requested index (default "courses") as documents {"title": <title>} with
// IDs "1".."N". Re-loading is idempotent: it re-indexes the same IDs.
//
//	POST /samples?index=courses
func (s *Server) handleLoadSamples(w http.ResponseWriter, r *http.Request) {
	index := r.URL.Query().Get("index")
	if index == "" {
		index = "courses"
	}

	titles := samples.CourseTitles()
	for i, title := range titles {
		id := strconv.Itoa(i + 1)
		s.indexDoc(index, id, map[string]any{"title": title})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result":  "sample_data_loaded",
		"index":   index,
		"loaded":  len(titles),
		"example": "try /api/search?index=" + index + "&q=elasticsearch",
	})
}

// searchHit is one result row, shaped after Elasticsearch's hit objects.
type searchHit struct {
	ID     string         `json:"_id"`
	Score  float64        `json:"_score"`
	Fields map[string]any `json:"fields"`
}

// handleSearch runs an AND search over the inverted index.
//
// Parameters:
//
//	index   required; the name of the index to search
//	q       required; analyzed with the same tokenizer as indexing
//	limit   optional positive integer, default 20, capped at 100
//
// An index that does not exist (yet) is NOT an error: it simply has zero
// documents, so the response is 200 with total=0 and hits=[]. This mirrors
// what Elasticsearch does for empty indices and keeps the UI friendly when
// you search before loading any data.
//
// Scoring is TF-IDF — see the search package documentation.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	index, query := q.Get("index"), q.Get("q")

	if index == "" {
		writeError(w, http.StatusBadRequest, "missing required parameter: index")
		return
	}
	if query == "" {
		writeError(w, http.StatusBadRequest, "missing required parameter: q")
		return
	}

	limit := defaultSearchLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	hits, total := s.idx.Search(index, query, limit)

	// Merge scores with stored documents. Documents can vanish between the
	// search and the lookup under concurrent deletes; skip such hits rather
	// than failing the whole request.
	out := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		doc, ok := s.store.Get(index, h.DocID)
		if !ok {
			continue
		}
		out = append(out, searchHit{ID: h.DocID, Score: h.Score, Fields: doc.Fields})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"index": index,
		"query": query,
		"total": total,
		"hits":  out,
	})
}

// handleDBTables lists the linked database's tables and views.
//
//	GET /api/db/tables  →  {"tables": ["comments", "posts", ...]}
func (s *Server) handleDBTables(w http.ResponseWriter, r *http.Request) {
	tables, err := s.db.ListTables(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "listing tables: "+err.Error())
		return
	}
	if tables == nil {
		tables = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tables": tables})
}

// handleDBSearch searches rows of one table, matching only the `search`
// columns and returning only the `return` columns.
//
//	GET /api/db/search?table=posts&q=sunset&search=content&return=id,content&limit=5
//
// Parameters:
//
//	table   required
//	q       required; analyzed like document search (exact > prefix > substring)
//	search  required, CSV; which columns are examined for matches
//	return  optional, CSV; which columns appear in results (empty = all)
//	limit   optional positive integer, default 20, capped at 100
func (s *Server) handleDBSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	table, query := q.Get("table"), q.Get("q")
	searchCols := splitCSV(q.Get("search"))
	returnCols := splitCSV(q.Get("return"))

	switch {
	case table == "":
		writeError(w, http.StatusBadRequest, "missing required parameter: table")
		return
	case query == "":
		writeError(w, http.StatusBadRequest, "missing required parameter: q")
		return
	case len(searchCols) == 0:
		writeError(w, http.StatusBadRequest, "missing required parameter: search (comma-separated columns)")
		return
	}

	limit := defaultSearchLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	rows, total, err := s.db.SearchTable(r.Context(), gosearcher.TableSearch{
		Table:      table,
		Query:      query,
		SearchCols: searchCols,
		ReturnCols: returnCols,
		Limit:      limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database search failed: "+err.Error())
		return
	}
	if rows == nil {
		rows = []map[string]any{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"table": table,
		"query": query,
		"total": total,
		"hits":  rows,
	})
}

// splitCSV splits a query-string column list, trimming and dropping empties in
// both directions ("a, b,," → ["a", "b"]). The empty string yields nil.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// writeError writes a JSON error body in an Elasticsearch-like shape:
// {"error":{"type":...,"reason":...},"status":...}
func writeError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":   http.StatusText(status),
			"reason": reason,
		},
		"status": status,
	})
}
