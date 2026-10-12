package policy_test

import (
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
)

// Upstream "one\ntwo\n" with a fenced insertion between the lines. The binding
// records the code digest of the first source; every other source is checked
// against that same binding.
func classifyFence(t *testing.T, approved, candidate string) (string, []policy.Finding) {
	t.Helper()
	before := []byte("one\ntwo\n")
	wrap := func(fence string) string { return "one\n" + fence + "two\n" }
	annotations, bad := policy.Annotations("core.rs", wrap(approved))
	if len(bad) != 0 {
		t.Fatal(bad)
	}
	c := policy.Config{Exceptions: []policy.Exception{{ID: "hook", Path: "core.rs", Start: 1, End: 1, UpstreamSHA256: repository.Digest(before), ReplacementSHA256: annotations["hook"].CodeSHA256(), Fence: true}}}
	inserted := repository.Hunk{Start: 1, End: 1, CurrentStart: 1, CurrentEnd: 1 + len(repository.Lines(candidate)), After: candidate}
	class, _, findings := c.Classify("core.rs", before, []byte(wrap(candidate)), []repository.Hunk{inserted})
	return class, findings
}

// The rustfmt pass in this repository (wrap_comments, comment_width 80)
// re-flowed this metadata, split references at punctuation and inserted a
// space where it rejoined them, and re-indented the end marker.
const approvedFence = `    // concave:begin hook
    // why: While a governing or private read rule runs, a file URL is engine system state
    // rather than a rule input, so it is refused before anything is read.
    //
    // witness: crates/application/tests/access_custody/governing_component_tests.rs::postgres_governing_rule_
    // cannot_read_system_state
    //
    // preserves: crates/application/src/governing.rs#rule-
    // reads-are-confined
    //
    // remove-when: Upstream refuses system state inside rules.

    // A file's URL reads the canonical origin.
    hook();
    // concave:end hook
`
const rewrappedFence = `    // concave:begin hook
    // why: While a governing or private read rule runs, a file URL is engine system
    // state rather than a rule input, so it is refused before anything is
    // read.
    //
    // witness: crates/application/tests/access_custody/governing_component_tests.
    // rs::postgres_governing_rule_ cannot_read_system_state
    //
    // preserves: crates/application/src/governing.
    // rs#rule- reads-are-confined
    //
    // remove-when: Upstream refuses system state
    // inside rules.

    // A file's URL reads the canonical origin.
    hook();
        // concave:end hook
`

func TestRewrappedFenceMetadataKeepsBindingAndExplanation(t *testing.T) {
	class, findings := classifyFence(t, approvedFence, rewrappedFence)
	if class != "excepted" || len(findings) != 0 {
		t.Fatal(class, findings)
	}
	approved, _ := policy.Annotations("core.rs", approvedFence)
	rewrapped, bad := policy.Annotations("core.rs", rewrappedFence)
	a, b := approved["hook"], rewrapped["hook"]
	if len(bad) != 0 || a.Code != b.Code || a.Code != "    // A file's URL reads the canonical origin.\n    hook();\n" {
		t.Fatal(bad, a.Code, b.Code)
	}
	want := policy.Explanation{
		Why:        "While a governing or private read rule runs, a file URL is engine system state rather than a rule input, so it is refused before anything is read.",
		Witnesses:  []string{"crates/application/tests/access_custody/governing_component_tests.rs::postgres_governing_rule_cannot_read_system_state"},
		Preserves:  []string{"crates/application/src/governing.rs#rule-reads-are-confined"},
		RemoveWhen: "Upstream refuses system state inside rules.",
	}
	for _, got := range []policy.Explanation{a.Explanation, b.Explanation} {
		if got.Why != want.Why || got.RemoveWhen != want.RemoveWhen || strings.Join(got.Witnesses, ",") != want.Witnesses[0] || strings.Join(got.Preserves, ",") != want.Preserves[0] {
			t.Fatalf("%#v", got)
		}
	}
	// Changing the explanation is a source edit, not a code edit.
	reworded := strings.Replace(rewrappedFence, "refused before anything is", "refused before any byte is", 1)
	if class, findings := classifyFence(t, approvedFence, reworded); class != "excepted" || len(findings) != 0 {
		t.Fatal(class, findings)
	}
}

func TestSplitReferenceWithInsertedWhitespaceResolves(t *testing.T) {
	for _, path := range []string{"x.rs", "Cargo.toml"} {
		text := "// concave:begin read\n// why: keep it\n// witness: other/file.\n// rs::tests::read_ all_\n// rows\n// witness:\n// tests::\n// read\nread();\n// concave:end read\n"
		if path == "Cargo.toml" {
			text = strings.ReplaceAll(text, "//", "#")
		}
		annotations, bad := policy.Annotations(path, text)
		if got := strings.Join(annotations["read"].Explanation.Witnesses, ","); len(bad) != 0 || got != "other/file.rs::tests::read_all_rows,tests::read" {
			t.Fatal(path, got, bad)
		}
	}
}

func TestMetadataBoundaryWithoutBlankSeparator(t *testing.T) {
	const metadata = "// concave:begin hook\n// why: keep the hook\n// witness: tests::hook\n//\n"
	const code = "// The hook runs first.\nhook();\n"
	blank := metadata + "\n" + code + "// concave:end hook\n"
	removed := metadata + code + "// concave:end hook\n"
	for name, text := range map[string]string{"blank": blank, "removed": removed} {
		annotations, bad := policy.Annotations("core.rs", text)
		a := annotations["hook"]
		if len(bad) != 0 || a.Code != code || a.Explanation.Why != "keep the hook" || strings.Join(a.Explanation.Witnesses, ",") != "tests::hook" {
			t.Fatal(name, a, bad)
		}
	}
	if class, findings := classifyFence(t, blank, removed); class != "excepted" || len(findings) != 0 {
		t.Fatal(class, findings)
	}
	// A blank line alone also ends metadata; leading blank lines are not code.
	annotations, bad := policy.Annotations("core.rs", "// concave:begin hook\n// why: keep the hook\n// witness: tests::hook\n\n\n"+code+"// concave:end hook\n")
	if len(bad) != 0 || annotations["hook"].Code != code {
		t.Fatal(annotations, bad)
	}
}

func TestCodeChangeInsideFenceStalesBinding(t *testing.T) {
	for name, candidate := range map[string]string{
		"statement":    strings.Replace(rewrappedFence, "hook();", "bypass();", 1),
		"body comment": strings.Replace(rewrappedFence, "canonical origin", "configured origin", 1),
		"added line":   strings.Replace(rewrappedFence, "    hook();\n", "    hook();\n    again();\n", 1),
	} {
		class, findings := classifyFence(t, approvedFence, candidate)
		stale := false
		for _, f := range findings {
			stale = stale || f.Kind == "stale_exception" && f.Exception == "hook"
		}
		if class != "unexplained" || !stale {
			t.Fatal(name, class, findings)
		}
	}
}

func TestWrappedProseAndMergedFields(t *testing.T) {
	text := "// concave:begin read\n// why: the scanner keeps two\n// modes: fast and slow\n// witness: tests::read\nread();\n// concave:end read\n"
	annotations, bad := policy.Annotations("x.rs", text)
	if len(bad) != 0 || annotations["read"].Explanation.Why != "the scanner keeps two modes: fast and slow" {
		t.Fatal(annotations, bad)
	}
	// Without a separator, re-flowing can push a label into the previous
	// paragraph. That field would be lost, so the fence is refused.
	merged := "// concave:begin read\n// why: keep the read\n// witness: tests::read\n// ordered. witness: tests::other\nread();\n// concave:end read\n"
	if _, bad := policy.Annotations("x.rs", merged); len(bad) == 0 || !strings.Contains(bad[0].Message, "wrapped into the preceding text") {
		t.Fatal(bad)
	}
	merged = "// concave:begin read\n// why: keep the read in order witness: tests::other\n// witness: tests::read\nread();\n// concave:end read\n"
	if _, bad := policy.Annotations("x.rs", merged); len(bad) == 0 {
		t.Fatal("merged field accepted")
	}
}

// rustfmt's comment wrapping moves a long fence ID onto its own comment line.
// The marker keeps its binding and stripped bytes; the ID line holds exactly
// one ID, so a wrapped label or prose is still an invalid marker.
func TestWrappedMarkerIDsKeepTheirFence(t *testing.T) {
	wrapped := strings.Replace(strings.Replace(approvedFence, "    // concave:begin hook\n", "    // concave:begin\n    // hook\n", 1), "    // concave:end hook\n", "    // concave:end\n    // hook\n", 1)
	class, findings := classifyFence(t, approvedFence, wrapped)
	if class != "excepted" || len(findings) != 0 {
		t.Fatal(class, findings)
	}
	approved, _ := policy.Annotations("core.rs", approvedFence)
	got, bad := policy.Annotations("core.rs", wrapped)
	if len(bad) != 0 || got["hook"].Code != approved["hook"].Code || got["hook"].Explanation.Why != approved["hook"].Explanation.Why {
		t.Fatal(bad, got)
	}
	plain, _ := policy.AnnotationFree("core.rs", approvedFence)
	stripped, ok := policy.AnnotationFree("core.rs", wrapped)
	if !ok || stripped != plain {
		t.Fatalf("%q\n%q", stripped, plain)
	}
	for _, next := range []string{"    // hook extra\n", "    // why: x\n", "    hook();\n"} {
		broken := strings.Replace(approvedFence, "    // concave:begin hook\n", "    // concave:begin\n"+next, 1)
		if _, bad := policy.Annotations("core.rs", broken); len(bad) == 0 || bad[0].Message != "expected a standalone concave:begin|end ID comment" {
			t.Fatal(next, bad)
		}
	}
}
