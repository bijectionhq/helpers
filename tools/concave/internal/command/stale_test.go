package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/testrepo"
	"concave/internal/verification"
)

func TestSourceAndPolicyRacesHaveTheSameExitCategory(t *testing.T) {
	for _, args := range [][]string{
		{"inspect", "--contracts"}, {"check"}, {"inspect", "queue"}, {"inspect", "queue", "--contracts"},
	} {
		for _, subject := range []string{"source", "policy"} {
			t.Run(strings.Join(args, "-")+"/"+subject, func(t *testing.T) {
				fixture := contractFixture(t)
				engine := os.Getenv("CONCAVE_AST_GREP")
				source := "crates/core/adapter.rs"
				target := source
				if subject == "policy" {
					target = policy.File
				}
				// Mutate exactly this fixture after its source reaches the real parser.
				// The wrapper preserves the engine version and delegates all analysis.
				wrapper := filepath.Join(fixture.Root, "target", "engine")
				script := "#!/bin/sh\nif [ \"$1\" != --version ]; then\nprintf ' ' >> " + process.ShellCommand([]string{filepath.Join(fixture.Root, target)}) + "\nfi\nexec " + process.ShellCommand([]string{engine}) + " \"$@\"\n"
				fixture.Write("target/engine", script)
				if err := os.Chmod(wrapper, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CONCAVE_AST_GREP", wrapper)
				code, text, stderr := run(fixture, append(args, source, "--json")...)
				if code != 3 || text != "" || !strings.Contains(stderr, target) {
					t.Fatal("race did not refuse with a located stale error", code, text, stderr)
				}
			})
		}
	}
}

func TestVerificationPersistsStaleReceiptAndUsesExitThree(t *testing.T) {
	fixture := testrepo.New(t)
	fixture.Config.Checks = map[string]policy.CheckDefinition{"local": {
		Command:   []string{"sh", "-c", "printf changed > crates/core/lib.rs"},
		Directory: ".", Inputs: []string{"crates/core/"},
		Toolchain:      []policy.ToolCheck{{Command: []string{"sh", "-c", "printf tool-1"}, Expected: "tool-1"}},
		TimeoutSeconds: 5, Results: "command",
	}}
	fixture.Save()
	directory := filepath.Join(fixture.Root, "target", "verification")
	code, text, stderr := run(fixture, "verify", "run", "local", "--output", directory, "--json")
	if code != 3 {
		t.Fatal(code, text, stderr)
	}
	var receipt verification.Receipt
	if err := json.Unmarshal([]byte(text), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != verification.Stale || len(receipt.Problems) != 1 || !strings.Contains(receipt.Problems[0].Message, `changed "crates/core/lib.rs"`) {
		t.Fatal(receipt.Status, receipt.Problems)
	}
	if _, err := os.Stat(filepath.Join(directory, "receipt.json")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ = run(fixture, "verify", "check", filepath.Join(directory, "receipt.json")); code != 3 {
		t.Fatal(code)
	}
}
