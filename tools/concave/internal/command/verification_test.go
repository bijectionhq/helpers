package command_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/testrepo"
	"concave/internal/verification"
)

func TestVerificationCheckNamesStaleInputsWithoutEditingReceipt(t *testing.T) {
	fixture := testrepo.New(t)
	fixture.Config.Checks = map[string]policy.CheckDefinition{"local": {
		Command: []string{"sh", "-c", "printf done"}, Directory: ".", Inputs: []string{"crates/core/"},
		Toolchain:      []policy.ToolCheck{{Command: []string{"sh", "-c", "printf tool-1"}, Expected: "tool-1"}},
		TimeoutSeconds: 5, Results: "command",
	}}
	fixture.Write("crates/core/gone.rs", "old input\n")
	fixture.Save()
	fixture.Commit()
	directory := filepath.Join(fixture.Root, "target", "receipt")
	code, text, stderr := run(fixture, "verify", "run", "local", "--output", directory)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	path := filepath.Join(directory, "receipt.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, text, stderr = run(fixture, "verify", "check", path)
	if code != 0 || !strings.Contains(text, `"status": "current"`) {
		t.Fatal(code, text, stderr)
	}
	fixture.Write("crates/core/lib.rs", "changed input\n")
	fixture.Write("crates/core/added.rs", "new input\n")
	if err := os.Remove(filepath.Join(fixture.Root, "crates/core/gone.rs")); err != nil {
		t.Fatal(err)
	}
	working, index := fixture.Git("diff", "--"), fixture.Git("ls-files", "--stage")
	for _, options := range [][]string{{}, {"--json"}} {
		code, text, stderr = run(fixture, append([]string{"verify", "check", path}, options...)...)
		if code != 3 || text != "" {
			t.Fatal(code, text, stderr)
		}
		for _, want := range []string{`added "crates/core/added.rs"`, `removed "crates/core/gone.rs"`, `changed "crates/core/lib.rs"`} {
			if !strings.Contains(stderr, want) {
				t.Fatal("missing changed input", want, stderr)
			}
		}
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, current) || fixture.Git("diff", "--") != working || fixture.Git("ls-files", "--stage") != index {
		t.Fatal("diagnosis edited receipt, source or index", err)
	}
}

// The real filtered dataset report had one pass, one failure and 54 skipped
// cases. Replaying that shape must retain executed counts without accepting it.
func TestVerificationFailedVitestCountsReachOutputAndReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		json         bool
	}{
		{"runner failure", "cat results.json; exit 1", false},
		{"report failure with zero exit", "cat results.json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := testrepo.New(t)
			fixture.Write("results.json", `{"success":false,"numPassedTests":1,"numFailedTests":1,"numPendingTests":54,"testResults":[{"assertionResults":[`+strings.Repeat(`{"status":"skipped"},`, 54)+`{"status":"passed"},{"status":"failed"}]}]}`)
			fixture.Config.Checks = map[string]policy.CheckDefinition{"local": {
				Command: []string{"sh", "-c", tc.script}, Directory: ".", Inputs: []string{"results.json"},
				Toolchain:      []policy.ToolCheck{{Command: []string{"sh", "-c", "printf tool-1"}, Expected: "tool-1"}},
				TimeoutSeconds: 5, Results: "vitest-json",
			}}
			fixture.Save()
			fixture.Commit()
			index, source := fixture.Git("ls-files", "--stage"), fixture.Git("diff", "--")
			directory := filepath.Join(fixture.Root, "target", "failed")
			args := []string{"verify", "run", "local", "--output", directory}
			if tc.json {
				args = append(args, "--json")
			}
			code, text, stderr := run(fixture, args...)
			want := "failed; 2 executed, 1 passed, 1 failed, 54 skipped"
			if tc.json {
				want = `"executed": 2`
			}
			if code != 1 || !strings.Contains(text, want) {
				t.Fatal(code, text, stderr)
			}
			path := filepath.Join(directory, "receipt.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var receipt verification.Receipt
			if err := json.Unmarshal(data, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Status != verification.Failed || !reflect.DeepEqual(receipt.Results, verification.Results{Format: "vitest-json", Passed: 1, Failed: 1, Skipped: 54, Executed: 2}) {
				t.Fatal(receipt.Status, receipt.Results)
			}
			code, text, stderr = run(fixture, "verify", "check", path)
			if code != 3 || !strings.Contains(stderr, "receipt did not pass: failed") {
				t.Fatal(code, text, stderr)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) || fixture.Git("ls-files", "--stage") != index || fixture.Git("diff", "--") != source {
				t.Fatal("failed verification changed receipt, source or index", err)
			}
		})
	}
}

func TestNamedVerificationOwnsEvidenceDirectory(t *testing.T) {
	fixture := testrepo.New(t)
	fixture.Config.Checks = map[string]policy.CheckDefinition{"local": {
		Command: []string{"sh", "-c", "printf done"}, Directory: ".", Inputs: []string{"crates/core/"},
		Toolchain:      []policy.ToolCheck{{Command: []string{"sh", "-c", "printf tool-1"}, Expected: "tool-1"}},
		TimeoutSeconds: 5, Results: "command",
	}}
	fixture.Save()
	fixture.Commit()
	for range 2 {
		code, text, stderr := run(fixture, "verify", "run", "local")
		if code != 0 || !strings.Contains(text, "Receipt:") || !strings.Contains(text, "command check") || strings.Contains(text, "0 executed") {
			t.Fatal(code, text, stderr)
		}
	}
	receipts, err := filepath.Glob(filepath.Join(fixture.Root, "target/concave/checks/*/receipt.json"))
	if err != nil || len(receipts) != 2 {
		t.Fatal(receipts, err)
	}
}
