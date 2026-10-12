package command_test

import (
	"strings"
	"testing"
)

func TestContractReviewTerminalKeepsPreviousObligation(t *testing.T) {
	for _, scenario := range []struct{ name, source, kind, after string }{
		{"changed", "// concave:invariant audience-binding\n// Accept every audience.\nfn authenticate() {}\n", "obligation_changed", "Accept every audience."},
		{"removed", "fn authenticate() {}\n", "obligation_removed", ""},
		{"reattached", "// concave:invariant audience-binding\n// Reject incompatible audiences.\nfn replacement() {}\n", "obligation_reattached", "function_item:replacement"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := contractFixture(t)
			fixture.Write("crates/core/adapter.rs", scenario.source)
			before := fixture.Git("diff", "--")
			code, text, stderr := run(fixture, "inspect", "queue", "--contracts", "crates/core/adapter.rs", "--top", "0")
			if code != 0 {
				t.Fatal(code, text, stderr)
			}
			for _, want := range []string{scenario.kind, "Before crates/core/adapter.rs:1 → function_item:authenticate", "Reject incompatible audiences.", "candidate working files on "} {
				if !strings.Contains(text, want) {
					t.Fatal(want, text)
				}
			}
			if scenario.after != "" && (!strings.Contains(text, "After crates/core/adapter.rs:1") || !strings.Contains(text, scenario.after)) {
				t.Fatal("candidate meaning hidden", text)
			}
			if scenario.name == "removed" && strings.Contains(text, "After crates/core/adapter.rs:1") {
				t.Fatal("removed obligation acquired a candidate", text)
			}
			if fixture.Git("diff", "--") != before {
				t.Fatal("review edited source")
			}
		})
	}
}

func TestContractReviewTerminalExplainsChangedAndRemovedFences(t *testing.T) {
	fixture := contractFixture(t)
	path := "crates/core/lib.rs"
	source := "// concave:begin checkpoint\n// why: Retain pending checkpoints across retries.\n// witness: crates/core/adapter.rs::authenticate\n// remove-when: Upstream retries checkpoints.\nfn first() {}\n// concave:end checkpoint\nfn second() {}\n"
	fixture.Write(path, source)
	code, text, stderr := run(fixture, "inspect", "queue", "--contracts", path, "--top", "0")
	if code != 0 || !strings.Contains(text, "exception_explanation_added") || !strings.Contains(text, "unknown_fence") {
		t.Fatal("unrecorded explanation was hidden or approved", code, text, stderr)
	}
	fixture.Commit()
	fixture.Write(path, strings.ReplaceAll(source, "Retain pending checkpoints across retries.", "Discard checkpoints on retry."))
	code, text, stderr = run(fixture, "inspect", "queue", "--contracts", path, "--top", "0")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	for _, want := range []string{"exception_explanation_changed", "Before:\n    why: Retain pending checkpoints across retries.", "After:\n    why: Discard checkpoints on retry.", "Candidate rationale " + path + "#checkpoint", "remove-when: Upstream retries checkpoints.", "unknown_fence"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	fixture.Write(path, "fn first() {}\nfn second() {}\n")
	code, text, stderr = run(fixture, "inspect", "queue", "--contracts", path, "--top", "0")
	if code != 0 || !strings.Contains(text, "exception_explanation_removed") || !strings.Contains(text, "Baseline-only rationale "+path+"#checkpoint") || !strings.Contains(text, "Retain pending checkpoints across retries.") || strings.Contains(text, "Candidate rationale "+path+"#checkpoint") {
		t.Fatal("removed explanation lost its baseline meaning", code, text, stderr)
	}
}
