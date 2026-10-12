package command_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
)

func TestContractReviewScopesExceptionsAfterResolvingDependencies(t *testing.T) {
	fixture := contractFixture(t)
	selected := "crates/core/lib.rs"
	caller := "npm-packages/caller.rs"
	witness := "tests/witness.rs"
	outside := "npm-packages/other.rs"
	fixtures := []struct{ id, path, before, after string }{
		{"selected", selected, "// concave:invariant first-result\n// Keep the result stable.\nfn first() { return 1; }\n", "// concave:invariant first-result\n// Keep the result stable.\nfn first() { return 2; }\n"},
		{"caller", caller, "fn caller() { first(); }\n", "fn caller() { first(); local(); }\n"},
		{"witness", witness, "fn verify() {}\n", "fn verify() { assert!(true); }\n"},
		{"outside", outside, "fn other() {}\n", "fn other() { local(); }\n"},
	}
	for _, source := range fixtures {
		fixture.Write(source.path, source.before)
	}
	fixture.Config.Upstream = fixture.Commit()
	fixture.Save()
	fixture.Commit()
	baselinePolicy, err := os.ReadFile(filepath.Join(fixture.Root, policy.File))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range fixtures {
		fixture.Write(source.path, source.after)
		fixture.Config.Exceptions = append(fixture.Config.Exceptions, policy.Exception{
			ID: source.id, Path: source.path, Start: len(repository.Lines(source.before)) - 1, End: len(repository.Lines(source.before)),
			UpstreamSHA256: repository.Digest([]byte(source.before)), ReplacementSHA256: repository.Digest([]byte(repository.Lines(source.after)[len(repository.Lines(source.after))-1])),
			Reason: "Exercise scoped review context.", Witnesses: []string{witness},
		})
	}
	fixture.Save()
	packet := contractReview(t, fixture, selected)
	if len(packet.Candidate.Findings) != 0 {
		t.Fatal(packet.Candidate.Findings)
	}
	relevant := map[string]bool{selected: false, caller: false, witness: false}
	for _, change := range packet.Changes {
		if !strings.HasPrefix(change.Kind, "exception_") {
			continue
		}
		if _, ok := relevant[change.Path]; !ok {
			t.Fatal("unrelated exception entered detailed changes", change)
		}
		relevant[change.Path] = true
	}
	for path, found := range relevant {
		if !found {
			t.Fatal("selected source, lexical caller or witness exception missing", path)
		}
	}
	if packet.ExcludedExceptionChanges != 1 {
		t.Fatal("missing excluded change count", packet.ExcludedExceptionChanges)
	}
	candidatePolicy, err := os.ReadFile(filepath.Join(fixture.Root, policy.File))
	if err != nil {
		t.Fatal(err)
	}
	retained := map[string]string{}
	for _, source := range packet.Sources {
		if source.Path == policy.File {
			retained[source.Version] = packet.Blobs[source.SHA256]
		}
	}
	if retained["baseline"] != string(baselinePolicy) || retained["candidate"] != string(candidatePolicy) {
		t.Fatal("scoping discarded full policy bytes")
	}
	code, text, stderr := run(fixture, "inspect", "queue", "--contracts", selected)
	if code != 0 || !strings.Contains(text, "Exception changes outside review context: 1; full policy retained.") || strings.Contains(text, "exception_coverage_expanded "+outside) {
		t.Fatal(code, text, stderr)
	}
	full := contractReview(t, fixture)
	if full.ExcludedExceptionChanges != 0 {
		t.Fatal("full review lost an exception", full.ExcludedExceptionChanges)
	}
	found := false
	for _, change := range full.Changes {
		found = found || change.Kind == "exception_coverage_expanded" && change.Path == outside
	}
	if !found {
		t.Fatal("full review omitted outside exception")
	}
	fixture.Config.Exceptions[3].Reason = "Changed outside the selected context."
	fixture.Save()
	changed := contractReview(t, fixture, selected)
	if changed.ExcludedExceptionChanges != 1 || packet.SnapshotSHA256 == changed.SnapshotSHA256 {
		t.Fatal("outside policy change escaped evidence identity")
	}
}
