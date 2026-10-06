// Package search implements the core of the search engine: an inverted index
// with TF-IDF ranking.
//
// Structure:
//
//	Index
//	 └── named index
//	      ├── postings:  term → posting list (docs sorted by DocID)
//	      ├── docTerms:  docID → its unique terms (forward index, used to delete)
//	      └── docLen:    docID → token count (retained for future BM25)
//
// A "posting list" is the classic inverted-index entry for one term: every
// document containing that term, plus the positions of the occurrences.
// Keeping lists sorted by DocID lets us intersect them with binary search.
//
// Query understanding is intentionally minimal: the query string is run
// through the same StandardTokenizer used at index time and all tokens must
// match (AND). A token matches a document through an exact dictionary term, a
// term that starts with it (so "key" finds "keyboard"), or — with the lowest
// weight — a term containing it anywhere ("key" also finds "monkey"). Exact
// matches score highest, then prefix matches, then substring matches. Not yet
// supported, left for later phases:
//
//   - phrase queries (positions are already stored, enabling them)
//   - per-field queries such as title:widget (postings are field-agnostic)
//   - OR / NOT operators and fuzzy matching
//   - analyzers with stemming or stop-word removal
//   - BM25 ranking (docLen is maintained so average field length is known)
package search

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gosearch/pkg/analyze"
)

// tokenizer used at both index and query time. Using the SAME analyzer for
// both is essential: "WELT" at index time and "welt" at query time must
// produce the identical term or the match silently fails.
var tok = analyze.StandardTokenizer{}

// Posting is one entry in a term's posting list.
type Posting struct {
	DocID     string
	Positions []int // doc-wide token positions; term frequency = len(Positions)
}

// Hit is one scored search result.
type Hit struct {
	DocID string
	Score float64
}

// Index is a concurrent-safe inverted index over any number of named indices.
type Index struct {
	mu       sync.RWMutex
	postings map[string]map[string][]Posting // index name → term → postings
	docTerms map[string]map[string][]string  // index name → docID → unique terms
	docLen   map[string]map[string]int       // index name → docID → token count
	termDict map[string][]string             // index name → sorted unique terms (match expansion)
}

// NewIndex creates an empty inverted index.
func NewIndex() *Index {
	return &Index{
		postings: map[string]map[string][]Posting{},
		docTerms: map[string]map[string][]string{},
		docLen:   map[string]map[string]int{},
		termDict: map[string][]string{},
	}
}

// Add (re)indexes a document: its previous postings (if any) are removed
// first, then every scalar string found in fields is analyzed and recorded.
//
// Field names themselves are not indexed — only values — so this is a single
// undifferentiated text space per document (no field-qualified queries yet).
// Nested objects and arrays are flattened in deterministic order (sorted map
// keys) so token positions are reproducible across runs.
func (ix *Index) Add(index, docID string, fields map[string]any) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	ix.removeLocked(index, docID)

	if ix.postings[index] == nil {
		ix.postings[index] = map[string][]Posting{}
	}
	if ix.docTerms[index] == nil {
		ix.docTerms[index] = map[string][]string{}
	}
	if ix.docLen[index] == nil {
		ix.docLen[index] = map[string]int{}
	}

	// Analyze all values into term → positions, using a doc-wide running
	// position counter so occurrences in different fields don't collide.
	positions := map[string][]int{}
	n := 0
	for _, text := range flatten(fields) {
		tok.Tokenize(text, func(t analyze.Token) bool {
			positions[t.Term] = append(positions[t.Term], n)
			n++
			return true
		})
	}
	ix.docLen[index][docID] = n

	terms := make([]string, 0, len(positions))
	for term, poss := range positions {
		terms = append(terms, term)
		list := ix.postings[index][term]
		ix.postings[index][term] = insertPosting(list, Posting{DocID: docID, Positions: poss})
		ix.termDict[index] = insertTerm(ix.termDict[index], term)
	}
	sort.Strings(terms)
	ix.docTerms[index][docID] = terms
}

// Delete removes a document's postings. It reports whether the document
// existed in this index.
func (ix *Index) Delete(index, docID string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if _, ok := ix.docTerms[index][docID]; !ok {
		return false
	}
	ix.removeLocked(index, docID)
	return true
}

// Match-kind penalties scale the contribution of a query token that matched a
// document only through an expanded term. Exact term matches keep a penalty
// of 1. A prefix match — the index term starts with the token, e.g. "key" in
// "keyboard" — costs prefixPenalty. A substring match — the token appears
// mid-word, e.g. "key" in "cockey" — costs even less, so close matches always
// outrank loose ones at equal tf/idf.
const (
	prefixPenalty    = 0.4
	substringPenalty = 0.15
)

// Search scores documents in index against the query and returns up to limit
// hits sorted by descending score (ties broken by DocID for determinism),
// plus the total number of matching documents before the limit is applied.
//
// Semantics: the query is tokenized with the standard analyzer and a document
// must contain ALL query tokens (AND). A query token matches a document if the
// document contains ANY index term that equals the token (exact match), starts
// with it (prefix match, "key" finds "keyboard"), or contains it mid-word
// (substring match, "key" finds "monkey" or "cockey").
//
// Scoring is classic TF-IDF over each matched index term:
//
//	score(doc, token) = max_over_matched_terms  tf(term, doc) · ln(1 + N / df(term))
//	score(doc, query) = Σ_over_query_tokens  score(doc, token)
//
// where tf is the number of occurrences, N the number of documents in the
// index, and df the number of documents containing the term. tf rewards
// repetition; ln(1 + N/df) rewards rarity. A token's contribution is
// multiplied by the penalty of its closest expansion tier: 1 for an exact
// term, prefixPenalty for a prefix-only term, substringPenalty for a term that
// merely contains the token. So exact matches outrank prefix-only ones, and
// those outrank substring-only ones. limit <= 0 means "no limit".
func (ix *Index) Search(index, query string, limit int) ([]Hit, int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	qTerms := unique(analyze.Terms(tok, query))
	if len(qTerms) == 0 {
		return nil, 0
	}

	n := float64(len(ix.docLen[index]))

	// Per query token: the best per-document contribution across every index
	// term that token expands to (exact + prefix).
	perToken := make([]map[string]float64, len(qTerms))
	for ti, qt := range qTerms {
		expanded := ix.expandLocked(index, qt)
		if len(expanded) == 0 {
			return nil, 0 // AND semantics: one unmatchable token rules out every doc
		}
		best := make(map[string]float64)
		for _, e := range expanded {
			list := ix.postings[index][e.term]
			idf := math.Log(1 + n/float64(len(list)))
			for _, p := range list {
				c := float64(len(p.Positions)) * idf * e.penalty
				if cur, ok := best[p.DocID]; !ok || c > cur {
					best[p.DocID] = c
				}
			}
		}
		perToken[ti] = best
	}

	// Drive the intersection from the token with the fewest candidates and
	// keep only documents that scored under EVERY token.
	driver := perToken[0]
	for _, m := range perToken[1:] {
		if len(m) < len(driver) {
			driver = m
		}
	}
	matches := make([]Hit, 0, len(driver))
	for docID := range driver {
		var score float64
		matched := true
		for _, m := range perToken {
			c, ok := m[docID]
			if !ok {
				matched = false
				break
			}
			score += c
		}
		if matched {
			matches = append(matches, Hit{DocID: docID, Score: score})
		}
	}

	total := len(matches)
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].DocID < matches[j].DocID
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, total
}

// expandedTerm is one index term a query token expanded to, plus the penalty
// for that tier of match: 1 for an exact term, prefixPenalty for a term that
// starts with the token, substringPenalty for a term that merely contains it.
type expandedTerm struct {
	term    string
	penalty float64
}

// expandLocked resolves a single query token to every dictionary term that
// matches it: exact (term == token), prefix (term starts with token, a
// contiguous sorted range), or substring (token appears anywhere in the term).
// Callers must hold mu.
func (ix *Index) expandLocked(index, token string) []expandedTerm {
	dict := ix.termDict[index]
	start := sort.SearchStrings(dict, token)

	var out []expandedTerm
	// The terms starting with token form one contiguous range here.
	end := start
	for ; end < len(dict) && strings.HasPrefix(dict[end], token); end++ {
		penalty := 1.0
		if dict[end] != token {
			penalty = prefixPenalty
		}
		out = append(out, expandedTerm{term: dict[end], penalty: penalty})
	}
	// Substring matches (token mid-word, e.g. "cockey" for "key") live outside
	// that range, so scan the rest of the dictionary for them. A plain linear
	// scan is fine for this educational engine; production engines use a
	// suffix automaton or trigram index here.
	for i, term := range dict {
		if i >= start && i < end {
			continue
		}
		if strings.Contains(term, token) {
			out = append(out, expandedTerm{term: term, penalty: substringPenalty})
		}
	}
	return out
}

// insertTerm inserts term into a sorted unique term list, if not present.
func insertTerm(dict []string, term string) []string {
	i := sort.SearchStrings(dict, term)
	if i < len(dict) && dict[i] == term {
		return dict
	}
	dict = append(dict, "")
	copy(dict[i+1:], dict[i:])
	dict[i] = term
	return dict
}

// removeTerm removes term from a sorted unique term list, if present.
func removeTerm(dict []string, term string) []string {
	i := sort.SearchStrings(dict, term)
	if i >= len(dict) || dict[i] != term {
		return dict
	}
	return append(dict[:i], dict[i+1:]...)
}

// HasIndex reports whether the named index contains at least one document.
func (ix *Index) HasIndex(index string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docTerms[index]) > 0
}

// DocLength returns the token count of a document (0 also for existing but
// empty documents, hence the bool).
func (ix *Index) DocLength(index, docID string) (int, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	n, ok := ix.docLen[index][docID]
	return n, ok
}

// removeLocked deletes all traces of a document. Callers must hold mu.
func (ix *Index) removeLocked(index, docID string) {
	terms, ok := ix.docTerms[index][docID]
	if !ok {
		return
	}
	for _, term := range terms {
		list := ix.postings[index][term]
		if i, found := searchPosting(list, docID); found {
			list = append(list[:i], list[i+1:]...)
			if len(list) == 0 {
				delete(ix.postings[index], term)
				ix.termDict[index] = removeTerm(ix.termDict[index], term)
			} else {
				ix.postings[index][term] = list
			}
		}
	}
	delete(ix.docTerms[index], docID)
	delete(ix.docLen[index], docID)
	if len(ix.docTerms[index]) == 0 {
		// Mirror storage.Store: an index with no documents stops existing.
		delete(ix.docTerms, index)
		delete(ix.postings, index)
		delete(ix.docLen, index)
		delete(ix.termDict, index)
	}
}

// insertPosting inserts p keeping the list sorted by DocID.
// If p.DocID is already present its posting is replaced (defensive; Add
// removes a document before re-adding it, so duplicates should not occur).
func insertPosting(list []Posting, p Posting) []Posting {
	i, found := searchPosting(list, p.DocID)
	if found {
		list[i] = p
		return list
	}
	list = append(list, Posting{})
	copy(list[i+1:], list[i:])
	list[i] = p
	return list
}

// searchPosting binary-searches a sorted posting list for docID.
func searchPosting(list []Posting, docID string) (int, bool) {
	i := sort.Search(len(list), func(i int) bool { return list[i].DocID >= docID })
	return i, i < len(list) && list[i].DocID == docID
}

// unique deduplicates terms while preserving query order. Duplicate query
// terms are ignored (query-term frequency is not part of this scoring model).
func unique(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	out := terms[:0] // reuse the input slice; analyze.Terms already copied it
	for _, t := range terms {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// flatten converts document field values into an ordered list of analyzeable
// strings. Strings are kept as-is; numbers and bools are stringified so a
// query like "42" matches a numeric field; nil and unsupported values are
// skipped. Maps are visited in sorted key order for deterministic positions.
func flatten(v any) []string {
	var out []string
	flattenInto(v, &out)
	return out
}

func flattenInto(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case json.Number:
		*out = append(*out, t.String())
	case float64:
		*out = append(*out, strconv.FormatFloat(t, 'g', -1, 64))
	case float32:
		*out = append(*out, strconv.FormatFloat(float64(t), 'g', -1, 32))
	case int:
		*out = append(*out, strconv.Itoa(t))
	case int64:
		*out = append(*out, strconv.FormatInt(t, 10))
	case bool:
		*out = append(*out, strconv.FormatBool(t))
	case []any:
		for _, e := range t {
			flattenInto(e, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			flattenInto(t[k], out)
		}
	case nil:
		// skip
	default:
		// Unsupported types (functions, structs, ...) are not indexed.
	}
}
