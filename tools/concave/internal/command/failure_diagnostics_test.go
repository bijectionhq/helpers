package command_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/testrepo"
	"concave/internal/verification"
)

func TestVerificationRetainsPreciseFailureAndSetupDiagnostics(t *testing.T) {
	fixture := testrepo.New(t)
	fixture.Write("results.json", `{"success":false,"numFailedTests":1,"testResults":[{"name":"auth.test.ts","assertionResults":[{"fullName":"rejects bad audience","status":"failed","failureMessages":["AssertionError: expected rejection for wrong audience"]}]}]}`)
	fixture.Config.Checks = map[string]policy.CheckDefinition{"local": {
		Command: []string{"sh", "-c", "cat results.json; exit 1"}, Directory: ".", Inputs: []string{"results.json"},
		Toolchain: []policy.ToolCheck{{Command: []string{"sh", "-c", "printf tool-1"}, Expected: "tool-1"}}, TimeoutSeconds: 5, Results: "vitest-json",
	}}
	fixture.Save()
	fixture.Commit()
	for _, jsonMode := range []bool{false, true} {
		dir := filepath.Join(fixture.Root, "target", map[bool]string{false: "text", true: "json"}[jsonMode])
		args := []string{"verify", "run", "local", "--output", dir}
		if jsonMode {
			args = append(args, "--json")
		}
		code, text, stderr := run(fixture, args...)
		if code != 1 || !strings.Contains(text, "expected rejection for wrong audience") || strings.Contains(text, "invalid_results") || strings.Contains(text, "did not start") {
			t.Fatal(code, text, stderr)
		}
		if jsonMode {
			var receipt verification.Receipt
			if err := json.Unmarshal([]byte(text), &receipt); err != nil {
				t.Fatal(err)
			}
			if !receipt.ExecutionStarted || len(receipt.Results.Cases) != 1 || receipt.Results.Cases[0].Diagnostic == "" || receipt.Format != verification.ReceiptFormat {
				t.Fatal(receipt)
			}
			found := false
			for _, problem := range receipt.Problems {
				found = found || problem.Code == "tests_failed"
			}
			if !found {
				t.Fatal(receipt.Problems)
			}
		}
	}
	check := fixture.Config.Checks["local"]
	check.Toolchain[0].Expected = "tool-2"
	fixture.Config.Checks["local"] = check
	fixture.Save()
	fixture.Commit()
	code, text, stderr := run(fixture, "verify", "run", "local", "--output", filepath.Join(fixture.Root, "target/setup"))
	for _, want := range []string{"tool-2", "PATH", "Check execution did not start.", "0 executed"} {
		if code != 1 || !strings.Contains(text, want) {
			t.Fatal(want, code, text, stderr)
		}
	}
}
