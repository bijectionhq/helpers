package analysis_test

import (
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
)

// Retiring a binding, stale or valid, resolves debt only when the candidate's
// own source for its span is upstream-identical or otherwise approved. Retiring
// it while the divergence remains is coverage loss, though the divergence alone
// is carried.
func TestParityRetiredBindingNeedsItsSpanExplained(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "stale"}[stale], func(t *testing.T) {
			f, path := parityFixture(t)
			f.Config.Exceptions = []policy.Exception{{ID: "fork-two", Path: path, UpstreamSHA256: repository.Digest([]byte(upstreamSource)), Start: 1, End: 2,
				ReplacementSHA256: repository.Digest([]byte("fn two() { fork(); }\n")), Reason: "fixture", Witnesses: []string{path}}}
			f.Save()
			replacement := "fn two() { fork(); }"
			if stale {
				replacement = "fn two() { drifted(); }"
			}
			diverged := edit(upstreamSource, "fn two() {}", replacement, "fn seven() {}", "fn seven() { kept(); }")
			f.Write(path, diverged)
			baseline := f.Commit()
			f.Config.Exceptions = nil
			f.Save()
			retired := f.Commit()
			result := parity(t, f, baseline, retired)
			if result.Status != "failed" || len(result.CoverageLoss) != 1 || !strings.Contains(result.CoverageLoss[0].Reason, "fork-two retired") {
				t.Fatalf("retirement without restoration accepted: %+v", result)
			}
			f.Write(path, edit(upstreamSource, "fn seven() {}", "fn seven() { kept(); }"))
			result = parity(t, f, baseline, f.Commit())
			if result.Status != "passed" || len(result.CoverageLoss) != 0 || result.Carried != 1 {
				t.Fatalf("retirement with restoration refused: %+v", result)
			}
			// Another approval of the same span also explains it.
			f.Write(path, diverged)
			f.Config.Exceptions = []policy.Exception{{ID: "fork-two-renamed", Path: path, UpstreamSHA256: repository.Digest([]byte(upstreamSource)), Start: 1, End: 2,
				ReplacementSHA256: repository.Digest([]byte(replacement + "\n")), Reason: "fixture", Witnesses: []string{path}}}
			f.Save()
			if result = parity(t, f, baseline, f.Commit()); len(result.CoverageLoss) != 0 {
				t.Fatalf("span approved by another binding counted as lost: %+v", result)
			}
		})
	}
}

// Reverting a refreshed binding makes it stale on unchanged source: the
// contribution caused that finding, so it is a regression, never discovered
// debt (the 9bbbe7ac15 case).
func TestParityRefusesAReversionOfARefreshedBinding(t *testing.T) {
	f, path := parityFixture(t)
	binding := func(replacement string) policy.Exception {
		return policy.Exception{ID: "fork-two", Path: path, UpstreamSHA256: repository.Digest([]byte(upstreamSource)), Start: 1, End: 2,
			ReplacementSHA256: repository.Digest([]byte(replacement)), Reason: "fixture", Witnesses: []string{path}}
	}
	f.Config.Exceptions = []policy.Exception{binding("fn two() { fork(); }\n")}
	f.Save()
	f.Write(path, edit(upstreamSource, "fn two() {}", "fn two() { fork(); }"))
	f.Commit()
	// The refresh: new source and its binding, published together.
	f.Config.Exceptions = []policy.Exception{binding("fn two() { refreshed(); }\n")}
	f.Save()
	f.Write(path, edit(upstreamSource, "fn two() {}", "fn two() { refreshed(); }"))
	refreshed := f.Commit()
	if result := parity(t, f, refreshed, refreshed); result.Status != "passed" {
		t.Fatal(result)
	}
	// A later contribution carrying the old binding bytes reverts it.
	f.Config.Exceptions = []policy.Exception{binding("fn two() { fork(); }\n")}
	f.Save()
	result := parity(t, f, refreshed, f.Commit())
	if result.Status != "failed" || len(result.Regressions) == 0 || len(result.DiscoveredDebt) != 0 || result.Regressions[0].Exception != "fork-two" {
		t.Fatalf("reverted binding passed as discovered debt: %+v", result)
	}
}
