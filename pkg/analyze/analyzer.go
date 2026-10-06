// Package analyze turns raw text into a stream of tokens (terms) that can be
// fed into the inverted index.
//
// Phase 2 provides a basic, from-scratch tokenizer:
//
//   - lowercase normalization
//   - word splitting on punctuation and whitespace
//   - Unicode-aware character classification (no ASCII-only assumptions)
//   - byte offsets back into the original text (needed later for highlighting)
//
// Deliberately NOT implemented yet (later phases):
//
//   - stemming / lemmatization
//   - stop-word removal
//   - language-specific analysis chains (analyzers)
//   - synonym handling
package analyze

import (
	"strings"
	"unicode"
)

// Token is one unit of the token stream.
//
// Offsets are byte offsets into the original (pre-lowercasing) text, with
// Start inclusive and End exclusive, matching strings.Slice semantics.
// Using byte offsets (rather than rune indexes) keeps them directly usable
// with Go string slicing without conversion.
//
// Pos is the ordinal position of the token in the stream (0-based). It is a
// simple counter for now; after stop-word removal we would instead track
// position increments so phrase queries can span removed words.
type Token struct {
	Term  string
	Pos   int
	Start int
	End   int
}

// Tokenizer splits text into tokens, invoking emit for each one.
//
// Iteration stops early if emit returns false, which lets callers implement
// top-k or short-circuit logic without allocating a full slice.
//
// Terms are substrings of the input (zero-copy), so retaining a token also
// retains the whole input buffer. That is safe because Go strings are
// immutable; the trade-off is documented rather than paying a copy per token.
type Tokenizer interface {
	Tokenize(text string, emit func(Token) bool)
}

// StandardTokenizer is the default tokenizer: it keeps runs of Unicode
// letters and digits together and splits on everything else, then lowercases
// each term.
//
// Character classes:
//
//	letter  (unicode.IsLetter)  → token char, e.g. "fox", "café", "日"
//	digit   (unicode.IsDigit)   → token char, e.g. "42", "٣" (Arabic-Indic 3)
//	other   (punctuation, symbols, whitespace, underscore) → separator
//
// Underscore is treated as a separator ("foo_bar" → ["foo","bar"]), a
// deliberate simplification versus UAX#29 word segmentation, which would
// join such words. Splitting is simpler and matches common expectations
// for a first-generation tokenizer.
type StandardTokenizer struct{}

// Tokenize implements Tokenizer.
func (StandardTokenizer) Tokenize(text string, emit func(Token) bool) {
	start := -1 // byte offset where the current token begins, -1 = in separator
	pos := 0

	// range over a string yields (byte index, rune); the byte index is
	// exactly what we need for offsets. Invalid UTF-8 bytes decode to
	// utf8.RuneError, which is not a token char, so they act as separators
	// and the index always advances.
	for i, r := range text {
		if isTokenChar(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			// Hit a separator: flush the token that just ended.
			if !emit(makeToken(text, start, i, pos)) {
				return
			}
			pos++
			start = -1
		}
	}
	// Flush a token that runs to the end of the input.
	if start >= 0 {
		emit(makeToken(text, start, len(text), pos))
	}
}

// makeToken builds a Token for text[start:end]. strings.ToLower returns the
// input unchanged when it is already lowercase, so all-lowercase input
// produces zero allocations for terms.
func makeToken(text string, start, end, pos int) Token {
	return Token{
		Term:  strings.ToLower(text[start:end]),
		Pos:   pos,
		Start: start,
		End:   end,
	}
}

// isTokenChar reports whether r belongs inside a token.
func isTokenChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// Collect runs the tokenizer and returns all tokens as a slice.
func Collect(t Tokenizer, text string) []Token {
	var out []Token
	t.Tokenize(text, func(tok Token) bool {
		out = append(out, tok)
		return true
	})
	return out
}

// Terms is a convenience that returns just the terms (the common case when
// feeding the inverted index).
func Terms(t Tokenizer, text string) []string {
	var out []string
	t.Tokenize(text, func(tok Token) bool {
		out = append(out, tok.Term)
		return true
	})
	return out
}
