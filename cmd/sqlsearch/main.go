// Command sqlsearch links a SQLite database to the search engine and runs
// column searches over its tables. It exercises GoSearcher against a real
// database file (like sn.db) directly, with no HTTP layer in between.
//
// Usage:
//
//	sqlsearch -db sn.db -tables
//
//	sqlsearch -db sn.db -table posts -query "sunset rooftop" \
//	          -search content -return id,content,created_at -limit 20
//
// Only the columns named in -search are analyzed; only the columns named in
// -return appear in the results (omitting -return returns every column).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)

	"gosearch/pkg/gosearcher"
)

func main() {
	dbPath := flag.String("db", "sn.db", "path to the SQLite database")
	tables := flag.Bool("tables", false, "list tables/views and exit")
	table := flag.String("table", "", "table to search")
	query := flag.String("query", "", "search query")
	searchCols := flag.String("search", "", "comma-separated 1+ columns to search on (required)")
	returnCols := flag.String("return", "", "comma-separated columns to return (default: all)")
	limit := flag.Int("limit", 20, "maximum rows returned (0 = all)")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("sql.Open(%q): %v", *dbPath, err)
	}
	defer db.Close()

	if *tables {
		listTables(db)
		return
	}
	if *table == "" {
		log.Fatal("-table is required (or use -tables to list candidates)")
	}
	sc := splitCSV(*searchCols)
	if len(sc) == 0 {
		log.Fatal("-search requires at least one column to search on")
	}

	gs := gosearcher.NewGoSearcher().LinkDB(db)
	results, total, err := gs.SearchTable(context.Background(), gosearcher.TableSearch{
		Table:      *table,
		Query:      *query,
		SearchCols: sc,
		ReturnCols: splitCSV(*returnCols),
		Limit:      *limit,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%d match(es) in %q (showing %d)\n", total, *table, len(results))
	if len(results) == 0 {
		return
	}
	cols := make([]string, 0, len(results[0]))
	for c := range results[0] {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	for i, row := range results {
		fmt.Printf("[%d] ", i+1)
		for j, c := range cols {
			if j > 0 {
				fmt.Print(" | ")
			}
			fmt.Printf("%s=%v", c, row[c])
		}
		fmt.Println()
	}
}

// listTables prints the searchable tables/views in the database.
func listTables(db *sql.DB) {
	rows, err := db.Query(
		`SELECT name, type FROM sqlite_master
		 WHERE type IN ('table','view')
		   AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'schema_%'
		 ORDER BY name`)
	if err != nil {
		log.Fatalf("listing tables: %v", err)
	}
	defer rows.Close()
	fmt.Println("tables/views:")
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %-18s (%s)\n", name, kind)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
}

// splitCSV splits and trims a comma-separated flag value into non-empty parts.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
