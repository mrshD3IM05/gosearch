package storage

import (
	"reflect"
	"sync"
	"testing"
)

func TestPutGetRoundTrip(t *testing.T) {
	s := NewStore()
	s.Put("products", "1", Document{Fields: map[string]any{
		"name":  "widget",
		"price": 9.99,
	}})

	doc, ok := s.Get("products", "1")
	if !ok {
		t.Fatal("expected document to exist")
	}
	if doc.ID != "1" {
		t.Errorf("ID = %q, want %q", doc.ID, "1")
	}
	if got := doc.Fields["name"]; got != "widget" {
		t.Errorf("name = %v, want widget", got)
	}
	// JSON numbers decode as float64; we store Go values as-is, but
	// 9.99 entered as an untyped constant becomes float64.
	if got := doc.Fields["price"]; got != 9.99 {
		t.Errorf("price = %v, want 9.99", got)
	}
}

func TestPutOverwritesExistingDocument(t *testing.T) {
	s := NewStore()
	s.Put("products", "1", Document{Fields: map[string]any{"v": "old"}})
	s.Put("products", "1", Document{Fields: map[string]any{"v": "new"}})

	doc, ok := s.Get("products", "1")
	if !ok {
		t.Fatal("expected document to exist")
	}
	if doc.Fields["v"] != "new" {
		t.Errorf("v = %v, want new", doc.Fields["v"])
	}
	if len(s.List("products")) != 1 {
		t.Errorf("expected 1 document after overwrite, got %d", len(s.List("products")))
	}
}

func TestGetMissingDocument(t *testing.T) {
	s := NewStore()

	if _, ok := s.Get("products", "missing"); ok {
		t.Error("expected miss from empty store")
	}
	s.Put("products", "1", Document{Fields: map[string]any{"a": 1}})
	if _, ok := s.Get("other", "1"); ok {
		t.Error("expected miss for unknown index")
	}
	if _, ok := s.Get("products", "2"); ok {
		t.Error("expected miss for unknown id")
	}
}

func TestDelete(t *testing.T) {
	s := NewStore()
	s.Put("products", "1", Document{Fields: map[string]any{"a": 1}})

	if !s.Delete("products", "1") {
		t.Fatal("Delete of existing document reported false")
	}
	if _, ok := s.Get("products", "1"); ok {
		t.Error("document still present after Delete")
	}
	if s.Delete("products", "1") {
		t.Error("second Delete reported true, want false")
	}
	if s.Delete("products", "missing") {
		t.Error("Delete of missing document reported true")
	}
}

func TestDeleteRemovesEmptyIndex(t *testing.T) {
	s := NewStore()
	s.Put("products", "1", Document{Fields: map[string]any{"a": 1}})
	s.Delete("products", "1")

	if got := s.Indices(); len(got) != 0 {
		t.Errorf("Indices() = %v, want empty", got)
	}
	if got := s.List("products"); got != nil {
		t.Errorf("List() = %v, want nil", got)
	}
}

func TestIndicesAreIsolated(t *testing.T) {
	s := NewStore()
	s.Put("products", "1", Document{Fields: map[string]any{"who": "products"}})
	s.Put("users", "1", Document{Fields: map[string]any{"who": "users"}})

	doc, _ := s.Get("products", "1")
	if doc.Fields["who"] != "products" {
		t.Errorf("products/1 = %v", doc.Fields["who"])
	}
	doc, _ = s.Get("users", "1")
	if doc.Fields["who"] != "users" {
		t.Errorf("users/1 = %v", doc.Fields["who"])
	}
}

// TestGetReturnsCopy ensures callers cannot mutate the store through a
// previously returned document.
func TestGetReturnsCopy(t *testing.T) {
	s := NewStore()
	s.Put("products", "1", Document{Fields: map[string]any{"v": "original"}})

	doc, _ := s.Get("products", "1")
	doc.Fields["v"] = "mutated"
	doc.Fields["extra"] = true

	got, _ := s.Get("products", "1")
	if got.Fields["v"] != "original" {
		t.Errorf("stored value changed to %v", got.Fields["v"])
	}
	if _, ok := got.Fields["extra"]; ok {
		t.Error("mutation leaked into store")
	}
}

func TestListReturnsAllDocuments(t *testing.T) {
	s := NewStore()
	s.Put("products", "1", Document{Fields: map[string]any{"n": 1}})
	s.Put("products", "2", Document{Fields: map[string]any{"n": 2}})
	s.Put("users", "1", Document{Fields: map[string]any{"n": 9}})

	docs := s.List("products")
	if len(docs) != 2 {
		t.Fatalf("len = %d, want 2", len(docs))
	}
	ids := map[string]bool{}
	for _, d := range docs {
		ids[d.ID] = true
	}
	if !ids["1"] || !ids["2"] {
		t.Errorf("ids = %v, want {1,2}", ids)
	}
}

// TestConcurrentAccess exercises the store under the race detector
// (run with `go test -race`).
func TestConcurrentAccess(t *testing.T) {
	s := NewStore()
	const workers = 8
	const n = 100

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				id := string(rune('a'+w)) + string(rune('a'+i%26))
				s.Put("idx", id, Document{Fields: map[string]any{"w": w, "i": i}})
				s.Get("idx", id)
				if i%3 == 0 {
					s.Delete("idx", id)
				}
			}
		}(w)
	}
	wg.Wait()

	// Store must remain consistent: every listed document must be retrievable.
	for _, d := range s.List("idx") {
		if _, ok := s.Get("idx", d.ID); !ok {
			t.Errorf("listed document %q not retrievable", d.ID)
		}
	}
}

func TestCloneFieldsNil(t *testing.T) {
	if got := cloneFields(nil); got != nil {
		t.Errorf("cloneFields(nil) = %v, want nil", got)
	}
	in := map[string]any{"a": 1}
	out := cloneFields(in)
	if !reflect.DeepEqual(in, out) {
		t.Errorf("cloneFields(%v) = %v", in, out)
	}
}
