package gosearcher

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gosearch/pkg/search"
)

// A version must change after every committed change visible to QueryContext.
// Querier implementations without this contract continue to read live rows.
type tableVersioner interface {
	Version(context.Context) (int64, error)
}

type tableSnapshot struct {
	version int64
	cols    []string
	rows    [][]any
	idx     *search.Index
}

const maxTableSnapshots = 8

func (g *GoSearcher) touchSnapshot(key string) {
	for i, existing := range g.cacheOrder {
		if existing == key {
			g.cacheOrder = append(g.cacheOrder[:i], g.cacheOrder[i+1:]...)
			break
		}
	}
	g.cacheOrder = append(g.cacheOrder, key)
}

func (g *GoSearcher) getSnapshot(ctx context.Context, ts TableSearch) (*tableSnapshot, error) {
	versioner, ok := g.db.(tableVersioner)
	if !ok {
		return g.loadSnapshot(ctx, ts)
	}

	// Serialize version checks and cold builds. Searches use immutable snapshots
	// after this lock is released, so warm queries can score concurrently.
	g.mu.Lock()
	defer g.mu.Unlock()

	version, err := versioner.Version(ctx)
	if errors.Is(err, errVersionUnavailable) {
		return g.loadSnapshot(ctx, ts)
	}
	if err != nil {
		return nil, err
	}
	key := ts.Table + "\x00" + strings.Join(ts.SearchCols, "\x00")
	if cached := g.cache[key]; cached != nil && cached.version == version {
		g.touchSnapshot(key)
		return cached, nil
	}

	var snapshot *tableSnapshot
	for attempt := 0; attempt < 2; attempt++ {
		snapshot, err = g.loadSnapshot(ctx, ts)
		if err != nil {
			return nil, err
		}
		after, err := versioner.Version(ctx)
		if errors.Is(err, errVersionUnavailable) {
			return snapshot, nil
		}
		if err != nil {
			return nil, err
		}
		if after == version {
			if g.cache == nil {
				g.cache = make(map[string]*tableSnapshot)
			}
			if _, exists := g.cache[key]; !exists && len(g.cache) >= maxTableSnapshots {
				delete(g.cache, g.cacheOrder[0])
				g.cacheOrder = g.cacheOrder[1:]
			}
			snapshot.version = version
			g.cache[key] = snapshot
			g.touchSnapshot(key)
			return snapshot, nil
		}
		version = after
	}
	// A writer kept changing the table. Return the latest scan without
	// caching it; the next request will scan again.
	return snapshot, nil
}

func (g *GoSearcher) loadSnapshot(ctx context.Context, ts TableSearch) (*tableSnapshot, error) {
	// The table name is an SQL identifier supplied by the caller. Identifier
	// validation is separate from the snapshot cache.
	rows, err := g.db.QueryContext(ctx, "SELECT * FROM "+ts.Table)
	if err != nil {
		return nil, fmt.Errorf("gosearcher: reading table %q: %w", ts.Table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("gosearcher: reading columns of %q: %w", ts.Table, err)
	}
	if missing := missingCols(cols, ts.SearchCols); len(missing) > 0 {
		return nil, fmt.Errorf("gosearcher: table %q has no search column(s) %v", ts.Table, missing)
	}

	snapshot := &tableSnapshot{cols: cols, idx: search.NewIndex()}
	for rows.Next() {
		row := make([]any, len(cols))
		dests := make([]any, len(cols))
		for i := range dests {
			dests[i] = &row[i]
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, fmt.Errorf("gosearcher: scanning %q: %w", ts.Table, err)
		}
		fields := make(map[string]any, len(ts.SearchCols))
		for _, col := range ts.SearchCols {
			fields[col] = row[colIndex(cols, col)]
		}
		snapshot.idx.Add(ts.Table, strconv.Itoa(len(snapshot.rows)), fields)
		snapshot.rows = append(snapshot.rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gosearcher: reading %q: %w", ts.Table, err)
	}
	return snapshot, nil
}
