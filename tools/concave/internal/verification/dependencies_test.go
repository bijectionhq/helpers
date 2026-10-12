package verification

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
)

func TestReceiptDependsOnSelectedCheckAndExplicitPolicyInputs(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected-check", true: "whole-policy"}[explicit], func(t *testing.T) {
			f := verificationFixture(t)
			if explicit {
				check := f.Config.Checks["local"]
				check.Inputs = append(check.Inputs, policy.File)
				f.Config.Checks["local"] = check
				f.Save()
			}
			repo, config := f.Open()
			receipt, err := Run(repo, config, "local", filepath.Join(f.Root, "target", "first"))
			if err != nil || receipt.Status != Passed {
				t.Fatal(receipt, err)
			}
			f.Config.Checks["unrelated"] = f.Config.Checks["local"]
			f.Save()
			current, err := policy.Load(repo, "")
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(repo, current, receipt)
			if (err != nil) != explicit {
				t.Fatal("wrong policy dependency", explicit, err)
			}
			if !explicit {
				second, err := Run(repo, current, "local", filepath.Join(f.Root, "target", "second"))
				if err != nil || second.Status != Passed || second.EvidenceSHA256 != receipt.EvidenceSHA256 {
					t.Fatal("unrelated policy changed evidence", second, err)
				}
			}
			check := f.Config.Checks["local"]
			check.TimeoutSeconds++
			f.Config.Checks["local"] = check
			f.Save()
			if err = Validate(repo, config, receipt); !repository.IsStale(err) {
				t.Fatal("changed definition hidden by stale config")
			}
			stale, err := Run(repo, config, "local", filepath.Join(f.Root, "target", "stale"))
			if err != nil || stale.Status != Stale || stale.ExecutionStarted || stale.Problems[0].Code != "definition_changed" {
				t.Fatal("stale command authorized", stale, err)
			}
		})
	}
}

func TestVerificationDetectsDefinitionChangeDuringExecution(t *testing.T) {
	f := verificationFixture(t)
	check := f.Config.Checks["local"]
	check.Command[2] = "cp next.json concave.json; " + check.Command[2]
	f.Config.Checks["local"] = check
	f.Save()
	repo, config := f.Open()
	next := *f.Config
	next.Checks = map[string]policy.CheckDefinition{"local": check}
	changed := check
	changed.TimeoutSeconds++
	next.Checks["local"] = changed
	data, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	f.Write("next.json", string(data))
	receipt, err := Run(repo, config, "local", filepath.Join(f.Root, "target", "changed"))
	if err != nil || receipt.Status != Stale {
		t.Fatal(receipt, err)
	}
}

func TestVerificationIgnoresUnconsumedPolicyFormattingDuringExecution(t *testing.T) {
	f := verificationFixture(t)
	check := f.Config.Checks["local"]
	check.Command[2] += "; printf ' ' >> concave.json"
	f.Config.Checks["local"] = check
	f.Save()
	repo, config := f.Open()
	receipt, err := Run(repo, config, "local", filepath.Join(f.Root, "target", "formatting"))
	if err != nil || receipt.Status != Passed {
		t.Fatal(receipt, err)
	}
}

func TestUnrelatedHeadAdvanceDoesNotInvalidateBoundSource(t *testing.T) {
	f := verificationFixture(t)
	check := f.Config.Checks["local"]
	check.Command[2] = "git -c user.name=fixture -c user.email=fixture@example.invalid commit --allow-empty -qm advance; " + check.Command[2]
	f.Config.Checks["local"] = check
	f.Save()
	f.Commit()
	repo, config := f.Open()
	receipt, err := Run(repo, config, "local", filepath.Join(f.Root, "target/head-advance"))
	if err != nil || receipt.Status != Passed {
		t.Fatal(receipt, err)
	}
	head, err := repo.Resolve("HEAD")
	if err != nil || head == receipt.Head {
		t.Fatal("fixture did not advance HEAD", head, err)
	}
	if err := Validate(repo, config, receipt); err != nil {
		t.Fatal(err)
	}
}
