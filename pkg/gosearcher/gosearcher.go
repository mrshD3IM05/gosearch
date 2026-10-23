// Package gosearcher wraps the search engine around a database: a SQL table
// becomes a searchable document set. The caller picks which 1+ columns are
// analyzed for matching and which columns come back in the results.
//
// It is the "talk to a real database" surface from the project README. The
// actual search always runs on the in-memory inverted index (pkg/search); the
// database is checked for committed changes on every call. File-backed SQLite
// tables reuse a cached snapshot while unchanged, and searches still reflect
// committed writes on the next request.
package gosearcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// Querier is the minimal database surface the engine needs. It is satisfied by
// sqlQuerier (which adapts *sql.DB), and by in-memory fakes in unit tests.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
}

// Rows mirrors the subset of *sql.Rows used here.
type Rows interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// sqlQuerier adapts *sql.DB to Querier. Interface satisfaction requires method
// signatures to match exactly, and database/sql returns the concrete
// *sql.Rows type, so a thin adapter is needed to surface it as Rows.
type sqlQuerier struct {
	db                 *sql.DB
	monitorMu          sync.Mutex
	monitorDB          *sql.DB
	monitorConn        *sql.Conn
	versionUnavailable bool
}

func (q *sqlQuerier) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// GoSearcher holds the database reference. Create one with NewGoSearcher,
// attach a database with LinkDB, then query tables with SearchTable.
type GoSearcher struct {
	mu         sync.Mutex
	db         Querier
	cache      map[string]*tableSnapshot
	cacheOrder []string
}

// NewGoSearcher returns a GoSearcher ready for LinkDB.
func NewGoSearcher() *GoSearcher {
	return &GoSearcher{}
}

// LinkDB attaches a database to the searcher and returns the receiver so calls
// can be chained:
//
//	gs := gosearcher.NewGoSearcher().LinkDB(db)
func (g *GoSearcher) LinkDB(db *sql.DB) *GoSearcher {
	return g.linkDB(&sqlQuerier{db: db})
}

// LinkQuerier attaches any Querier. It exists for custom backends and tests
// that do not want to build a *sql.DB.
func (g *GoSearcher) LinkQuerier(db Querier) *GoSearcher {
	return g.linkDB(db)
}

// linkDB attaches any Querier (used by tests and adapters).
func (g *GoSearcher) linkDB(db Querier) *GoSearcher {
	g.mu.Lock()
	defer g.mu.Unlock()
	if old, ok := g.db.(*sqlQuerier); ok {
		_ = old.closeMonitor()
	}
	g.db = db
	g.cache = nil
	g.cacheOrder = nil
	return g
}

// Close releases the SQLite change-monitor connection opened by LinkDB.
// It does not close the caller's *sql.DB.
func (g *GoSearcher) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cache = nil
	g.cacheOrder = nil
	if db, ok := g.db.(*sqlQuerier); ok {
		return db.closeMonitor()
	}
	return nil
}

// ListTables returns the names of the database's tables and views. The query
// is SQLite-flavored (sqlite_master) and excludes internal bookkeeping
// (sqlite_% and schema_% names); it powers table pickers in UIs.
func (g *GoSearcher) ListTables(ctx context.Context) ([]string, error) {
	if g.db == nil {
		return nil, errors.New("gosearcher: call LinkDB before ListTables")
	}
	const query = `SELECT name FROM sqlite_master
	               WHERE type IN ('table','view')
	                 AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'schema_%'
	               ORDER BY name`
	rows, err := g.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("gosearcher: listing tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("gosearcher: scanning table name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// TableSearch describes one search call: which table, the query, the columns
// to match against, and the columns to return.
type TableSearch struct {
	// Query is analyzed with the same tokenizer used to index SearchCols.
	Query string
	// Table is the SQL table (or view) to search.
	Table string
	// SearchCols are the columns analyzed and matched; at least one is
	// required. Anything outside this list never affects matching.
	SearchCols []string
	// ReturnCols are the columns included in each result map. Empty means
	// every column is returned.
	ReturnCols []string
	// Limit caps the number of returned rows; 0 or negative means no limit.
	Limit int
}

// SearchTable answers ts.Query against one table. For file-backed SQLite, it
// reuses a snapshot until the database changes. Other Querier implementations
// are read on every call unless they provide a stable change version.
//
// Result maps contain exactly the requested columns (ReturnCols, or all
// columns when it is empty), ordered by descending score (ties broken by row
// order). Total counts every matching row before Limit is applied. Scoring and
// match semantics (exact > prefix > substring, AND) are the inverted index's.
func (g *GoSearcher) SearchTable(ctx context.Context, ts TableSearch) ([]map[string]any, int, error) {
	if g.db == nil {
		return nil, 0, errors.New("gosearcher: call LinkDB before SearchTable")
	}
	if ts.Table == "" {
		return nil, 0, errors.New("gosearcher: table must not be empty")
	}
	if len(ts.SearchCols) == 0 {
		return nil, 0, errors.New("gosearcher: at least one search column required")
	}

	snapshot, err := g.getSnapshot(ctx, ts)
	if err != nil {
		return nil, 0, err
	}
	if missing := missingCols(snapshot.cols, ts.ReturnCols); len(missing) > 0 {
		return nil, 0, fmt.Errorf("gosearcher: table %q has no return column(s) %v", ts.Table, missing)
	}

	returnCols := ts.ReturnCols
	if len(returnCols) == 0 {
		returnCols = snapshot.cols
	}
	hits, total := snapshot.idx.Search(ts.Table, ts.Query, ts.Limit)
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		pos, err := strconv.Atoi(h.DocID)
		if err != nil {
			continue
		}
		out = append(out, project(snapshot.rows[pos], snapshot.cols, returnCols))
	}
	return out, total, nil
}

// project builds a result map with exactly the requested columns, in the
// requested order.
func project(row []any, allCols, want []string) map[string]any {
	out := map[string]any{}
	for _, c := range want {
		out[c] = row[colIndex(allCols, c)]
	}
	return out
}

// colIndex returns the position of name in cols, or -1.
func colIndex(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

// missingCols returns the entries of want that are absent from cols.
func missingCols(cols, want []string) []string {
	var out []string
	for _, c := range want {
		if colIndex(cols, c) < 0 {
			out = append(out, c)
		}
	}
	return out
}
