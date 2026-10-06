package gosearcher

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// ---- in-memory fake database ------------------------------------------------

// fakeRows implements Rows over a slice of rows.
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

func (f *fakeDB) QueryContext(_ context.Context, query string, _ ...any) (Rows, error) {
	f.lastQuery = query
	if f.err != nil {
		return nil, f.err
	}
	return &fakeRows{cols: f.cols, rows: f.rows}, nil
}

func newSearcher(t *testing.T, cols []string, rows [][]any) (*GoSearcher, *fakeDB) {
	t.Helper()
	db := &fakeDB{cols: cols, rows: rows}
	return NewGoSearcher().linkDB(db), db
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

	out, total, err := gs.SearchTable(context.Background(), TableSearch{
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

	out, total, err := gs.SearchTable(context.Background(), TableSearch{
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

	out, _, err := gs.SearchTable(context.Background(), TableSearch{
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

	out, total, err := gs.SearchTable(context.Background(), TableSearch{
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

	out, total, err := gs.SearchTable(context.Background(), TableSearch{
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

// TestSearchTableErrors: missing LinkDB, empty table, no search columns, and
// unknown columns each fail with a clear message.
func TestSearchTableErrors(t *testing.T) {
	mk := func() *GoSearcher {
		g, _ := newSearcher(t, []string{"id", "name"}, [][]any{{1, "x"}})
		return g
	}

	notLinked := NewGoSearcher()
	if _, _, err := notLinked.SearchTable(context.Background(), TableSearch{
		Table: "t", Query: "x", SearchCols: []string{"name"},
	}); err == nil {
		t.Error("SearchTable before LinkDB: want error")
	}

	for name, ts := range map[string]TableSearch{
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

// TestLinkDBChains: LinkDB returns the receiver for one-line construction.
func TestLinkDBChains(t *testing.T) {
	db := &fakeDB{cols: []string{"id", "name"}, rows: [][]any{{1, "chain test"}}}
	gs := NewGoSearcher().linkDB(db)
	if gs.db == nil {
		t.Fatal("linkDB did not attach the database")
	}
}

// TestLinkQuerierChains: LinkQuerier attaches any Querier (custom backends,
// tests) and chains.
func TestLinkQuerierChains(t *testing.T) {
	gs := NewGoSearcher().LinkQuerier(&fakeDB{cols: []string{"id", "name"}, rows: [][]any{{1, "x"}}})
	if gs.db == nil {
		t.Fatal("LinkQuerier did not attach the database")
	}
}

// TestListTables: names come back in query order from the sqlite_master query.
func TestListTables(t *testing.T) {
	db := &fakeDB{
		cols: []string{"name"},
		rows: [][]any{{"comments"}, {"posts"}, {"users"}},
	}
	gs := NewGoSearcher().linkDB(db)

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
	if _, err := NewGoSearcher().ListTables(context.Background()); err == nil {
		t.Error("ListTables before LinkDB: want error")
	}
}
