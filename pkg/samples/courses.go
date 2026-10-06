// Package samples provides ready-made corpora for trying the search engine
// without typing documents by hand.
package samples

import (
	"embed"
	"strings"
)

//go:embed courses.txt
var corpus embed.FS

// CourseTitles returns the embedded course-title corpus as a slice, one
// title per line. The corpus intentionally mixes casing, punctuation,
// deliberately misspelled variants (for future fuzzy-search testing) and
// non-English titles. Blank lines are skipped so the file stays greppable.
func CourseTitles() []string {
	data, err := corpus.ReadFile("courses.txt")
	if err != nil {
		return nil // go:embed guarantees the file exists; defensive only
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if title := strings.TrimSpace(line); title != "" {
			out = append(out, title)
		}
	}
	return out
}
