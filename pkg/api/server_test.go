package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gosearch/pkg/search"
	"gosearch/pkg/storage"
)

// newTestServer returns a handler plus fresh store/index for inspection.
func newTestServer(t *testing.T) (http.Handler, *storage.Store) {
	t.Helper()
	store := storage.NewStore()
	return NewServer(store, search.NewIndex()).Handler(), store
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	var payload map[string]any
	raw, _ := io.ReadAll(res.Body)
	if strings.Contains(res.Header.Get("Content-Type"), "json") && len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("%s %s: response is not JSON: %q", method, path, raw)
		}
	}
	return res, payload
}

func TestPutGetDeleteLifecycle(t *testing.T) {
	h, _ := newTestServer(t)

	// PUT a bare fields object.
	res, body := doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"widget","price":9.99}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %v", res.StatusCode, body)
	}
	if body["result"] != "created_or_replaced" {
		t.Errorf("result = %v", body["result"])
	}

	// GET it back.
	res, doc := doJSON(t, h, http.MethodGet, "/index/products/1", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", res.StatusCode)
	}
	if doc["id"] != "1" {
		t.Errorf("id = %v, want 1", doc["id"])
	}
	fields, ok := doc["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields missing or wrong type: %v", doc["fields"])
	}
	if fields["name"] != "widget" {
		t.Errorf("name = %v, want widget", fields["name"])
	}
	// 9.99 survives the JSON round trip as a number, not a string.
	if fields["price"] != 9.99 {
		t.Errorf("price = %v, want 9.99", fields["price"])
	}

	// DELETE it.
	res, body = doJSON(t, h, http.MethodDelete, "/index/products/1", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d", res.StatusCode)
	}
	if body["result"] != "deleted" {
		t.Errorf("result = %v", body["result"])
	}

	// Now it is gone.
	res, _ = doJSON(t, h, http.MethodGet, "/index/products/1", "")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete status = %d, want 404", res.StatusCode)
	}
}

func TestPutFullDocumentForm(t *testing.T) {
	h, store := newTestServer(t)

	res, _ := doJSON(t, h, http.MethodPut, "/index/products/2",
		`{"id":"ignored","fields":{"tags":["a","b"],"stock":3}}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", res.StatusCode)
	}

	doc, ok := store.Get("products", "2")
	if !ok {
		t.Fatal("document not stored")
	}
	if doc.ID != "2" {
		t.Errorf("path id must win: ID = %q", doc.ID)
	}
	tags, ok := doc.Fields["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" {
		t.Errorf("tags = %v, want [a b]", doc.Fields["tags"])
	}
	// json.Number keeps integer-ness for later phases (sorting, range queries).
	if _, ok := doc.Fields["stock"].(json.Number); !ok {
		t.Errorf("stock type = %T, want json.Number", doc.Fields["stock"])
	}
}

func TestGetMissingDocumentReturns404(t *testing.T) {
	h, _ := newTestServer(t)

	res, body := doJSON(t, h, http.MethodGet, "/index/products/nope", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
	if body["status"] != float64(404) {
		t.Errorf("status field = %v", body["status"])
	}
	if _, ok := body["error"].(map[string]any); !ok {
		t.Errorf("error field = %v, want object", body["error"])
	}
}

func TestDeleteMissingDocumentReturns404(t *testing.T) {
	h, _ := newTestServer(t)

	res, _ := doJSON(t, h, http.MethodDelete, "/index/products/nope", "")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestPutRejectsInvalidBodies(t *testing.T) {
	h, _ := newTestServer(t)

	cases := []struct {
		name string
		body string
	}{
		{"not json", `hello world`},
		{"json array", `[1,2,3]`},
		{"json scalar", `"just a string"`},
		{"trailing garbage", `{"a":1} {"b":2}`},
		{"fields not object", `{"fields":"nope"}`},
		{"empty body", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := doJSON(t, h, http.MethodPut, "/index/products/1", tc.body)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", res.StatusCode)
			}
		})
	}
}

func TestIndicesAreIsolatedOverHTTP(t *testing.T) {
	h, _ := newTestServer(t)

	doJSON(t, h, http.MethodPut, "/index/products/1", `{"who":"products"}`)
	doJSON(t, h, http.MethodPut, "/index/users/1", `{"who":"users"}`)

	_, doc := doJSON(t, h, http.MethodGet, "/index/users/1", "")
	fields := doc["fields"].(map[string]any)
	if fields["who"] != "users" {
		t.Errorf("users/1 = %v, want users", fields["who"])
	}
}

func TestListIndices(t *testing.T) {
	h, _ := newTestServer(t)

	res, body := doJSON(t, h, http.MethodGet, "/index", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if indices, ok := body["indices"].([]any); !ok || len(indices) != 0 {
		t.Errorf("indices = %v, want empty on fresh server", body["indices"])
	}

	doJSON(t, h, http.MethodPut, "/index/products/1", `{"a":1}`)
	_, body = doJSON(t, h, http.MethodGet, "/index", "")
	indices, ok := body["indices"].([]any)
	if !ok || len(indices) != 1 || indices[0] != "products" {
		t.Errorf("indices = %v, want [products]", body["indices"])
	}
}

func TestHealthz(t *testing.T) {
	h, _ := newTestServer(t)

	res, _ := doJSON(t, h, http.MethodGet, "/healthz", "")
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

// TestLoadSamplesEndpoint indexes the embedded course corpus and makes it
// searchable; reloading is idempotent.
func TestLoadSamplesEndpoint(t *testing.T) {
	h, store := newTestServer(t)

	res, body := doJSON(t, h, http.MethodPost, "/samples?index=titles", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %v", res.StatusCode, body)
	}
	loaded, ok := body["loaded"].(float64)
	if !ok || loaded < 500 {
		t.Fatalf("loaded = %v, want at least 500 course titles", body["loaded"])
	}
	if body["index"] != "titles" {
		t.Errorf("index = %v, want titles", body["index"])
	}

	// The corpus must now be searchable through the normal endpoint.
	_, docs := searchGET(t, h, "index=titles&q=elasticsearch&limit=5")
	if hits := docs["hits"].([]any); len(hits) == 0 {
		t.Error("no hits for q=elasticsearch after loading samples")
	}

	// Default index name when none is given.
	res2, _ := doJSON(t, h, http.MethodPost, "/samples", "")
	if res2.StatusCode != http.StatusOK {
		t.Errorf("no-index load status = %d", res2.StatusCode)
	}

	// Reloading the same index re-indexes identical IDs, so result counts
	// must not grow (no duplicate documents).
	_, before := searchGET(t, h, "index=titles&q=search&limit=100")
	totalBefore := before["total"].(float64)
	doJSON(t, h, http.MethodPost, "/samples?index=titles", "")
	_, after := searchGET(t, h, "index=titles&q=search&limit=100")
	if after["total"].(float64) != totalBefore {
		t.Errorf("total changed across reload: %v -> %v", totalBefore, after["total"])
	}

	// Documents were created without the doc tool — spot-check the store
	// and the index directly.
	doc, ok := store.Get("titles", "1")
	if !ok || doc.Fields["title"] == nil {
		t.Errorf("titles/1 = %+v, ok=%v", doc, ok)
	}
}

// TestCorsHeaders: the standalone web console (served on another origin) needs
// CORS on every response and short-circuited browser preflights. Without it
// the live-test loop (open the console, edit, reload) fails at the browser.
func TestCorsHeaders(t *testing.T) {
	h, _ := newTestServer(t)

	// Ordinary responses carry the CORS headers the console needs.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}

	// Preflight: the browser sends OPTIONS before a PUT with
	// Content-Type: application/json; it must be answered without a handler.
	req = httptest.NewRequest(http.MethodOptions, "/api/search", nil)
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res = rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("preflight missing Access-Control-Allow-Methods")
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("preflight missing Access-Control-Allow-Headers")
	}
}

func TestPutRejectsOversizedBody(t *testing.T) {
	h, _ := newTestServer(t)

	body := `{"big":"` + strings.Repeat("x", maxBodyBytes+1024) + `"}`
	res, _ := doJSON(t, h, http.MethodPut, "/index/products/1", body)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized body", res.StatusCode)
	}
}

func searchGET(t *testing.T, h http.Handler, params string) (*http.Response, map[string]any) {
	t.Helper()
	return doJSON(t, h, http.MethodGet, "/api/search?"+params, "")
}

func TestSearchEndpointFindsIndexedDocuments(t *testing.T) {
	h, _ := newTestServer(t)

	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"red fox","price":10}`)
	doJSON(t, h, http.MethodPut, "/index/products/2", `{"name":"blue fox","price":20}`)
	doJSON(t, h, http.MethodPut, "/index/products/3", `{"name":"red panda","price":30}`)

	res, body := searchGET(t, h, "index=products&q=red+fox")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %v", res.StatusCode, body)
	}
	if body["total"] != float64(1) {
		t.Errorf("total = %v, want 1", body["total"])
	}
	rawHits, ok := body["hits"].([]any)
	if !ok || len(rawHits) != 1 {
		t.Fatalf("hits = %v, want 1 entry", body["hits"])
	}
	hit := rawHits[0].(map[string]any)
	if hit["_id"] != "1" {
		t.Errorf("_id = %v, want 1", hit["_id"])
	}
	if hit["_score"].(float64) <= 0 {
		t.Errorf("_score = %v, want > 0", hit["_score"])
	}
	fields := hit["fields"].(map[string]any)
	if fields["name"] != "red fox" {
		t.Errorf("fields.name = %v", fields["name"])
	}
}

func TestSearchEndpointRanksByScore(t *testing.T) {
	h, _ := newTestServer(t)

	// doc 2 repeats "widget", so it should outrank doc 1.
	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"widget"}`)
	doJSON(t, h, http.MethodPut, "/index/products/2", `{"name":"widget widget widget"}`)

	_, body := searchGET(t, h, "index=products&q=widget")
	hitsList := body["hits"].([]any)
	if len(hitsList) != 2 {
		t.Fatalf("hits = %d, want 2", len(hitsList))
	}
	first := hitsList[0].(map[string]any)
	second := hitsList[1].(map[string]any)
	if first["_id"] != "2" || second["_id"] != "1" {
		t.Errorf("order = %v, %v; want 2 then 1", first["_id"], second["_id"])
	}
	if first["_score"].(float64) <= second["_score"].(float64) {
		t.Errorf("scores = %v <= %v", first["_score"], second["_score"])
	}
}

func TestSearchEndpointReflectsUpdatesAndDeletes(t *testing.T) {
	h, _ := newTestServer(t)

	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"apple"}`)
	_, body := searchGET(t, h, "index=products&q=apple")
	if body["total"] != float64(1) {
		t.Fatalf("total = %v, want 1", body["total"])
	}

	// Update: old term must vanish from results.
	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"banana"}`)
	_, body = searchGET(t, h, "index=products&q=apple")
	if body["total"] != float64(0) {
		t.Errorf("stale term total = %v, want 0", body["total"])
	}

	// Delete: everything gone; searching now finds nothing (200, not a 404 —
	// an index with zero documents is not an error).
	doJSON(t, h, http.MethodDelete, "/index/products/1", "")
	res, body := searchGET(t, h, "index=products&q=banana")
	if res.StatusCode != http.StatusOK {
		t.Errorf("status after deleting last doc = %d, want 200", res.StatusCode)
	}
	if body["total"] != float64(0) {
		t.Errorf("total = %v, want 0", body["total"])
	}
	if hits, ok := body["hits"].([]any); !ok || len(hits) != 0 {
		t.Errorf("hits = %v, want empty", body["hits"])
	}
}

func TestSearchEndpointLimitAndTotal(t *testing.T) {
	h, _ := newTestServer(t)

	for i := 1; i <= 5; i++ {
		body := fmt.Sprintf(`{"n":%d,"text":"common word"}`, i)
		doJSON(t, h, http.MethodPut, fmt.Sprintf("/index/products/d%d", i), body)
	}

	_, payload := searchGET(t, h, "index=products&q=common&limit=2")
	if payload["total"] != float64(5) {
		t.Errorf("total = %v, want 5", payload["total"])
	}
	if hits := payload["hits"].([]any); len(hits) != 2 {
		t.Errorf("hits = %d, want 2", len(hits))
	}

	// Default limit is 20 (5 docs still all fit).
	_, payload = searchGET(t, h, "index=products&q=common")
	if hits := payload["hits"].([]any); len(hits) != 5 {
		t.Errorf("default limit hits = %d, want 5", len(hits))
	}
}

func TestSearchEndpointBadRequests(t *testing.T) {
	h, _ := newTestServer(t)
	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"fox"}`)

	cases := []struct {
		name   string
		params string
		status int
	}{
		{"missing index", "q=fox", http.StatusBadRequest},
		{"missing q", "index=products", http.StatusBadRequest},
		{"bad limit", "index=products&q=fox&limit=abc", http.StatusBadRequest},
		{"zero limit", "index=products&q=fox&limit=0", http.StatusBadRequest},
		{"negative limit", "index=products&q=fox&limit=-3", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := searchGET(t, h, tc.params)
			if res.StatusCode != tc.status {
				t.Errorf("status = %d, want %d (body %v)", res.StatusCode, tc.status, body)
			}
		})
	}
}

// TestSearchEndpointUnknownIndexIsNotAnError: searching an index that does
// not exist yields an empty result set rather than a 404.
func TestSearchEndpointUnknownIndexIsNotAnError(t *testing.T) {
	h, _ := newTestServer(t)

	res, body := searchGET(t, h, "index=never-heard-of-it&q=fox")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if body["total"] != float64(0) {
		t.Errorf("total = %v, want 0", body["total"])
	}
	if hits, ok := body["hits"].([]any); !ok || len(hits) != 0 {
		t.Errorf("hits = %v, want empty", body["hits"])
	}
}

// TestSearchEndpointPrefixMatching: "key" finds "keyboard" documents, with
// the exact match ranking first.
func TestSearchEndpointPrefixMatching(t *testing.T) {
	h, _ := newTestServer(t)

	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"keyboard"}`)
	doJSON(t, h, http.MethodPut, "/index/products/2", `{"name":"key"}`)

	res, body := searchGET(t, h, "index=products&q=key")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %v", res.StatusCode, body)
	}
	if body["total"] != float64(2) {
		t.Fatalf("total = %v, want 2", body["total"])
	}
	hits := body["hits"].([]any)
	first := hits[0].(map[string]any)
	second := hits[1].(map[string]any)
	if first["_id"] != "2" || second["_id"] != "1" {
		t.Errorf("order = %v, %v; want 2 (exact) then 1 (prefix)", first["_id"], second["_id"])
	}
	if first["_score"].(float64) <= second["_score"].(float64) {
		t.Errorf("exact score %v should exceed prefix score %v", first["_score"], second["_score"])
	}
}

// TestSearchEndpointSubstringMatching: "key" also finds documents whose words
// merely CONTAIN it ("monkey"), ranked below exact and prefix matches.
func TestSearchEndpointSubstringMatching(t *testing.T) {
	h, _ := newTestServer(t)

	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"key"}`)
	doJSON(t, h, http.MethodPut, "/index/products/2", `{"name":"keyboard"}`)
	doJSON(t, h, http.MethodPut, "/index/products/3", `{"name":"monkey"}`)

	res, body := searchGET(t, h, "index=products&q=key")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %v", res.StatusCode, body)
	}
	if body["total"] != float64(3) {
		t.Fatalf("total = %v, want 3", body["total"])
	}
	hits := body["hits"].([]any)
	first := hits[0].(map[string]any)
	second := hits[1].(map[string]any)
	third := hits[2].(map[string]any)
	if first["_id"] != "1" || second["_id"] != "2" || third["_id"] != "3" {
		t.Errorf("order = %v, %v, %v; want 1 (exact), 2 (prefix), 3 (substring)",
			first["_id"], second["_id"], third["_id"])
	}
	if third["_score"].(float64) >= second["_score"].(float64) {
		t.Errorf("substring score %v should be below prefix score %v",
			third["_score"], second["_score"])
	}
}

func TestSearchEndpointUnicodeQuery(t *testing.T) {
	h, _ := newTestServer(t)

	doJSON(t, h, http.MethodPut, "/index/products/1", `{"name":"Hello World Limited"}`)
	doJSON(t, h, http.MethodPut, "/index/products/2", `{"name":"Grüße aus München"}`)

	escape := func(s string) string { return url.QueryEscape(s) }

	// The analyzer lowercases and matches on exact (folded) terms.
	_, body := searchGET(t, h, "index=products&q="+escape("Grüße"))
	if body["total"] != float64(1) {
		t.Errorf("total for Grüße = %v, want 1", body["total"])
	}
	_, body = searchGET(t, h, "index=products&q="+escape("münchen"))
	if body["total"] != float64(1) {
		t.Errorf("total for münchen = %v, want 1", body["total"])
	}
	// No cross-folding ß→ss or ü→ue in the basic analyzer: matches nothing.
	_, body = searchGET(t, h, "index=products&q="+escape("GRÜSSE"))
	if body["total"] != float64(0) {
		t.Errorf("total for GRÜSSE = %v, want 0 (no ß→ss folding)", body["total"])
	}
	_, body = searchGET(t, h, "index=products&q=gruesse")
	if body["total"] != float64(0) {
		t.Errorf("total for gruesse = %v, want 0 (no ü→ue folding)", body["total"])
	}
}
