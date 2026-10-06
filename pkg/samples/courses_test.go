package samples

import (
	"strings"
	"testing"
)

func TestCourseTitles(t *testing.T) {
	titles := CourseTitles()

	// The corpus is large enough to be a meaningful test set.
	if len(titles) < 500 {
		t.Fatalf("len(CourseTitles()) = %d, want at least 500", len(titles))
	}

	// Spot-check representative entries that must be present verbatim.
	want := map[string]bool{
		"Introduction to Go Programming":        true,
		"Building a Search Engine from Scratch": true,
		"Elasticsearch Tutoral":                 true, // deliberate typo variant
		"Introduction à la programmation Go":    true, // non-ASCII (é)
		"Recherche plein texte":                 true,
		"GO SEARCH ENGINE":                      true, // casing variant
		"Building a Search Engine From Scratch": true, // ASCII-capitalised variant
	}

	for w := range want {
		found := false
		for _, got := range titles {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing expected title %q", w)
		}
	}
}

func TestCourseTitlesSkipBlankLines(t *testing.T) {
	for _, s := range CourseTitles() {
		if s == "" || strings.TrimSpace(s) != s {
			t.Errorf("corpus contains blank or non-trimmed line %q", s)
		}
	}
}
