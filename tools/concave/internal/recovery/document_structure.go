package recovery

import (
	"concave/internal/document"
	"concave/internal/repository"
	"strings"
)

// Converted pages keep their publishing owner's metadata and import syntax.
func structuralDocumentEdit(facts document.Facts, source string, h repository.Hunk) bool {
	ranges := append([]document.Range{}, func() []document.Range {
		if facts.Frontmatter != nil {
			return []document.Range{*facts.Frontmatter}
		}
		return nil
	}()...)
	for _, imported := range facts.Imports {
		ranges = append(ranges, imported.Range)
	}
	for _, span := range ranges {
		start := strings.Count(source[:span.Start], "\n")
		end := strings.Count(source[:span.End], "\n") + 1
		if h.Start < end && h.End > start || h.Start == h.End && h.Start >= start && h.Start < end {
			return true
		}
	}
	return false
}
