package search

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
)

// hits is a helper returning just the doc IDs from a search result.
func hits(h []Hit) []string {
	out := make([]string, len(h))
	for i, x := range h {
		out[i] = x.DocID
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func TestAddAndSearchSingleTerm(t *testing.T) {
	ix := NewIndex()
	ix.Add("products", "1", map[string]any{"name": "red fox"})

	got, total := ix.Search("products", "fox", 10)
	if total != 1 || len(got) != 1 {
		t.Fatalf("got %v total=%d, want 1 hit", hits(got), total)
	}
	if got[0].DocID != "1" {
		t.Errorf("DocID = %q, want 1", got[0].DocID)
	}
	if got[0].Score <= 0 {
		t.Errorf("Score = %v, want > 0", got[0].Score)
	}
}

func TestSearchIsCaseAndUnicodeInsensitiveViaAnalyzer(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"title": "Grüße WELT"})

	for _, q := range []string{"welt", "WELT", "grüße", "GRÜSSE"} {
		got, total := ix.Search("idx", q, 10)
		// "GRÜSSE" has no ß→ss folding in the basic analyzer: no match,
		// which documents the (intentional) lack of Unicode folding.
		if q == "GRÜSSE" {
			if total != 0 {
				t.Errorf("query %q: expected no match without folding, got %v", q, hits(got))
			}
			continue
		}
		if total != 1 {
			t.Errorf("query %q: total = %d, want 1", q, total)
		}
	}
}

func TestAndSemantics(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"text": "red fox"})
	ix.Add("idx", "2", map[string]any{"text": "red panda"})

	got, total := ix.Search("idx", "red fox", 10)
	if total != 1 || !equalStrings(hits(got), []string{"1"}) {
		t.Errorf(`query "red fox": got %v total=%d, want [1] total=1`, hits(got), total)
	}

	got, total = ix.Search("idx", "red elephant", 10)
	if total != 0 || len(got) != 0 {
		t.Errorf(`query "red elephant": got %v total=%d, want no hits`, hits(got), total)
	}
}

// TestRankingHigherTFWins: more occurrences of the query term ranks higher.
func TestRankingHigherTFWins(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "a", map[string]any{"text": "fox fox fox"})
	ix.Add("idx", "b", map[string]any{"text": "fox"})

	got, _ := ix.Search("idx", "fox", 10)
	if !equalStrings(hits(got), []string{"a", "b"}) {
		t.Errorf("order = %v, want [a b] (higher tf first)", hits(got))
	}
	if got[0].Score <= got[1].Score {
		t.Errorf("scores = %v, want a > b", got)
	}
}

// TestRankingRareTermScoresHigher: with the same tf, a rare term (high IDF)
// scores higher than a common one (low IDF).
func TestRankingRareTermScoresHigher(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "d1", map[string]any{"text": "common"})
	ix.Add("idx", "d2", map[string]any{"text": "common"})
	ix.Add("idx", "d3", map[string]any{"text": "common"})
	ix.Add("idx", "d4", map[string]any{"text": "rare"})

	commonHits, _ := ix.Search("idx", "common", 10)
	rareHits, _ := ix.Search("idx", "rare", 10)
	if len(commonHits) != 3 || len(rareHits) != 1 {
		t.Fatalf("common=%d rare=%d hits, want 3 and 1", len(commonHits), len(rareHits))
	}

	// idf(common) = ln(1+4/3) ≈ 0.85 with df=3
	// idf(rare)   = ln(1+4/1) ≈ 1.61 with df=1
	// Same tf=1, so the rare-term hit must score higher.
	if rareHits[0].Score <= commonHits[0].Score {
		t.Errorf("rare score %v <= common score %v; IDF is not weighting rarity",
			rareHits[0].Score, commonHits[0].Score)
	}
}

// TestRankingScoreAccumulatesOverQueryTerms: every query term contributes
// to the final score of an AND match.
func TestRankingScoreAccumulatesOverQueryTerms(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "d1", map[string]any{"text": "fox elephant"})
	ix.Add("idx", "d2", map[string]any{"text": "fox fox elephant"})

	got, total := ix.Search("idx", "fox elephant", 10)
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if !equalStrings(hits(got), []string{"d2", "d1"}) {
		t.Errorf("order = %v, want [d2 d1] (d2 repeats fox)", hits(got))
	}
	// d2's score = 2·idf(fox) + 1·idf(elephant), strictly above d1's.
	if got[0].Score <= got[1].Score {
		t.Errorf("scores = %v, want d2 > d1", got)
	}
}

// TestTieBreaksByDocID keeps result order deterministic across runs.
func TestTieBreaksByDocID(t *testing.T) {
	ix := NewIndex()
	for _, id := range []string{"zeta", "alpha", "mid"} {
		ix.Add("idx", id, map[string]any{"text": "same text"})
	}
	got, _ := ix.Search("idx", "same", 10)
	if !equalStrings(hits(got), []string{"alpha", "mid", "zeta"}) {
		t.Errorf("order = %v, want sorted by DocID", hits(got))
	}
}

func TestUpdateReplacesPostings(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"text": "apple"})
	if n, ok := ix.DocLength("idx", "1"); !ok || n != 1 {
		t.Fatalf("DocLength = %d, %v; want 1, true", n, ok)
	}

	ix.Add("idx", "1", map[string]any{"text": "banana split"})

	if got, total := ix.Search("idx", "apple", 10); total != 0 {
		t.Errorf(`stale term "apple" still matches: %v`, hits(got))
	}
	got, total := ix.Search("idx", "banana", 10)
	if total != 1 || !equalStrings(hits(got), []string{"1"}) {
		t.Errorf(`"banana": got %v total=%d, want [1]`, hits(got), total)
	}
	if n, _ := ix.DocLength("idx", "1"); n != 2 {
		t.Errorf("DocLength = %d, want 2 after update", n)
	}
	// No duplicate posting was created.
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if list := ix.postings["idx"]["banana"]; len(list) != 1 {
		t.Errorf("postings for banana = %d entries, want 1", len(list))
	}
}

func TestDeleteRemovesDocument(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"text": "red fox"})

	if !ix.Delete("idx", "1") {
		t.Fatal("Delete reported false")
	}
	if ix.Delete("idx", "1") {
		t.Error("second Delete reported true")
	}
	if got, total := ix.Search("idx", "fox", 10); total != 0 {
		t.Errorf("still matching after delete: %v", hits(got))
	}
	if _, ok := ix.DocLength("idx", "1"); ok {
		t.Error("DocLength still present after delete")
	}
	if ix.HasIndex("idx") {
		t.Error("HasIndex = true after deleting last document")
	}
}

func TestIndicesAreIsolated(t *testing.T) {
	ix := NewIndex()
	ix.Add("products", "1", map[string]any{"text": "laptop"})
	ix.Add("users", "1", map[string]any{"text": "alice"})

	if got, total := ix.Search("products", "alice", 10); total != 0 {
		t.Errorf("products matches alice: %v", hits(got))
	}
	if got, total := ix.Search("users", "laptop", 10); total != 0 {
		t.Errorf("users matches laptop: %v", hits(got))
	}
	// Deleting from one index must not touch the other.
	ix.Delete("products", "1")
	if !ix.HasIndex("users") {
		t.Error("users index lost when products doc deleted")
	}
}

func TestLimitAndTotal(t *testing.T) {
	ix := NewIndex()
	for i := 0; i < 5; i++ {
		ix.Add("idx", fmt.Sprintf("d%d", i), map[string]any{"text": "common word"})
	}
	got, total := ix.Search("idx", "common", 2)
	if total != 5 {
		t.Errorf("total = %d, want 5 (total counts pre-limit matches)", total)
	}
	if len(got) != 2 {
		t.Errorf("len(hits) = %d, want 2", len(got))
	}

	got, _ = ix.Search("idx", "common", 0) // limit <= 0 → all
	if len(got) != 5 {
		t.Errorf("limit 0 returned %d hits, want 5", len(got))
	}
}

func TestEmptyQueryAndUnknownIndex(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"text": "fox"})

	for _, q := range []string{"", "   ", "...", "!!!"} {
		if got, total := ix.Search("idx", q, 10); total != 0 || got != nil {
			t.Errorf("query %q: got %v total=%d, want nil/0", q, got, total)
		}
	}
	if got, total := ix.Search("missing", "fox", 10); total != 0 || got != nil {
		t.Errorf("unknown index: got %v total=%d, want nil/0", got, total)
	}
	if ix.HasIndex("missing") {
		t.Error("HasIndex(missing) = true")
	}
}

func TestNumericAndBoolFieldsAreSearchable(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{
		"price": json.Number("9.99"),
		"stock": 42,
		"in":    true,
		"tags":  []any{"sale", "new"},
		"meta":  map[string]any{"color": "red"},
	})

	cases := []string{"9.99", "42", "true", "sale", "red"}
	for _, q := range cases {
		if _, total := ix.Search("idx", q, 10); total != 1 {
			t.Errorf("query %q: total = %d, want 1", q, total)
		}
	}
	if _, total := ix.Search("idx", "missingfield", 10); total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

func TestEmptyDocumentIsStoredButMatchesNothing(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "empty", map[string]any{})

	if !ix.HasIndex("idx") {
		t.Error("empty document should still create the index")
	}
	if n, ok := ix.DocLength("idx", "empty"); !ok || n != 0 {
		t.Errorf("DocLength = %d, %v; want 0, true", n, ok)
	}
	if _, total := ix.Search("idx", "anything", 10); total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

// TestPrefixMatchFindsLongerTerms: "key" must find docs containing "keyboard",
// with the exact-token doc ranking above the prefix-only one.
func TestPrefixMatchFindsLongerTerms(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "a", map[string]any{"text": "keyboard"})
	ix.Add("idx", "b", map[string]any{"text": "key"})

	got, total := ix.Search("idx", "key", 10)
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if !equalStrings(hits(got), []string{"b", "a"}) {
		t.Errorf("order = %v, want [b a] (exact key before prefix keyboard)", hits(got))
	}
	if got[0].Score <= got[1].Score {
		t.Errorf("scores = %v, want exact key match > prefix keyboard match", got)
	}
}

// TestPrefixMatchPenalty: with identical tf and df, the prefix-only
// contribution is exactly prefixPenalty × the exact one.
func TestPrefixMatchPenalty(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "a", map[string]any{"text": "keyboard"})
	ix.Add("idx", "b", map[string]any{"text": "key"})

	got, _ := ix.Search("idx", "key", 10)
	if len(got) != 2 {
		t.Fatalf("hits = %d, want 2", len(got))
	}
	// Same df=1, same tf=1 → ratio must equal prefixPenalty exactly.
	ratio := got[1].Score / got[0].Score
	if math.Abs(ratio-prefixPenalty) > 1e-9 {
		t.Errorf("ratio = %v, want %v (prefix penalty)", ratio, prefixPenalty)
	}
}

// TestMatchRequiresTermInDictionary: a token needs an exact, prefix, or
// substring dictionary match. Substrings count (mid-word), but a token with no
// match at all rules out every document.
func TestMatchRequiresTermInDictionary(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"text": "keyboard"})

	if _, total := ix.Search("idx", "keyboard", 10); total != 1 {
		t.Errorf("keyboard: total = %d, want 1", total)
	}
	if _, total := ix.Search("idx", "key", 10); total != 1 {
		t.Errorf("key: total = %d, want 1 (prefix)", total)
	}
	if _, total := ix.Search("idx", "board", 10); total != 1 {
		t.Errorf("board: total = %d, want 1 (substring of keyboard)", total)
	}
	if _, total := ix.Search("idx", "eybo", 10); total != 1 {
		t.Errorf("eybo: total = %d, want 1 (interior substring)", total)
	}
	if _, total := ix.Search("idx", "keyz", 10); total != 0 {
		t.Errorf("keyz: total = %d, want 0 (no matching dictionary term)", total)
	}
}

// TestSubstringMatchFindsContainingTerms: "key" must also find docs whose term
// merely CONTAINS the token mid-word ("cockey", "monkey"), ranking below both
// the exact and the prefix matches.
func TestSubstringMatchFindsContainingTerms(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "exact", map[string]any{"text": "key"})
	ix.Add("idx", "prefix", map[string]any{"text": "keyboard"})
	ix.Add("idx", "substr", map[string]any{"text": "cockey"})
	ix.Add("idx", "substr2", map[string]any{"text": "monkey"})

	got, total := ix.Search("idx", "key", 10)
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	if !equalStrings(hits(got), []string{"exact", "prefix", "substr", "substr2"}) {
		t.Errorf("order = %v, want [exact prefix substr substr2]", hits(got))
	}
	if got[0].Score <= got[1].Score || got[1].Score <= got[2].Score {
		t.Errorf("scores = %v, want exact > prefix > substring", got)
	}
}

// TestSubstringMatchPenalty: with identical tf and df, a substring-only
// contribution is exactly substringPenalty × the exact one.
func TestSubstringMatchPenalty(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "a", map[string]any{"text": "key"})
	ix.Add("idx", "b", map[string]any{"text": "cockey"})

	got, _ := ix.Search("idx", "key", 10)
	if len(got) != 2 {
		t.Fatalf("hits = %d, want 2", len(got))
	}
	ratio := got[1].Score / got[0].Score
	if math.Abs(ratio-substringPenalty) > 1e-9 {
		t.Errorf("ratio = %v, want %v (substring penalty)", ratio, substringPenalty)
	}
}

// TestPrefixMultitokenAND: AND still applies across tokens after expansion.
func TestPrefixMultitokenAND(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"text": "keyboard wireless"})
	ix.Add("idx", "2", map[string]any{"text": "keyboard wired"})
	ix.Add("idx", "3", map[string]any{"text": "wireless mouse"})

	got, total := ix.Search("idx", "key wireless", 10)
	if total != 1 || !equalStrings(hits(got), []string{"1"}) {
		t.Errorf(`"key wireless": got %v total=%d, want [1]`, hits(got), total)
	}
}

// TestPrefixTermDictionaryIsMaintained: updating a document so a term
// disappears must drop it from prefix expansion too.
func TestPrefixTermDictionaryIsMaintained(t *testing.T) {
	ix := NewIndex()
	ix.Add("idx", "1", map[string]any{"text": "keyboard"})

	ix.Add("idx", "1", map[string]any{"text": "monitor"}) // drop "keyboard"

	if _, total := ix.Search("idx", "key", 10); total != 0 {
		t.Errorf(`stale "keyboard" still matched via prefix: total=%d`, total)
	}
	if _, total := ix.Search("idx", "mon", 10); total != 1 {
		t.Errorf("mon: total = %d, want 1", total)
	}

	// And removing the last doc clears the term dictionary with the index.
	ix.Delete("idx", "1")
	if got, total := ix.Search("idx", "mon", 10); total != 0 {
		t.Errorf("after delete: got %v total=%d, want 0", hits(got), total)
	}
}

// TestConcurrentUse is exercised under the race detector (go test -race).
func TestConcurrentUse(t *testing.T) {
	ix := NewIndex()
	const workers = 8
	const n = 50

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("w%d-%d", w, i)
				ix.Add("idx", id, map[string]any{"text": fmt.Sprintf("doc %d word%d", i, i%5)})
				ix.Search("idx", "word1 doc", 5)
				if i%2 == 0 {
					ix.Delete("idx", id)
				}
			}
		}(w)
	}
	wg.Wait()

	// Consistency: every document reported present must be findable and
	// every missing one must not be (spot check via DocLength).
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for docID := range ix.docLen["idx"] {
		if _, ok := ix.docTerms["idx"][docID]; !ok {
			t.Errorf("docLen has %q but docTerms does not", docID)
		}
	}
	for term, list := range ix.postings["idx"] {
		for _, p := range list {
			terms := ix.docTerms["idx"][p.DocID]
			found := false
			for _, tm := range terms {
				if tm == term {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("posting %q→%q not reflected in docTerms", term, p.DocID)
			}
		}
	}
}
