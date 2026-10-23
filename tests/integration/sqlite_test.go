package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gosearch/pkg/api"
	"gosearch/pkg/gosearcher"
	"gosearch/pkg/search"
	"gosearch/pkg/storage"

	_ "modernc.org/sqlite"
)

func TestSQLiteTableSearchEndToEnd(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `CREATE TABLE products (
		id INTEGER PRIMARY KEY, name TEXT, description TEXT, secret TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id         int
		name, desc string
	}{
		{1, "Wireless Keyboard", "compact"},
		{2, "Mechanical Keyboard", "wireless in description"},
		{3, "Mouse", "pointer"},
	} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO products (id, name, description, secret) VALUES (?, ?, ?, ?)",
			row.id, row.name, row.desc, "private wireless key"); err != nil {
			t.Fatal(err)
		}
	}

	gs := gosearcher.NewGoSearcher()
	if gs.LinkDB(db) != gs {
		t.Fatal("LinkDB did not return the receiver")
	}
	t.Cleanup(func() { _ = gs.Close() })
	tables, err := gs.ListTables(ctx)
	if err != nil || len(tables) != 1 || tables[0] != "products" {
		t.Fatalf("ListTables = %v, %v; want [products]", tables, err)
	}

	ts := gosearcher.TableSearch{
		Table: "products", Query: "wireless key",
		SearchCols: []string{"name"}, ReturnCols: []string{"id", "name"}, Limit: 1,
	}
	rows, total, err := gs.SearchTable(ctx, ts)
	if err != nil || total != 1 || len(rows) != 1 || rows[0]["id"] != int64(1) {
		t.Fatalf("initial search: rows=%v total=%d err=%v", rows, total, err)
	}
	if len(rows[0]) != 2 {
		t.Errorf("returned columns = %v; want only id and name", rows[0])
	}

	if _, err := db.ExecContext(ctx, "UPDATE products SET name = ? WHERE id = 2", "Wireless Keyboard Pro"); err != nil {
		t.Fatal(err)
	}
	rows, total, err = gs.SearchTable(ctx, ts)
	if err != nil || total != 2 || len(rows) != 1 {
		t.Fatalf("search after update: rows=%v total=%d err=%v", rows, total, err)
	}

	handler := api.NewServer(storage.NewStore(), search.NewIndex()).WithDB(gs).Handler()
	searchHTTP := func() struct {
		Total int              `json:"total"`
		Hits  []map[string]any `json:"hits"`
	} {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet,
			"/api/db/search?table=products&q=wireless+key&search=name&return=id,name&limit=10", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("HTTP search: status=%d body=%s", response.Code, response.Body.String())
		}
		var result struct {
			Total int              `json:"total"`
			Hits  []map[string]any `json:"hits"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	result := searchHTTP()
	if result.Total != 2 || len(result.Hits) != 2 {
		t.Fatalf("HTTP search after update = %+v; want two hits", result)
	}
	for _, hit := range result.Hits {
		if len(hit) != 2 {
			t.Errorf("HTTP hit exposed extra columns: %v", hit)
		}
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM products WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	result = searchHTTP()
	if result.Total != 1 || len(result.Hits) != 1 || result.Hits[0]["id"] != float64(2) {
		t.Fatalf("HTTP search after delete = %+v; want only id 2", result)
	}
}
