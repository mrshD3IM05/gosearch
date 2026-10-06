// Package storage provides document storage for the search engine.
//
// This is Phase 1 of the project: a simple, in-memory document store that
// supports create/index, retrieve and delete operations. Documents are
// grouped into named indices (a "collection"), similar to how Elasticsearch
// organizes documents into indices.
//
// The store is intentionally in-memory only. Persistent storage (segments,
// write-ahead logs) is a later phase.
package storage

import (
	"sync"
)

// Document is the unit of data stored in the index.
//
// Fields holds arbitrary JSON-compatible values (string, float64, bool,
// nil, []any, map[string]any) so that documents can be schemaless,
// like Elasticsearch documents.
type Document struct {
	ID     string         `json:"id"`
	Fields map[string]any `json:"fields"`
}

// Store is a concurrent-safe, in-memory document store.
//
// Structure:
//
//	Store
//	 └── index name  →  (doc ID → Document)
type Store struct {
	mu      sync.RWMutex
	indices map[string]map[string]Document
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{indices: make(map[string]map[string]Document)}
}

// Put creates or replaces a document under the given index and ID.
//
// The ID passed separately wins over any ID inside the Document value;
// this keeps the mapping from REST path parameters obvious.
func (s *Store) Put(index, id string, doc Document) {
	s.mu.Lock()
	defer s.mu.Unlock()

	docs, ok := s.indices[index]
	if !ok {
		docs = make(map[string]Document)
		s.indices[index] = docs
	}

	doc.ID = id
	doc.Fields = cloneFields(doc.Fields)
	docs[id] = doc
}

// Get returns the document with the given index and ID.
// The second return value is false when the document (or index) does not exist.
//
// The returned Fields map is a shallow copy: callers may mutate it without
// affecting the stored copy. Nested values are shared; a deep copy would be
// more expensive and is not needed for correctness of the search pipeline,
// which treats documents as immutable once indexed.
func (s *Store) Get(index, id string) (Document, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	docs, ok := s.indices[index]
	if !ok {
		return Document{}, false
	}
	doc, ok := docs[id]
	if !ok {
		return Document{}, false
	}
	doc.Fields = cloneFields(doc.Fields)
	return doc, true
}

// Delete removes a document. It reports whether the document existed.
func (s *Store) Delete(index, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	docs, ok := s.indices[index]
	if !ok {
		return false
	}
	if _, ok := docs[id]; !ok {
		return false
	}
	delete(docs, id)

	// Drop the index itself once it is empty so Get/List stay consistent.
	if len(docs) == 0 {
		delete(s.indices, index)
	}
	return true
}

// List returns all documents in an index. Used by tests and debugging;
// the search engine will use the inverted index instead (later phases).
func (s *Store) List(index string) []Document {
	s.mu.RLock()
	defer s.mu.RUnlock()

	docs, ok := s.indices[index]
	if !ok {
		return nil
	}
	out := make([]Document, 0, len(docs))
	for _, d := range docs {
		d.Fields = cloneFields(d.Fields)
		out = append(out, d)
	}
	return out
}

// Indices returns the names of all non-empty indices.
func (s *Store) Indices() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(s.indices))
	for name := range s.indices {
		out = append(out, name)
	}
	return out
}

// cloneFields returns a shallow copy of the fields map.
// A nil map is returned as nil so `fields: null` round-trips through JSON.
func cloneFields(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
