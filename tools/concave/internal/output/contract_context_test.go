package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
)

func TestContractReviewRationalesRetainScopeAndBoundPresentation(t *testing.T) {
	context := func(ids ...string) *analysis.ContractContext {
		file := analysis.FileContracts{Path: "core.rs"}
		for _, id := range ids {
			file.Exceptions = append(file.Exceptions, analysis.ScopedException{ID: id, Start: 5, End: 9, Explanation: policy.Explanation{
				Why: "Keep " + id + ".\nPreserve the original guarantee.", Witnesses: []string{"tests.rs::" + id}, Preserves: []string{"core.rs#safe"}, RemoveWhen: "Upstream owns " + id + ".",
			}})
		}
		return &analysis.ContractContext{Files: []analysis.FileContracts{file}}
	}
	packet := &analysis.ContractReview{Against: "baseline", Candidate: &analysis.Report{Revision: "candidate", Contracts: context("first", "second")}, Baseline: &analysis.Report{Contracts: context("first", "removed")}, CallerContext: context("first", "caller")}
	before, _ := json.Marshal(packet)
	var limited, full bytes.Buffer
	ContractReview(&limited, packet, 1)
	ContractReview(&full, packet, 0)
	for _, want := range []string{"candidate candidate", "0 changes against baseline", "Candidate rationale core.rs#first at core.rs:5-9", "3 more divergence rationales in --json (or use --top 0)"} {
		if !strings.Contains(limited.String(), want) {
			t.Fatal(want, limited.String())
		}
	}
	for _, absent := range []string{"Keep second.", "Keep caller.", "Keep removed."} {
		if strings.Contains(limited.String(), absent) {
			t.Fatal("limit hid no rationale", limited.String())
		}
	}
	for _, want := range []string{"Caller rationale core.rs#caller", "Baseline-only rationale core.rs#removed", "why: Keep removed.\n    Preserve the original guarantee.", "preserves: core.rs#safe", "witness: tests.rs::removed (not executed)", "remove-when: Upstream owns removed.", "explanations, not approvals"} {
		if !strings.Contains(full.String(), want) {
			t.Fatal(want, full.String())
		}
	}
	if strings.Count(full.String(), "rationale core.rs#first") != 1 || strings.Contains(full.String(), "more divergence rationales") {
		t.Fatal("duplicate context or false omission", full.String())
	}
	after, _ := json.Marshal(packet)
	if !bytes.Equal(before, after) {
		t.Fatal("presentation changed evidence")
	}
}
