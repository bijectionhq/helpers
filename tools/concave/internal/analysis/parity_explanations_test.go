package analysis

import (
	"concave/internal/policy"
	"testing"
)

func TestChangedExplanationIsBoundToItsOwnSourceSpan(t *testing.T) {
	old := policy.Exception{ID: "first", Path: "Justfile", Start: 23, End: 23, ReplacementSHA256: "old"}
	other := policy.Exception{ID: "other", Path: "Justfile", Start: 27, End: 28, ReplacementSHA256: "unchanged"}
	replacement := old
	replacement.ReplacementSHA256 = "new"
	changes := changedExplanations(&policy.Config{Exceptions: []policy.Exception{old, other}}, &policy.Config{Exceptions: []policy.Exception{replacement, other}})
	for _, row := range []struct {
		finding  policy.Finding
		expected bool
	}{
		{policy.Finding{Path: "Justfile", Exception: "first"}, true},
		{policy.Finding{Path: "Justfile", Exception: "other"}, false},
		{policy.Finding{Path: "Justfile", Upstream: &policy.Span{Start: 23, End: 23}}, true},
		{policy.Finding{Path: "Justfile", Upstream: &policy.Span{Start: 27, End: 28}}, false},
	} {
		if actual := changes.affects(row.finding); actual != row.expected {
			t.Fatal(row.finding, actual, row.expected)
		}
	}
}
