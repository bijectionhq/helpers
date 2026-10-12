package verification

import (
	"os"
	"path/filepath"
	"testing"

	"concave/internal/policy"
	"concave/internal/process"
)

func TestCheckBindsSelectedStructuralEngine(t *testing.T) {
	configured := os.Getenv("CONCAVE_AST_GREP")
	if configured == "" {
		configured = "../../../../target/concave/engine/0.45.3/ast-grep"
	}
	engine, err := filepath.Abs(configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCAVE_AST_GREP", engine)
	fixture := verificationFixture(t)
	check := fixture.Config.Checks["local"]
	check.Toolchain = []policy.ToolCheck{{Command: []string{"ast-grep", "--version"}, Expected: "ast-grep 0.45.3"}}
	fixture.Config.Checks["local"] = check
	fixture.Save()
	repo, config := fixture.Open()
	receipt, err := Run(repo, config, "local", filepath.Join(fixture.Root, "target/check"))
	if err != nil || receipt.Status != Passed {
		t.Fatal(receipt, err)
	}
	executable, digest, err := process.Executable(fixture.Root, engine)
	if err != nil || len(receipt.Toolchain) != 1 || receipt.Toolchain[0].Executable != executable || receipt.Toolchain[0].SHA256 != digest {
		t.Fatal("receipt did not bind selected engine", receipt.Toolchain, err)
	}
}
