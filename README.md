# gosearch

A small, dependency-free search engine written in **Go**, built from scratch for teaching how search engines work.

Instead of wrapping Lucene, Bleve, or Elasticsearch, every component — tokenizer, inverted index, posting lists, scoring, HTTP API — is implemented directly. The standard library is the only dependency.

## Features

- **Document API** — index, retrieve, and delete JSON documents into named indices
- **Inverted index** — term → posting lists with *document IDs and token positions*
- **Term dictionary** — sorted vocabulary enabling **prefix matching** (`key` finds `keyboard`)
- **Unicode-aware tokenizer** — lowercase, punctuation-aware, works on non-ASCII text (French, Cyrillic, CJK)
- **TF-IDF ranking** — documents scored by term frequency and inverse document frequency
- **AND queries** — all query tokens must match; expansion happens per token
- **Standalone live-test console** — the frontend is a separate unit in `/web`, served on its own port, so you can edit and test against a running engine with no rebuild
- **Sample corpus** — 1,011 course titles (including deliberate typo variants for fuzzy-search work) loadable in one click
- **Concurrency-safe** — clean under `go test -race`

## Architecture

```
HTTP API (pkg/api)
   │
   ├── PUT /index/{index}/{id}   →  pkg/storage  (document store)
   └── GET /api/search           →  pkg/search   (query engine)
                                        │
                              pkg/search (inverted index)
                              ├── term dictionary  (sorted terms → prefix lookup)
                              ├── posting lists    (docID + positions per term)
                              └── forward index    (doc → its terms, for updates/deletes)
                                        │
                              pkg/analyze (StandardTokenizer)
                                        │
                       ┌─────────────┴─────────────┐
                       │  CORS (browser): any origin may call the API
                       └─────────────┬─────────────┘
                                        │
                              web/ (live-test console)
                              served by cmd/webtest on :5000
```

```
package layout
├── cmd/gosearch              backend server: POST /samples, doc + search APIs
├── cmd/webtest               static server for the live-test console (terminal 2)
├── pkg/analyze               tokenizer (Phase 2)
├── pkg/search                inverted index, prefix expansion, TF-IDF scoring (Phase 3+)
├── pkg/storage               in-memory document store (Phase 1)
├── pkg/samples               1,011-title embedded search corpus
├── pkg/api                   REST handlers + CORS layer
└── web/                      standalone single-page console (no build step, no CDN)
```

The backend is a reusable library (`pkg/...`, importable from any Go module), while the console is deliberately *not* compiled in: it is a live-test unit you run next to a real backend and modify freely.

## How the search works

Both indexing and querying run the **same tokenizer**, so a query term and an indexed term can never disagree about spelling.

1. **Tokenize** — text is normalized with `StandardTokenizer`: Unicode letters/digits form tokens, everything else splits; terms are lowercased. Byte offsets are kept for highlighting later.
2. **Index** — each token updates its posting list with the document ID and the token position. Every document's terms are recorded in a forward index so updates and deletes can expire the right postings.
3. **Query** — query tokens are tokenized identically, then each token is *expanded* against the term dictionary to exact matches and longer terms that start with it. A document must match **all** tokens (AND).
4. **Score** — classic TF-IDF:

   ```
   score(doc, query) = Σ over query tokens  max over matched terms ( tf(term, doc) · ln(1 + N / df(term)) )
   ```

   Exact matches score at full weight; prefix-only matches are scaled by a penalty (`0.4`) so exact hits rank above fuzzy ones.

## Getting started

The engine and the console are separate processes; live testing is the default workflow.

```sh
# terminal 1 — the backend
go run ./cmd/gosearch                # → gosearch listening on :9200

# terminal 2 — the live-test console
go run ./cmd/webtest                 # → webtest serving web/ on http://localhost:5000
```

Open <http://localhost:5000> and search. The console talks to the backend through
the `API_BASE` constant at the top of `web/index.html` (default
`http://localhost:9200`); the backend answers CORS preflights, so you can also
open `web/index.html` directly from disk. Edit any HTML/CSS/JS in `web/` and
reload — no build step, no embed to regenerate.

Once loaded, try:

- `keyboard` — exact and prefix matches, ranked
- `elasticsearch ranking` — AND semantics
- `concurr` — prefix expansion

## REST API

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | liveness probe |
| `GET` | `/index` | list index names |
| `PUT` | `/index/{index}/{id}` | index (create/replace) a JSON document |
| `GET` | `/index/{index}/{id}` | retrieve a document |
| `DELETE` | `/index/{index}/{id}` | delete a document |
| `GET` | `/api/search?index={index}&q={query}&limit={n}` | AND search over the inverted index |
| `POST` | `/samples?index={index}` | bulk-index the embedded course corpus (default index `courses`) |

Every response includes CORS headers and OPTIONS preflights, so the standalone
console (`/web`, served by `cmd/webtest`) can call the API cross-origin.

`/api/search` parameters: `index` (required), `q` (required), `limit` (optional, default `20`, capped at `100`). Searching an index with no documents returns `200` with zero hits rather than `404`.

```sh
# Index a document (bare object form)
curl -X PUT localhost:9200/index/products/1 \
     -H 'Content-Type: application/json' \
     -d '{"name":"Mechanical Keyboard","price":89}'

# Retrieve it
curl localhost:9200/index/products/1

# Search it (prefix: "key" also matches "keyboard")
curl 'localhost:9200/api/search?index=products&q=wireless+keyboard'

# Load 1,011 course titles into the "courses" index
curl -X POST 'localhost:9200/samples?index=courses'

# Delete it
curl -X DELETE localhost:9200/index/products/1
```

Example response:

```json
{
  "index": "courses",
  "query": "elasticsearch ranking",
  "total": 1,
  "hits": [
    {
      "_id": "324",
      "_score": 3.097,
      "fields": { "title": "Elasticsearch Ranking" }
    }
  ]
}
```

## Development

```sh
make build         # build bin/gosearch
make run           # run the backend on :9200
make web           # run the live-test console on :5000
make test          # go test ./...
make test-race     # go test -race ./...
make vet           # go vet ./...
make fmt           # gofmt -w
make check         # format check + vet + race tests (matches CI)
```

All checks run in CI on every push/PR (`.github/workflows/ci.yml`).

## Roadmap

Implemented:
- [x] Document storage (in-memory) + HTTP CRUD
- [x] Unicode-aware tokenizer with byte offsets and positions
- [x] Inverted index (posting lists + forward index + term dictionary)
- [x] AND search with exact + prefix matching
- [x] TF-IDF scoring
- [x] Standalone live-test console (`/web`) + sample corpora

Planned (educational order):
- [ ] Phrase queries (positions are already stored)
- [ ] Field-aware queries (`title:search`)
- [ ] Booleans: `OR` / `NOT`
- [ ] Stop-word removal and analyzers
- [ ] BM25 ranking (avg document length is already maintained)
- [ ] Fuzzy / typo-tolerant matching (typo variants are in the sample corpus)
- [ ] Segments, disk persistence, and segment merging
- [ ] Sharding and distributed result merging

Planned — talk to a real database (SQL round-trip):

Today documents must be pushed into gosearch one by one with `PUT`. The goal
below is to cut the database out of that loop entirely: gosearch reads rows
straight from a database, and the user decides which columns are searchable
and which columns come back.

- [ ] Connect directly to a database (SQLite first, then Postgres/MySQL) and index its tables/rows automatically, with no manual `PUT` export step
- [ ] Let the user pick **1+ columns to search on** — e.g. `GET /api/search?index=products&search_columns=name,description&q=...` (only listed columns are analyzed and matched)
- [ ] Let the user pick **1+ columns to return** — e.g. `return_columns=id,name,price` (responses carry exactly those columns, not the whole document)
- [ ] Keep score columns usable alongside row data (return `_score` even when arbitrary columns are selected)
- [ ] Auto-map database schema to indices: table → index, row → document, column → field
- [ ] Respect primary keys so updates in the database overwrite (not duplicate) documents
- [ ] Live sync: re-index on database writes (polling, then change data capture) so searches always match the current rows
- [ ] Respect the user's column list when *storing* too: unselected columns are not memory-costly and never leak into results

## License

[MIT](LICENSE)