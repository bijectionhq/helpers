package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/review"
	"concave/internal/testrepo"
)

func ownedCheck() policy.CheckDefinition {
	return policy.CheckDefinition{
		Paths:     []string{"packaging/owned/"},
		Command:   []string{"sh", "-c", `printf '%s\n' '{"Action":"pass","Package":"pkg","Test":"TestOwn"}'`},
		Directory: ".", Inputs: []string{"packaging/"},
		Toolchain:      []policy.ToolCheck{{Command: []string{"sh", "-c", "printf v1"}, Expected: "v1"}},
		TimeoutSeconds: 5, Results: "go-test-json",
		Witnesses: map[string][]string{"packaging/check_test.go": {"pkg::TestOwn"}},
	}
}

func TestOwnedSourceRequiresMappedChecks(t *testing.T) {
	for _, operation := range []string{"edit", "create", "delete"} {
		t.Run(operation, func(t *testing.T) {
			fixture := testrepo.New(t)
			path := "packaging/owned/value.go"
			if operation != "create" {
				fixture.Write(path, "package owned\nconst Value = 1\n")
			}
			fixture.Write("packaging/check_test.go", "package owned\n")
			fixture.Config.Checks = map[string]policy.CheckDefinition{"owned": ownedCheck()}
			fixture.Save()
			fixture.Commit()
			if operation == "delete" {
				if err := os.Remove(filepath.Join(fixture.Root, path)); err != nil {
					t.Fatal(err)
				}
			} else {
				fixture.Write(path, "package owned\nconst Value = 2\n")
			}
			// A parent-directory review must discover checks for contained owners.
			code, text, stderr := run(fixture, "review", "packaging/", "--json")
			var contribution review.Contribution
			if code != 0 || json.Unmarshal([]byte(text), &contribution) != nil || len(contribution.RequiredChecks) != 1 || contribution.RequiredChecks[0] != "owned" {
				t.Fatal(code, text, stderr)
			}
			code, text, stderr = run(fixture, "review", "status", contribution.ID, "--json")
			var verdict review.Verdict
			if code != 1 || json.Unmarshal([]byte(text), &verdict) != nil || verdict.Witnesses != review.WitnessesMapped || len(verdict.MissingChecks) != 1 {
				t.Fatal(code, text, stderr)
			}
			if operation != "delete" {
				code, text, stderr = run(fixture, "inspect", path+":2", "--contracts", "--json")
				var inspection analysis.Report
				if code != 0 || json.Unmarshal([]byte(text), &inspection) != nil || inspection.Contracts.Verification == nil || len(inspection.Contracts.Verification.Checks) != 1 || inspection.Contracts.Verification.Checks[0].Cases[0] != "pkg::TestOwn" {
					t.Fatal(code, text, stderr)
				}
				code, text, stderr = run(fixture, "inspect", path+":2", "--contracts")
				if code != 0 || !strings.Contains(text, "Required check owned · pkg::TestOwn (not run)") {
					t.Fatal(code, text, stderr)
				}
			}
			fixture.Write("packaging/unrelated.go", "package unrelated\n")
			code, text, stderr = run(fixture, "review", "packaging/unrelated.go", "--json")
			if code != 0 || json.Unmarshal([]byte(text), &contribution) != nil || len(contribution.RequiredChecks) != 0 {
				t.Fatal("unowned change selected check", code, text, stderr)
			}
		})
	}
}

func TestReviewCannotNarrowRequiredCheckScope(t *testing.T) {
	for _, change := range []string{"remove", "narrow", "broaden"} {
		t.Run(change, func(t *testing.T) {
			fixture := testrepo.New(t)
			fixture.Config.Checks = map[string]policy.CheckDefinition{"owned": ownedCheck()}
			fixture.Save()
			fixture.Commit()
			check := fixture.Config.Checks["owned"]
			switch change {
			case "remove":
				check.Paths = nil
			case "narrow":
				check.Paths = []string{"packaging/owned/one.go"}
			case "broaden":
				check.Paths = []string{"packaging/"}
			}
			fixture.Config.Checks["owned"] = check
			fixture.Save()
			code, text, stderr := run(fixture, "review", policy.File, "--json")
			var contribution review.Contribution
			if code != 0 || json.Unmarshal([]byte(text), &contribution) != nil {
				t.Fatal(code, text, stderr)
			}
			weakened := false
			for _, finding := range contribution.Context.Candidate.Findings {
				if finding.Kind == "policy_witness_weakened" && finding.Rule == "policy/changed" {
					weakened = true
				}
			}
			if weakened != (change != "broaden") {
				t.Fatal(change, contribution.Context.Candidate.Findings)
			}
		})
	}
}

// A fence names a JavaScript test by its title. Inspection resolves the title
// in the test file, a check that maps that reference owns it however the key
// spells the title's spaces, and the scope's witnesses are counted by what
// stands behind each: cases a check runs, a symbol or test no check runs, or a
// file.
func TestTestTitleWitnessesResolveMapAndAreSummarized(t *testing.T) {
	fixture := contractFixture(t)
	path, test := "npm-packages/convex/index.ts", "npm-packages/convex/index.test.ts"
	fixture.Write(test, "describe(\"doubling\", () => {\n  it(\"doubles the value\", () => {});\n  it(\"is unmapped\", () => {});\n});\n")
	fixture.Write("npm-packages/convex/doubling.ts", "export const factor = 2;\n")
	fixture.Write(path, "// concave:begin doubled\n// why: The console doubles the value.\n//\n// witness: "+test+"::doubling doubles the value\n// witness: "+test+"::is unmapped\n// witness: npm-packages/convex/doubling.ts\nexport const value = 2;\n// concave:end doubled\n")
	fixture.Config.Checks = map[string]policy.CheckDefinition{"unit": {
		Command: []string{"sh", "-c", "true"}, Directory: ".", Inputs: []string{"npm-packages/convex/"},
		Toolchain:      []policy.ToolCheck{{Command: []string{"sh", "-c", "printf v1"}, Expected: "v1"}},
		TimeoutSeconds: 5, Results: "jest-json",
		Witnesses: map[string][]string{test + "::doubling doubles the value": {test + "::doubling doubles the value"}},
	}}
	fixture.Save()
	report := inspection(t, fixture, path, "--contracts")
	if report.Contracts == nil || len(report.Contracts.Files) != 1 || len(report.Contracts.Files[0].Witnesses) != 3 {
		t.Fatalf("%+v", report.Contracts)
	}
	resolutions := map[string]string{}
	for _, witness := range report.Contracts.Files[0].Witnesses {
		resolutions[witness.Reference] = witness.Resolution
	}
	for reference, resolution := range map[string]string{
		test + "::doublingdoublesthevalue": "test_case_found",
		test + "::isunmapped":              "test_case_found",
		"npm-packages/convex/doubling.ts":  "file_found",
	} {
		if resolutions[reference] != resolution {
			t.Fatal(reference, resolutions)
		}
	}
	verification := report.Contracts.Verification
	if verification == nil || len(verification.Checks) != 1 || verification.Checks[0].Name != "unit" || len(verification.Unmapped) != 2 {
		t.Fatalf("%+v", verification)
	}
	if summary := report.Contracts.WitnessSummary; summary == nil || *summary != (analysis.WitnessSummary{Total: 3, Mapped: 1, UnmappedWitnesses: 1, SourceReferences: 1}) {
		t.Fatalf("%+v", summary)
	}
	code, text, stderr := run(fixture, "inspect", path, "--contracts")
	if code != 0 || !strings.Contains(text, "Witnesses: 3 cited · 1 run by a named check · 1 name a symbol or test no check runs · 1 source references") {
		t.Fatal(code, text, stderr)
	}
	// A renamed test leaves the reference unresolved instead of silently kept.
	fixture.Write(test, "describe(\"doubling\", () => {\n  it(\"doubles\", () => {});\n});\n")
	report = inspection(t, fixture, path, "--contracts")
	for _, witness := range report.Contracts.Files[0].Witnesses {
		if witness.Symbol != "" && witness.Resolution != "symbol_not_resolved" {
			t.Fatal("a removed test title still resolved", witness)
		}
	}
}
