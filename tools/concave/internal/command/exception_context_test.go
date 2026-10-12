package command_test

import (
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
)

func TestContractReviewExplainsWitnessBindingChangesFromTheirOwnSnapshot(t *testing.T) {
	fixture := contractFixture(t)
	selected, witness := "crates/core/lib.rs", "tests/witness.rs"
	fixture.Write(witness, "fn verify() {}\n")
	fixture.Config.Upstream = fixture.Commit()
	fixture.Write(selected, "// concave:begin adapter\n// why: Exercise the native adapter.\n// witness: tests/witness.rs::verify\nfn first() {}\n// concave:end adapter\nfn second() {}\n")
	source := "// concave:begin witness-retry\n// why: Retain the previous retry guarantee.\n// witness: tests/witness.rs::verify\nfn verify() {}\n// concave:end witness-retry\n"
	fixture.Write(witness, source)
	annotations, _ := policy.Annotations(witness, source)
	fixture.Config.Exceptions = []policy.Exception{{ID: "witness-retry", Path: witness, Fence: true, End: 1, UpstreamSHA256: repository.Digest([]byte("fn verify() {}\n")), ReplacementSHA256: annotations["witness-retry"].CodeSHA256()}}
	fixture.Save()
	fixture.Commit()
	// Fence bindings cover code only: change the code with its explanation.
	changed := strings.Replace(strings.Replace(source, "Retain the previous retry guarantee.", "Use the changed retry guarantee.", 1), "fn verify() {}\n// concave:end", "fn verify() { retry(); }\n// concave:end", 1)
	fixture.Write(witness, changed)
	annotations, _ = policy.Annotations(witness, changed)
	fixture.Config.Exceptions[0].ReplacementSHA256 = annotations["witness-retry"].CodeSHA256()
	fixture.Save()
	before, index := fixture.Git("diff", "--"), fixture.Git("ls-files", "--stage")
	packet := contractReview(t, fixture, selected)
	found := false
	for _, change := range packet.Changes {
		if change.Kind != "exception_binding_changed" || change.Path != witness {
			continue
		}
		found = true
		old := change.Before.(map[string]any)
		next := change.After.(map[string]any)
		if old["explanation"].(map[string]any)["why"] != "Retain the previous retry guarantee." || next["explanation"].(map[string]any)["why"] != "Use the changed retry guarantee." {
			t.Fatal(change)
		}
	}
	if !found {
		t.Fatal("witness binding omitted", packet.Changes)
	}
	code, text, stderr := run(fixture, "inspect", "queue", "--contracts", selected, "--top", "0")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	for _, want := range []string{"Before exception witness-retry at " + witness, "Retain the previous retry guarantee.", "After exception witness-retry at " + witness, "Use the changed retry guarantee.", "witness: tests/witness.rs::verify (not executed)"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if fixture.Git("diff", "--") != before || fixture.Git("ls-files", "--stage") != index {
		t.Fatal("review edited source or index")
	}
	fixture.Write(witness, "fn verify() {}\n")
	code, text, stderr = run(fixture, "inspect", "queue", "--contracts", selected, "--top", "0")
	if code != 0 || !strings.Contains(text, "rationale unavailable: named fence absent from captured source") || !strings.Contains(text, "Retain the previous retry guarantee.") || strings.Contains(text, "Use the changed retry guarantee.") {
		t.Fatal("missing fence acquired a rationale", code, text, stderr)
	}
}
