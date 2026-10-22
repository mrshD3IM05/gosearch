package gosearcher_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gosearch/pkg/gosearcher"
)

// ---- in-memory fake database ------------------------------------------------

// fakeRows implements gosearcher.Rows over a slice of rows.
type fakeRows struct {
	cols []string
	rows [][]any
	pos  int
}

func (f *fakeRows) Columns() ([]string, error) { return f.cols, nil }
func (f *fakeRows) Next() bool                 { f.pos++; return f.pos <= len(f.rows) }
func (f *fakeRows) Scan(dest ...any) error {
	row := f.rows[f.pos-1]
	for i := range dest {
		switch d := dest[i].(type) {
		case *any:
			*d = row[i]
		case *string:
			*d = row[i].(string)
		default:
			return fmt.Errorf("fakeRows: unsupported dest type %T", dest[i])
		}
	}
	return nil
}
func (f *fakeRows) Err() error   { return nil }
func (f *fakeRows) Close() error { return nil }

// fakeDB implements Querier, returning a fixed result set.
type fakeDB struct {
	cols []string
	rows [][]any
	err  error

	lastQuery string
}

type versionedFakeDB struct {
	fakeDB
	version int64
	scans   int
}

func (f *versionedFakeDB) Version(context.Context) (int64, error) { return f.version, nil }

func (f *versionedFakeDB) QueryContext(ctx context.Context, query string, args ...any) (gosearcher.Rows, error) {
	f.scans++
	return f.fakeDB.QueryContext(ctx, query, args...)
}

func (f *fakeDB) QueryContext(_ context.Context, query string, _ ...any) (gosearcher.Rows, error) {
	f.lastQuery = query
	if f.err != nil {
		return nil, f.err
	}
	return &fakeRows{cols: f.cols, rows: f.rows}, nil
}

func newSearcher(t *testing.T, cols []string, rows [][]any) (*gosearcher.GoSearcher, *fakeDB) {
	t.Helper()
	db := &fakeDB{cols: cols, rows: rows}
	return gosearcher.NewGoSearcher().LinkQuerier(db), db
}

// ---- tests ------------------------------------------------------------------

// TestSearchTableReturnsOnlyReturnCols: results contain exactly the requested
// return columns and only rows whose search columns match.
func TestSearchTableReturnsOnlyReturnCols(t *testing.T) {
	gs, _ := newSearcher(t,
		[]string{"id", "name", "tags", "secret"},
		[][]any{
			{1, "wireless keyboard", []string{"keyboard", "wireless"}, "classified"},
			{2, "horse", nil, "classified"},
			{3, "mechanical keyboard", []string{"keyboard"}, "classified"},
		})

	out, total, err := gs.SearchTable(context.Background(), gosearcher.TableSearch{
		Table:      "products",
		Query:      "keyboard",
		SearchCols: []string{"name", "tags"},
		ReturnCols: []string{"id", "name"},
	})
	if err != nil {
		t.Fatalf("SearchTable: %v", err)
	}
	if total != 2 || len(out) != 2 {
		t.Fatalf("total=%d rows=%d, want 2 and 2", total, len(out))
	}
	if _, has := out[0]["secret"]; has {
		t.Error("unrequested column 'secret' leaked into results")
	}
	for _, row := range out {
		if len(row) != 2 {
			t.Errorf("row has %d columns, want exactly [id name]", len(row))
		}
	}
	// Equal tf/idf → tie broken by row order (id 1 before id 3).
	if out[0]["id"] != 1 || out[1]["id"] != 3 {
		t.Errorf("row order = %v, %v; want 1 then 3", out[0]["id"], out[1]["id"])
	}
}

// TestSearchTableIgnoresUnselectedColumns: a column not listed in SearchCols
// never influences matching.
func TestSearchTableIgnoresUnselectedColumns(t *testing.T) {
	gs, _ := newSearcher(t,
		[]string{"name", "memo"},
		[][]any{
			{"plain widget", "contains the word secret here"},
		})

	out, total, err := gs.SearchTable(context.Background(), gosearcher.TableSearch{
		Table:      "parts",
		Query:      "secret",
		SearchCols: []string{"name"},
		ReturnCols: []string{"name", "memo"},
	})
	if err != nil {
		t.Fatalf("SearchTable: %v", err)
	}
	if total != 0 || len(out) != 0 {
		t.Errorf("total=%d rows=%d, want 0 (memo must not be searchable)", total, len(out))
	}
}

// TestSearchTableReturnColumns: with ReturnCols empty, every column returns;
// values survive the Scan round trip.
func TestSearchTableReturnsAllColumnsWhenUnspecified(t *testing.T) {
	gs, _ := newSearcher(t,
		[]string{"id", "name", "price"},
		[][]any{{1, "blue fox", 9.99}})

	out, _, err := gs.SearchTable(context.Background(), gosearcher.TableSearch{
		Table:      "parts",
		Query:      "fox",
		SearchCols: []string{"name"},
	})
	if err != nil {
		t.Fatalf("SearchTable: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("rows = %d, want 1", len(out))
	}
	if out[0]["id"] != 1 || out[0]["name"] != "blue fox" || out[0]["price"] != 9.99 {
		t.Errorf("row = %v", out[0])
	}
}

// TestSearchTableMatchTiersCarryOver: substring matching behaves inside a
// table just like on plain documents — exact outranks substring.
func TestSearchTableMatchTiersCarryOver(t *testing.T) {
	gs, _ := newSearcher(t,
		[]string{"word"},
		[][]any{
			{"key"},
			{"cockey"},
			{"unrelated"},
		})

	out, total, err := gs.SearchTable(context.Background(), gosearcher.TableSearch{
		Table:      "terms",
		Query:      "key",
		SearchCols: []string{"word"},
	})
	if err != nil {
		t.Fatalf("SearchTable: %v", err)
	}
	if total != 2 || len(out) != 2 {
		t.Fatalf("total=%d rows=%d, want 2", total, len(out))
	}
	if out[0]["word"] != "key" || out[1]["word"] != "cockey" {
		t.Errorf("order = %v, %v; want key then cockey", out[0]["word"], out[1]["word"])
	}
}

// TestSearchTableLimitAndTotal: Limit caps returned rows without hiding the
// true match count.
func TestSearchTableLimitAndTotal(t *testing.T) {
	var rows [][]any
	for i := 0; i < 5; i++ {
		rows = append(rows, []any{i, "common word"})
	}
	gs, db := newSearcher(t, []string{"id", "text"}, rows)

	out, total, err := gs.SearchTable(context.Background(), gosearcher.TableSearch{
		Table:      "logs",
		Query:      "common",
		SearchCols: []string{"text"},
		ReturnCols: []string{"id"},
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("SearchTable: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(out) != 2 {
		t.Errorf("rows = %d, want 2", len(out))
	}
	if db.lastQuery != "SELECT * FROM logs" {
		t.Errorf("lastQuery = %q", db.lastQuery)
	}
}

func TestVersionedTableReusesAndRefreshesIndex(t *testing.T) {
	db := &versionedFakeDB{
		fakeDB: fakeDB{
			cols: []string{"id", "name", "description"},
			rows: [][]any{{1, "wireless keyboard", "first"}, {2, "mouse", "second"}},
		},
		version: 1,
	}
	gs := gosearcher.NewGoSearcher().LinkQuerier(db)
	ts := gosearcher.TableSearch{Table: "products", Query: "keyboard", SearchCols: []string{"name"}, ReturnCols: []string{"id"}}
	if rows, total, err := gs.SearchTable(context.Background(), ts); err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("first search: rows=%v total=%d err=%v", rows, total, err)
	}
	if db.scans != 1 {
		t.Fatalf("first search scanned %d times, want 1", db.scans)
	}
	ts.Query = "mouse"
	ts.ReturnCols = []string{"name"}
	if rows, total, err := gs.SearchTable(context.Background(), ts); err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("warm search: rows=%v total=%d err=%v", rows, total, err)
	}
	if db.scans != 1 {
		t.Errorf("warm search scanned again: %d scans", db.scans)
	}

	db.rows[1][1] = "trackpad"
	db.version++
	if rows, total, err := gs.SearchTable(context.Background(), ts); err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("search after update: rows=%v total=%d err=%v", rows, total, err)
	}
	if db.scans != 2 {
		t.Errorf("changed table scanned %d times, want 2", db.scans)
	}

	ts.SearchCols = []string{"description"}
	ts.Query = "second"
	if rows, total, err := gs.SearchTable(context.Background(), ts); err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("different columns: rows=%v total=%d err=%v", rows, total, err)
	}
	if db.scans != 3 {
		t.Errorf("different search columns scanned %d times, want 3", db.scans)
	}
}

// TestSearchTableErrors: missing LinkDB, empty table, no search columns, and
// unknown columns each fail with a clear message.
func TestSearchTableErrors(t *testing.T) {
	mk := func() *gosearcher.GoSearcher {
		g, _ := newSearcher(t, []string{"id", "name"}, [][]any{{1, "x"}})
		return g
	}

	notLinked := gosearcher.NewGoSearcher()
	if _, _, err := notLinked.SearchTable(context.Background(), gosearcher.TableSearch{
		Table: "t", Query: "x", SearchCols: []string{"name"},
	}); err == nil {
		t.Error("SearchTable before LinkDB: want error")
	}

	for name, ts := range map[string]gosearcher.TableSearch{
		"empty table":   {Table: "", Query: "x", SearchCols: []string{"name"}},
		"no searchcol":  {Table: "t", Query: "x"},
		"bad searchcol": {Table: "t", Query: "x", SearchCols: []string{"nope"}},
		"bad returncol": {Table: "t", Query: "x", SearchCols: []string{"name"}, ReturnCols: []string{"nope"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := mk().SearchTable(context.Background(), ts); err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

// TestLinkQuerierChains: LinkQuerier attaches any Querier (custom backends,
// tests) and chains.
func TestLinkQuerierChains(t *testing.T) {
	gs := gosearcher.NewGoSearcher()
	linked := gs.LinkQuerier(&fakeDB{cols: []string{"id", "name"}, rows: [][]any{{1, "x"}}})
	if linked != gs {
		t.Fatal("LinkQuerier did not return the receiver")
	}
	rows, total, err := linked.SearchTable(context.Background(), gosearcher.TableSearch{
		Table: "items", Query: "x", SearchCols: []string{"name"},
	})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("linked search: rows=%v total=%d err=%v", rows, total, err)
	}
}

// TestListTables: names come back in query order from the sqlite_master query.
func TestListTables(t *testing.T) {
	db := &fakeDB{
		cols: []string{"name"},
		rows: [][]any{{"comments"}, {"posts"}, {"users"}},
	}
	gs := gosearcher.NewGoSearcher().LinkQuerier(db)

	names, err := gs.ListTables(context.Background())
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(names) != 3 || names[0] != "comments" || names[2] != "users" {
		t.Errorf("names = %v", names)
	}
	if !strings.Contains(db.lastQuery, "sqlite_master") {
		t.Errorf("unexpected query: %q", db.lastQuery)
	}
}

// TestListTablesRequiresLink: no LinkDB, no listing.
func TestListTablesRequiresLink(t *testing.T) {
	if _, err := gosearcher.NewGoSearcher().ListTables(context.Background()); err == nil {
		t.Error("ListTables before LinkDB: want error")
	}
}
