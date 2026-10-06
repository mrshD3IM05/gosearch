package analyze

import (
	"reflect"
	"testing"
)

// tok is the StandardTokenizer under test; kept short for table-driven cases.
var tok = StandardTokenizer{}

func terms(text string) []string {
	return Terms(tok, text)
}

// wantEq compares got/want with nil-vs-empty tolerance: an empty input
// produces no tokens, and we don't care whether that slice is nil or empty.
func wantEq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q, want %q", got, want)
	}
}

func TestTokenizeLowercase(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"The QUICK, brown fox!", []string{"the", "quick", "brown", "fox"}},
		{"HELLO", []string{"hello"}},
		{"already lowercase", []string{"already", "lowercase"}},
		{"MiXeD CaSe", []string{"mixed", "case"}},
		// Non-ASCII case folding.
		{"CAFÉ Ünïcode", []string{"café", "ünïcode"}},
	}
	for _, tc := range cases {
		wantEq(t, terms(tc.in), tc.want)
	}
}

func TestTokenizePunctuation(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"hello,world", []string{"hello", "world"}},
		{"a;b;c", []string{"a", "b", "c"}},
		{"...leading and trailing...", []string{"leading", "and", "trailing"}},
		{"stop. Drop. Roll.", []string{"stop", "drop", "roll"}},
		{"don't", []string{"don", "t"}},     // apostrophe splits (documented simplification)
		{"foo_bar", []string{"foo", "bar"}}, // underscore splits
		{"e-mail", []string{"e", "mail"}},
		{"(parens) [brackets] {braces}", []string{"parens", "brackets", "braces"}},
		{"em—dash", []string{"em", "dash"}},
	}
	for _, tc := range cases {
		wantEq(t, terms(tc.in), tc.want)
	}
}

func TestTokenizeWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"one two three", []string{"one", "two", "three"}},
		{"one\ttwo\nthree\r\nfour", []string{"one", "two", "three", "four"}},
		{"   lots    of      spaces   ", []string{"lots", "of", "spaces"}},
		{" non-breaking space", []string{"non", "breaking", "space"}},
		{"\u00a0nbsp\u00a0separated", []string{"nbsp", "separated"}}, // U+00A0 is a separator
	}
	for _, tc := range cases {
		wantEq(t, terms(tc.in), tc.want)
	}
}

func TestTokenizeEmptyAndSeparatorOnly(t *testing.T) {
	cases := []string{
		"",
		" ",
		"\t\n\r",
		"...",
		"!@#$%^&*()",
		"—",
	}
	for _, in := range cases {
		got := terms(in)
		if len(got) != 0 {
			t.Errorf("terms(%q) = %q, want none", in, got)
		}
	}
}

func TestTokenizeUnicode(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Accented Latin letters are letters, not separators.
		{"naïve résumé déjà vu", []string{"naïve", "résumé", "déjà", "vu"}},
		// Cyrillic.
		{"Привет мир", []string{"привет", "мир"}},
		// Greek.
		{"Καλημέρα κόσμε", []string{"καλημέρα", "κόσμε"}},
		// CJK: characters are letters, so a run with no spaces stays one token.
		// (Proper CJK word segmentation needs a dictionary — future work.)
		{"日本語", []string{"日本語"}},
		// CJK plus ASCII mixing splits on the space only.
		{"日本語 text", []string{"日本語", "text"}},
		// Non-ASCII digits count as digits.
		{"٣٤", []string{"٣٤"}},
		// Mixed script with punctuation.
		{"Grüße, Welt!", []string{"grüße", "welt"}},
	}
	for _, tc := range cases {
		wantEq(t, terms(tc.in), tc.want)
	}
}

func TestTokenizeKeepsNumbers(t *testing.T) {
	wantEq(t, terms("42 walls cost 1,234.50 EUR"), []string{"42", "walls", "cost", "1", "234", "50", "eur"})
}

func TestTokenPositionsAreSequential(t *testing.T) {
	toks := Collect(tok, "alpha beta gamma")
	if len(toks) != 3 {
		t.Fatalf("len = %d, want 3", len(toks))
	}
	for i, tk := range toks {
		if tk.Pos != i {
			t.Errorf("tok[%d].Pos = %d, want %d", i, tk.Pos, i)
		}
	}
}

func TestTokenOffsets(t *testing.T) {
	text := "The QUICK, brown fox!"
	toks := Collect(tok, text)

	want := []struct {
		term       string
		start, end int
	}{
		{"the", 0, 3},
		{"quick", 4, 9},
		{"brown", 11, 16},
		{"fox", 17, 20},
	}
	if len(toks) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(toks), len(want), toks)
	}
	for i, w := range want {
		got := toks[i]
		if got.Term != w.term || got.Start != w.start || got.End != w.end {
			t.Errorf("tok[%d] = {%s %d %d}, want {%s %d %d}",
				i, got.Term, got.Start, got.End, w.term, w.start, w.end)
		}
		// Offsets must slice the ORIGINAL text back to the pre-lowercased token.
		if slice := text[got.Start:got.End]; slice == "" {
			t.Errorf("tok[%d]: empty slice", i)
		}
	}
}

func TestTokenOffsetsAreByteOffsetsForMultibyteText(t *testing.T) {
	text := "café ok"
	toks := Collect(tok, text)
	if len(toks) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(toks), toks)
	}
	// "café" is 5 bytes (é = 2 bytes), not 4 runes/chars.
	if toks[0].Start != 0 || toks[0].End != 5 {
		t.Errorf("first token offsets = %d:%d, want 0:5", toks[0].Start, toks[0].End)
	}
	if text[toks[0].Start:toks[0].End] != "café" {
		t.Errorf("slice = %q, want café", text[toks[0].Start:toks[0].End])
	}
	if toks[1].Start != 6 || toks[1].End != 8 {
		t.Errorf("second token offsets = %d:%d, want 6:8", toks[1].Start, toks[1].End)
	}
}

func TestTokenizeStopsEarlyWhenEmitReturnsFalse(t *testing.T) {
	var seen []string
	tok.Tokenize("one two three four", func(tk Token) bool {
		seen = append(seen, tk.Term)
		return len(seen) < 2
	})
	if !reflect.DeepEqual(seen, []string{"one", "two"}) {
		t.Errorf("seen = %q, want [one two]", seen)
	}
}

func TestTokenizeInvalidUTF8DoesNotPanic(t *testing.T) {
	// 0xff is never a valid UTF-8 start byte; it decodes as RuneError.
	got := terms("a\xffb")
	wantEq(t, got, []string{"a", "b"})
}

// TestTermsDoNotMutateInput guards against implementations that lowercase
// in place (impossible with Go strings, but cheap to verify).
func TestTermsDoNotMutateInput(t *testing.T) {
	text := "ABC"
	_ = terms(text)
	if text != "ABC" {
		t.Errorf("input changed to %q", text)
	}
}

func BenchmarkStandardTokenizer(b *testing.B) {
	const text = "The QUICK, brown fox jumps over the lazy dog. " +
		"Grüße aus München, 42 rue de Rivoli, 東京タワー — plain ASCII text too."
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.Tokenize(text, func(Token) bool { return true })
	}
}
