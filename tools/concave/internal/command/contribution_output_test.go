package command_test

import (
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestContributionOutputLeadsWithSharedVerdict(t *testing.T) {
	for _, state := range []string{"unchanged", "selection", "blocked"} {
		t.Run(state, func(t *testing.T) {
			f := testrepo.New(t)
			path := "crates/core/lib.rs"
			switch state {
			case "selection":
				path = "tools/helper.go"
				f.Write(path, "package helper\n")
			case "blocked":
				f.Write(path, "fn changed() {}\n")
			}
			code, text, stderr := run(f, "review", path, "--json")
			var record review.Contribution
			if code != 0 || json.Unmarshal([]byte(text), &record) != nil {
				t.Fatal(code, text, stderr)
			}
			code, text, stderr = run(f, "review", "status", record.ID, "--json")
			var verdict review.Verdict
			wantStatus := 0
			if state == "blocked" {
				wantStatus = 1
			}
			if code != wantStatus || json.Unmarshal([]byte(text), &verdict) != nil {
				t.Fatal(code, text, stderr)
			}
			code, text, stderr = run(f, "review", record.ID)
			if code != 0 || !strings.HasPrefix(text, verdict.Outcome.Headline()+"\nContribution: "+verdict.Contribution+"\n") || !strings.Contains(text, verdict.Next.Message) {
				t.Fatal(code, text, stderr)
			}
			if state == "unchanged" && (strings.Contains(text, "missing check:") || strings.Contains(text, "required checks") || strings.Contains(text, "Verification:")) {
				t.Fatal("empty contribution has no verification work", text)
			}
			if state == "blocked" && (!strings.Contains(text, "crates/core/lib.rs:1 [source/covered]") || !strings.Contains(text, "captured_scope")) {
				t.Fatal("finding lacks source location, rule or scope", text)
			}
		})
	}
}
