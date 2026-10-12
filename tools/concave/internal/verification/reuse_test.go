package verification

import (
	"concave/internal/policy"
	"path/filepath"
	"testing"
)

func TestReuseRequiresExplicitBoundaryAndExactEvidence(t *testing.T) {
	for _, scenario := range []string{"default", "current", "input", "member", "definition", "tool", "log", "receipt", "failed", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			f := verificationFixture(t)
			check := f.Config.Checks["local"]
			if scenario != "default" {
				check.ReuseReason = "The fixture only prints fixed JSON; declared source and shell identity are its complete inputs."
			}
			f.Config.Checks["local"] = check
			f.Save()
			repo, config := f.Open()
			dir := filepath.Join(f.Root, "target/reuse")
			receipt, err := Run(repo, config, "local", dir)
			if err != nil {
				t.Fatal(err)
			}
			want := "reusable"
			switch scenario {
			case "default":
				want = "rerun"
			case "input":
				f.Write("crates/core/lib.rs", "changed\n")
				want = "stale"
			case "member":
				f.Write("crates/core/new.rs", "new\n")
				want = "stale"
			case "definition":
				check.TimeoutSeconds++
				f.Config.Checks["local"] = check
				f.Save()
				config, err = policy.Load(repo, "")
				if err != nil {
					t.Fatal(err)
				}
				want = "stale"
			case "tool":
				receipt.Toolchain[0].SHA256 = "changed"
				receipt.seal()
				want = "stale"
			case "log":
				f.Write("target/reuse/stdout.log", "tampered")
				want = "invalid"
			case "receipt":
				receipt.ExitCode = 1
				want = "invalid"
			case "failed":
				receipt.Status = Failed
				receipt.seal()
				want = "failed"
			case "missing":
				receipt = nil
				want = "missing"
			}
			decision := Reuse(repo, config, "local", dir, receipt)
			if decision.State != want {
				t.Fatal(decision, want)
			}
		})
	}
}
