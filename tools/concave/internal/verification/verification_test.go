package verification

import (
	"concave/internal/policy"
	"concave/internal/testrepo"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultsRequireExecutedCasesAndCountLeaves(t *testing.T) {
	for _, tc := range []struct {
		format, data    string
		passed, skipped int
		bad             bool
	}{
		{"go-test-json", "{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestA\"}\n{\"Action\":\"pass\",\"Package\":\"p\",\"Test\":\"TestA/child\"}\n{\"Action\":\"skip\",\"Package\":\"p\",\"Test\":\"TestSkip\"}\n", 1, 1, false},
		{"go-test-json", "{\"Action\":\"pass\",\"Package\":\"p\"}\n", 0, 0, true},
		{"go-test-json", "{\"Action\":\"output\",\"Package\":\"p\",\"Output\":\"ok p (cached)\"}\n", 0, 0, true},
		{"go-test-json", "{\"Action\":\"fail\",\"Test\":\"TestA\"}\n", 0, 0, true},
		{"vitest-json", `{"success":true,"numPassedTests":1,"testResults":[{"name":"test.ts","assertionResults":[{"fullName":"reads","status":"passed"}]}]}`, 1, 0, false},
		{"vitest-json", `{"success":true,"numPassedTests":1,"testResults":[]}`, 0, 0, true},
		{"node-test-tap", "TAP version 13\nok 1 - reads\nok 2 - skips # SKIP\n# tests 2\n# pass 1\n# fail 0\n# cancelled 0\n# skipped 1\n# todo 0\n", 1, 1, false},
		{"node-test-tap", "# tests 0\n# pass 0\n# fail 0\n# cancelled 0\n# skipped 0\n# todo 0\n", 0, 0, true},
	} {
		t.Run(tc.format+tc.data[:min(10, len(tc.data))], func(t *testing.T) {
			r, err := parseResults(tc.format, []byte(tc.data))
			if (err != nil) != tc.bad || !tc.bad && (r.Passed != tc.passed || r.Skipped != tc.skipped) {
				t.Fatal(r, err)
			}
		})
	}
}

func verificationFixture(t *testing.T) *testrepo.Fixture {
	f := testrepo.New(t)
	f.Config.Checks = map[string]policy.CheckDefinition{"local": {Command: []string{"sh", "-c", `printf '%s\n' '{"Action":"pass","Package":"example","Test":"TestRead"}'`}, Directory: ".", Inputs: []string{"crates/core/"}, Toolchain: []policy.ToolCheck{{Command: []string{"sh", "-c", "printf tool-1"}, Expected: "tool-1"}}, TimeoutSeconds: 5, Results: "go-test-json"}}
	f.Save()
	f.Commit()
	return f
}
func TestReceiptBindsSourceToolchainResultsAndExactBytes(t *testing.T) {
	f := verificationFixture(t)
	r, c := f.Open()
	p, err := Run(r, c, "local", filepath.Join(f.Root, "target", "run"))
	if err != nil || p.Status != "passed" || p.Results.Executed != 1 {
		t.Fatal(p, err)
	}
	if err = Validate(r, c, p); err != nil {
		t.Fatal(err)
	}
	if err = ValidateLogs(filepath.Join(f.Root, "target", "run"), p); err != nil {
		t.Fatal(err)
	}
	f.Write("target/run/stdout.log", "tampered")
	if err = ValidateLogs(filepath.Join(f.Root, "target", "run"), p); err == nil {
		t.Fatal("changed log accepted")
	}
	copy := *p
	copy.ElapsedMS++
	if err := copy.seal(); err != nil {
		t.Fatal(err)
	}
	if copy.EvidenceSHA256 != p.EvidenceSHA256 || copy.ReceiptSHA256 == p.ReceiptSHA256 {
		t.Fatal("observation affects deterministic evidence")
	}
	f.Write("crates/core/new.rs", "new source\n")
	if err = Validate(r, c, p); err == nil {
		t.Fatal("unrecorded new input accepted")
	}
	p.Results.Passed++
	if err = Validate(r, c, p); err == nil {
		t.Fatal("receipt edit accepted")
	}
}
func TestWrongToolZeroTestsFailedTimeoutAndSourceRace(t *testing.T) {
	for _, kind := range []string{"tool", "zero", "failed", "timeout", "source"} {
		t.Run(kind, func(t *testing.T) {
			f := verificationFixture(t)
			check := f.Config.Checks["local"]
			switch kind {
			case "tool":
				check.Toolchain[0].Expected = "tool-2"
			case "zero":
				check.Command = []string{"sh", "-c", `printf '%s\n' '{"Action":"pass","Package":"example"}'`}
			case "failed":
				check.Command = []string{"sh", "-c", "exit 7"}
			case "timeout":
				check.Command = []string{"sh", "-c", "sleep 10"}
				check.TimeoutSeconds = 1
			case "source":
				check.Command[2] += "; printf changed > crates/core/lib.rs"
			}
			f.Config.Checks["local"] = check
			f.Save()
			r, c := f.Open()
			p, err := Run(r, c, "local", filepath.Join(f.Root, "target", "run"))
			if kind == "tool" {
				if err != nil || p.Status != Failed || p.ExecutionStarted || p.Phase != "preparation" || !strings.Contains(p.Problems[0].Message, "expected") {
					t.Fatal(p, err)
				}
			}
			if err != nil || p.Status == "passed" {
				t.Fatal(p, err)
			}
			if kind == "source" && p.Status != "stale" {
				t.Fatal(p)
			}
			if _, err = os.Stat(filepath.Join(f.Root, "target", "run", "receipt.json")); err != nil {
				t.Fatal("failure receipt missing", err)
			}
		})
	}
}

func TestToolProbeFailureNamesDependencyAndKeepsBoundedDiagnostic(t *testing.T) {
	f := verificationFixture(t)
	check := f.Config.Checks["local"]
	check.Toolchain[0].Command = []string{"sh", "-c", "printf missing-fixture-dependency >&2; exit 7"}
	f.Config.Checks["local"] = check
	f.Save()
	repo, config := f.Open()
	dir := filepath.Join(f.Root, "target/missing-tool")
	receipt, err := Run(repo, config, "local", dir)
	if err != nil || receipt.Status != Failed || receipt.ExecutionStarted || receipt.Problems[0].Code != "tool_unavailable" || !strings.Contains(receipt.Problems[0].Message, "missing-fixture-dependency") {
		t.Fatal(receipt, err)
	}
	if err := ValidateLogs(dir, receipt); err != nil {
		t.Fatal(err)
	}
}
