package analysis_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestFindingIdentityIsRuleAndObligation(t *testing.T) {
	finding := policy.Finding{Kind: "exact_file_changed", Rule: "source/exact", Path: "a.rs", Line: 3, EndLine: 4, Upstream: &policy.Span{Start: 2, End: 3}, Subject: "replacement", Message: "file must remain exactly upstream"}
	same := finding
	same.Kind, same.Message, same.Line, same.EndLine = "exact_file_changed", "reworded diagnostic", 40, 41
	if same.Identity() != finding.Identity() {
		t.Fatal("wording, kind or selected lines changed the identity")
	}
	// Upstream coordinates move with a merge; the subject names the content.
	same.Upstream = &policy.Span{Start: 12, End: 13}
	if same.Identity() != finding.Identity() {
		t.Fatal("upstream coordinates changed the identity")
	}
	for _, other := range []policy.Finding{
		{Rule: "source/covered", Path: "a.rs", Upstream: &policy.Span{Start: 2, End: 3}, Subject: "replacement"},
		{Rule: "source/exact", Path: "a.rs", Upstream: &policy.Span{Start: 2, End: 3}, Subject: "rewritten"},
		{Rule: "source/exact", Path: "a.rs", Upstream: &policy.Span{Start: 2, End: 3}, Exception: "binding", Subject: "replacement"},
		{Rule: "source/exact", Path: "b.rs", Upstream: &policy.Span{Start: 2, End: 3}, Subject: "replacement"},
	} {
		if other.Identity() == finding.Identity() {
			t.Fatalf("distinct obligation shares identity: %+v", other)
		}
	}
	if (policy.Finding{Rule: "source/covered", Path: "a.rs", Upstream: &policy.Span{Start: 2, End: 3}}).Location() != finding.Location() {
		t.Fatal("location depends on rule or subject")
	}
	upstream := []byte("a\nb\nc\nd\n")
	shifted := []byte("x\ny\na\nb\nc\nd\n")
	subject := policy.HunkSubject(upstream, 1, 2, "B\n")
	if policy.HunkSubject(shifted, 3, 4, "B\n") != subject {
		t.Fatal("shifted upstream lines changed the subject")
	}
	for _, other := range []string{policy.HunkSubject(upstream, 1, 2, "C\n"), policy.HunkSubject([]byte("a\nb\nC\nd\n"), 1, 2, "B\n"), policy.HunkSubject(upstream, 2, 2, "B\n")} {
		if other == subject {
			t.Fatal("replacement, adjacent upstream or position within it ignored")
		}
	}
}

// parityFixture pins an eight-line upstream file so divergences have distinct
// spans. Upstream advances on its own line of history and merges into the fork.
func parityFixture(t *testing.T) (*testrepo.Fixture, string) {
	f := testrepo.New(t)
	advanceUpstream(f, f.Base, upstreamSource)
	f.Commit()
	return f, sourcePath
}

// advanceUpstream merges a new upstream revision of sourcePath, pins it in the
// policy and leaves the merge for the caller to commit.
func advanceUpstream(f *testrepo.Fixture, from, source string) string {
	f.Git("checkout", "-q", "--detach", from)
	f.Write(sourcePath, source)
	upstream := f.Commit()
	f.Git("checkout", "-q", "main")
	f.Git("merge", "-q", "--no-ff", "--no-commit", upstream)
	f.Config.Upstream = upstream
	f.Save()
	return upstream
}

const sourcePath = "crates/core/lib.rs"
const upstreamSource = "fn one() {}\nfn two() {}\nfn three() {}\nfn four() {}\nfn five() {}\nfn six() {}\nfn seven() {}\nfn eight() {}\n"

func edit(source string, replacements ...string) string {
	for i := 0; i < len(replacements); i += 2 {
		source = strings.Replace(source, replacements[i], replacements[i+1], 1)
	}
	return source
}

func parity(t *testing.T, f *testrepo.Fixture, baseline, candidate string) *analysis.Parity {
	t.Helper()
	repo, _ := f.Open()
	result, err := analysis.CheckParity(repo, analysis.ParityOptions{Baseline: baseline, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if (result.Status == "passed") != (len(result.Regressions) == 0 && len(result.CoverageLoss) == 0) {
		t.Fatalf("status disagrees with verdict: %+v", result)
	}
	return result
}

func TestParityKeepsDebtAcrossShiftedLines(t *testing.T) {
	f, path := parityFixture(t)
	f.Write(path, edit(upstreamSource, "fn two() {}\n", "fn two() {}\n// a\n// b\n// c\n", "fn seven() {}", "fn seven() { fork(); }"))
	baseline := f.Commit()
	// Restoring the insertion shifts the remaining divergence by three lines.
	f.Write(path, edit(upstreamSource, "fn seven() {}", "fn seven() { fork(); }"))
	result := parity(t, f, baseline, f.Commit())
	if result.Status != "passed" || result.Carried != 1 || result.Resolved != 1 || result.Scope.Repository || len(result.Scope.Paths) != 1 {
		t.Fatalf("shifted debt re-attributed: %+v", result)
	}
}

func TestParityCarriesDebtAcrossAnUpstreamMerge(t *testing.T) {
	f, path := parityFixture(t)
	f.Write(path, edit(upstreamSource, "fn seven() {}", "fn seven() { fork(); }"))
	baseline := f.Commit()
	advanceUpstream(f, f.Config.Upstream, edit(upstreamSource, "fn two() {}", "fn two() { upstream(); }"))
	result := parity(t, f, baseline, f.Commit())
	if result.Status != "passed" || result.Carried != 1 || result.Scope.Reasons[0] != "upstream" {
		t.Fatalf("merge re-attributed untouched debt: %+v", result)
	}
}

func TestParityRefusesAReplacementWithEqualCounts(t *testing.T) {
	f, path := parityFixture(t)
	f.Write(path, edit(upstreamSource, "fn two() {}", "fn two() { fork(); }"))
	baseline := f.Commit()
	f.Write(path, edit(upstreamSource, "fn five() {}", "fn five() { fork(); }"))
	result := parity(t, f, baseline, f.Commit())
	if result.Status != "failed" || len(result.Regressions) != 1 || result.Regressions[0].Line != 5 || result.Resolved != 1 || result.Carried != 0 {
		t.Fatalf("replacement hidden by equal counts: %+v", result)
	}
	if result.Next.Kind != "inspect" || !strings.Contains(strings.Join(result.Next.Argv, " "), path+":5") {
		t.Fatalf("next action does not locate the regression: %+v", result.Next)
	}
	// Rewriting an existing replacement is a new unreviewed change.
	f.Write(path, edit(upstreamSource, "fn two() {}", "fn two() { other(); }"))
	if result = parity(t, f, baseline, f.Commit()); len(result.Regressions) != 1 || result.Regressions[0].Line != 2 {
		t.Fatalf("rewritten replacement carried: %+v", result)
	}
}

func TestParityJudgesPolicyChangesWithTheSameRule(t *testing.T) {
	f := testrepo.New(t)
	path := "crates/core/lib.rs"
	f.Write(path, "fn first() { changed(); }\nfn second() {}\n")
	baseline := f.Commit()
	f.Config.PreserveExactly = append(f.Config.PreserveExactly, path)
	f.Save()
	stricter := f.Commit()
	result := parity(t, f, baseline, stricter)
	// The stricter rule finds the existing difference: discovered debt, not a
	// regression, and the weaker rule's disappearance is no coverage loss.
	if result.Status != "passed" || len(result.DiscoveredDebt) != 1 || result.DiscoveredDebt[0].Rule != "source/exact" || !result.Scope.Repository || result.Scope.Reasons[0] != "policy" {
		t.Fatalf("expanded coverage: %+v", result)
	}
	// The same policy change with a simultaneous source change is a regression.
	f.Config.PreserveExactly = f.Config.PreserveExactly[:len(f.Config.PreserveExactly)-1]
	f.Save()
	f.Commit()
	f.Config.PreserveExactly = append(f.Config.PreserveExactly, path)
	f.Save()
	f.Write(path, "fn first() { rewritten(); }\nfn second() {}\n")
	if result = parity(t, f, baseline, f.Commit()); result.Status != "failed" || len(result.Regressions) != 1 {
		t.Fatalf("source edit hidden as discovered debt: %+v", result)
	}
	// Removing the stricter requirement is coverage loss, even with no new finding.
	f.Config.PreserveExactly = nil
	f.Save()
	f.Write(path, "fn first() { changed(); }\nfn second() {}\n")
	result = parity(t, f, stricter, f.Commit())
	if result.Status != "failed" || len(result.CoverageLoss) == 0 || len(result.Regressions) != 0 {
		t.Fatalf("weakened policy accepted: %+v", result)
	}
	if result.Next.Kind != "inspect" || result.Next.Argv[len(result.Next.Argv)-1] != result.CoverageLoss[0].Path {
		t.Fatalf("coverage loss has no next action: %+v", result.Next)
	}
}

func TestParityDocumentDebtFollowsItsOwnDependencies(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	f.Config.Roots = append(f.Config.Roots, "apps")
	f.Save()
	f.Write("apps/docs/a.mdx", "import P from './missing.mdx';\n\n<P />\n")
	f.Write("apps/docs/b.mdx", "Plain page.\n")
	f.Write("apps/docs/c.mdx", "import P from './_partial.mdx';\n\n<P />\n")
	f.Write("apps/docs/_partial.mdx", "import Q from './absent.mdx';\n\n<Q />\n")
	original := map[string]string{"apps/docs/a.mdx": "import P from './missing.mdx';\n\n<P />\n", "apps/docs/b.mdx": "Plain page.\n", "apps/docs/_partial.mdx": "import Q from './absent.mdx';\n\n<Q />\n"}
	baseline := f.Commit()
	measured := func(edits map[string]string) *analysis.Parity {
		f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}}
		f.Save()
		for path, text := range edits {
			f.Write(path, text)
		}
		result := parity(t, f, baseline, f.Commit())
		f.Config.Documents = nil
		f.Save()
		for path := range edits {
			f.Write(path, original[path])
		}
		f.Commit()
		return result
	}
	// Expanded document coverage plus an unrelated document edit: existing
	// problems keep their own dependency identities and remain discovered debt.
	result := measured(map[string]string{"apps/docs/b.mdx": "Edited page.\n"})
	if result.Status != "passed" || len(result.DiscoveredDebt) != 2 {
		t.Fatalf("unrelated document edit re-attributed debt: %+v", result)
	}
	// Editing the page, or a partial it imports, changes that page's identity.
	result = measured(map[string]string{"apps/docs/a.mdx": "import P from './missing.mdx';\n\nEdited <P />\n"})
	if result.Status != "failed" || len(result.Regressions) != 1 || result.Regressions[0].Path != "apps/docs/a.mdx" {
		t.Fatalf("edited document debt hidden: %+v", result)
	}
	result = measured(map[string]string{"apps/docs/_partial.mdx": "import Q from './absent.mdx';\n\nEdited <Q />\n"})
	if result.Status != "failed" || len(result.Regressions) != 1 || result.Regressions[0].Path != "apps/docs/_partial.mdx" {
		t.Fatalf("edited partial debt hidden: %+v", result)
	}
}

func TestParityUsesTheBaselineToolForCoverage(t *testing.T) {
	f, path := parityFixture(t)
	f.Write(path, edit(upstreamSource, "fn two() {}", "fn two() { fork(); }"))
	baseline := f.Commit()
	f.Write("packaging/other.ts", "export const other = 1;\n")
	candidate := f.Commit()
	repo, config := f.Open()
	before, err := analysis.Check(repo, config, analysis.CheckOptions{Rev: baseline})
	if err != nil {
		t.Fatal(err)
	}
	tool := func(report *analysis.Report, code int) string {
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		file := filepath.Join(dir, "report.json")
		script := filepath.Join(dir, "concave")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("#!/bin/sh\ncat '"+file+"'\nexit "+string(rune('0'+code))+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return script
	}
	check := func(script string) (*analysis.Parity, error) {
		return analysis.CheckParity(repo, analysis.ParityOptions{Baseline: baseline, Candidate: candidate, BaselineTool: script})
	}
	result, err := check(tool(before, 1))
	if err != nil || result.Status != "passed" || !result.Scope.Repository || result.Scope.Reasons[0] != "concave" || result.Carried != len(before.Findings) {
		t.Fatal(result, err)
	}
	// A detection the baseline's own Concave made, and this one does not, is lost.
	lost := *before
	lost.Findings = append(append([]policy.Finding{}, before.Findings...), policy.Finding{Kind: "invalid_contract", Rule: "contract/valid", Path: "crates/core/lib.rs", Line: 1, Message: "older detector"})
	if result, err = check(tool(&lost, 1)); err != nil || result.Status != "failed" || len(result.CoverageLoss) != 1 || len(result.Regressions) != 0 {
		t.Fatal(result, err)
	}
	// The baseline report must describe that revision under its own policy.
	wrong := *before
	wrong.Revision = candidate
	if _, err = check(tool(&wrong, 1)); err == nil {
		t.Fatal("baseline report for another revision accepted")
	}
	if _, err = check(tool(before, 2)); err == nil {
		t.Fatal("failed baseline check accepted")
	}
}

// A transformation that explains one hunk of a file still left unexplained by
// another approves that divergence: it is resolved, not lost coverage. A
// hunk that merely stops being detected remains coverage loss.
func TestParityAcceptsAHunkTheCandidatePolicyApproves(t *testing.T) {
	f, path := parityFixture(t)
	f.Write(path, edit(upstreamSource, "fn two() {}", "fn two_renamed() {}", "fn seven() {}", "fn seven() { fork(); }"))
	baseline := f.Commit()
	f.Config.Transforms = []policy.Transform{{ID: "rename-two", Paths: []string{path}, Replacements: []policy.Replacement{{From: "fn two() {}", To: "fn two_renamed() {}"}}, Explanation: policy.Explanation{Why: "Use the product name.", Witnesses: []string{path}}}}
	f.Save()
	result := parity(t, f, baseline, f.Commit())
	if result.Status != "passed" || len(result.CoverageLoss) != 0 || result.Carried != 1 {
		t.Fatalf("approved hunk reported as lost coverage: %+v", result)
	}
}
