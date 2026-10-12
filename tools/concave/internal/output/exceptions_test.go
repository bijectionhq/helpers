package output

import (
	"bytes"
	"strings"
	"testing"

	"concave/internal/exceptions"
)

func TestExceptionHistoryTextPreservesEvidenceMeaning(t *testing.T) {
	diff := "diff --git a/source.rs b/source.rs\n--- a/source.rs\n+++ b/source.rs\n@@ -1 +1 @@\n-old();\n+changed();\n"
	for _, test := range []struct {
		name, status, diff, want string
		fence                    bool
	}{
		{"changed", "recovered", diff, "Approved → current body:", true},
		{"removed", "recovered", diff, "Approved → empty body (current fence absent):", false},
		{"unchanged", "recovered", "", "Approved body is unchanged; binding validity is reported separately.", true},
		{"unavailable", "unavailable", "", "Previously approved source: unavailable", true},
		{"bounded", "search_byte_limit", "", "Previously approved source: search_byte_limit", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &exceptions.PreviousSource{Status: test.status, Diff: test.diff, CurrentFence: test.fence, Examined: 2, Limit: 64}
			if test.status == "recovered" {
				source.Commit, source.Blob = "approved-commit", "approved-blob"
			}
			var explanation, proposal bytes.Buffer
			if err := ExceptionExplanation(&explanation, &exceptions.Explanation{ID: "read", Status: "stale", PreviousSource: source}); err != nil {
				t.Fatal(err)
			}
			if err := ExceptionProposal(&proposal, &exceptions.Proposal{Changes: []*exceptions.Change{{ID: "read", PreviousSource: source}}}, nil); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{explanation.String(), proposal.String()} {
				if !strings.Contains(text, test.want) || !strings.Contains(text, "examined 2 historical blobs (limit 64 commits)") {
					t.Fatal(text)
				}
				if test.diff != "" && (!strings.Contains(text, test.diff) || strings.Contains(text, `\n-old();`)) {
					t.Fatal("diff was escaped or changed", text)
				}
				if test.status != "recovered" && (strings.Contains(text, "Approved at") || strings.Contains(text, "body is unchanged")) {
					t.Fatal("unavailable history became a comparison", text)
				}
				if test.status == "recovered" && !strings.Contains(text, "Approved at approved-commit (blob approved-blob)") {
					t.Fatal("source provenance omitted", text)
				}
			}
		})
	}
}
