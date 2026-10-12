package verification

import (
	"concave/internal/policy"
	"concave/internal/process"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestObservedRuntimeAndDeclaredVariablesInvalidateReuse(t *testing.T) {
	f := verificationFixture(t)
	check := f.Config.Checks["local"]
	check.ReuseReason = "Fixture output and shell identity are entirely declared."
	check.Variables = map[string]string{"CONCAVE_TEST_VERSION": "v1"}
	check.Toolchain = []policy.ToolCheck{{Command: []string{"sh", "-c", "printf %s \"$CONCAVE_TEST_VERSION\""}}}
	f.Config.Checks["local"] = check
	f.Save()
	repo, config := f.Open()
	dir := filepath.Join(f.Root, "target/runtime")
	receipt, err := Run(repo, config, "local", dir)
	if err != nil || receipt.Status != Passed || receipt.Toolchain[0].Version != "v1" {
		t.Fatal(receipt, err)
	}
	if decision := Reuse(repo, config, "local", dir, receipt); decision.State != "reusable" {
		t.Fatal(decision)
	}
	check.Variables["CONCAVE_TEST_VERSION"] = "v2"
	f.Config.Checks["local"] = check
	f.Save()
	_, config = f.Open()
	if decision := Reuse(repo, config, "local", dir, receipt); decision.State != "stale" {
		t.Fatal(decision)
	}
}

func TestToolIdentityBindsSelectedGoRuntime(t *testing.T) {
	f := verificationFixture(t)
	repo, _ := f.Open()
	check := policy.CheckDefinition{Directory: ".", Variables: map[string]string{"GOTOOLCHAIN": runtime.Version()}, Toolchain: []policy.ToolCheck{{Command: []string{"go", "env", "GOVERSION"}, Expected: runtime.Version()}}}
	identity, err := tools(repo, check)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ExecutionContext(repo.Context, check)
	root, err := process.Run(ctx, repo.Root, []string{"go", "env", "GOROOT"}, nil, 4096)
	if err != nil {
		t.Fatal(err)
	}
	path, digest, err := process.Executable(repo.Root, filepath.Join(strings.TrimSpace(string(root.Stdout)), "bin/go"))
	if err != nil || len(identity) != 1 || identity[0].Executable != path || identity[0].SHA256 != digest {
		t.Fatal(identity, path, digest, err)
	}
}
