package policy_test

import (
	"slices"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestRustCheckBindsOneHarnessAndWitnessSource(t *testing.T) {
	for _, kind := range []string{"valid", "wrong target", "unbound witness", "multiple harnesses", "wrong threads"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			check := policy.CheckDefinition{
				Command:   []string{"python3", "scripts/build_slot.py", "--", "cargo", "test", "--locked", "-p", "core", "--lib", "--", "--test-threads=1", "--format=json"},
				Directory: ".", Inputs: []string{"crates/core/"},
				Toolchain:      []policy.ToolCheck{{Command: []string{"rustc", "--version"}, Expected: "pinned"}},
				TimeoutSeconds: 10, Results: "rust-test-json", Target: "core/lib",
				Witnesses: map[string][]string{"crates/core/lib.rs::tests::reads": {"core/lib::tests::reads"}},
			}
			switch kind {
			case "wrong target":
				check.Target = "other/lib"
			case "unbound witness":
				check.Inputs = []string{"Cargo.lock"}
			case "multiple harnesses":
				check.Command[8] = "--all-targets"
			case "wrong threads":
				check.Command[10] = "--test-threads=2"
			}
			f.Config.Checks = map[string]policy.CheckDefinition{"reads": check}
			if err := f.Config.Validate(); (err == nil) != (kind == "valid") {
				t.Fatal(kind, err)
			}
		})
	}
}

// An environment includes only distinct, known environments that include
// nothing themselves; a check's closure is itself, then its includes.
func TestEnvironmentIncludesAreKnownDistinctAndFlat(t *testing.T) {
	for _, kind := range []string{"valid", "unknown", "self", "nested", "repeated"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			workspace := func(includes ...string) policy.Environment {
				return policy.Environment{Directory: "npm-packages", Packages: []string{"convex"}, Includes: includes, TimeoutSeconds: 60}
			}
			f.Config.Environments = map[string]policy.Environment{"fixtures": workspace("docs"), "docs": workspace(), "sdk": workspace()}
			switch kind {
			case "unknown":
				f.Config.Environments["fixtures"] = workspace("missing")
			case "self":
				f.Config.Environments["fixtures"] = workspace("fixtures")
			case "nested":
				f.Config.Environments["docs"] = workspace("sdk")
			case "repeated":
				f.Config.Environments["fixtures"] = workspace("docs", "docs")
			}
			if err := f.Config.Validate(); (err == nil) != (kind == "valid") {
				t.Fatal(kind, err)
			}
			if kind == "valid" && !slices.Equal(f.Config.EnvironmentClosure("fixtures"), []string{"fixtures", "docs"}) {
				t.Fatal(f.Config.EnvironmentClosure("fixtures"))
			}
		})
	}
}

// A fence reference holds no whitespace, so a check may spell a test title
// with its spaces and still own the reference a fence makes of it.
func TestCheckWitnessKeysNameWhitespaceFreeReferences(t *testing.T) {
	check := policy.CheckDefinition{
		Command: []string{"jest"}, Directory: "app", Inputs: []string{"app/"}, Toolchain: []policy.ToolCheck{{Command: []string{"node", "--version"}}},
		TimeoutSeconds: 60, Results: "jest-json",
		Witnesses: map[string][]string{
			"app/dialog.test.tsx::rotates the listed credential": {"app/dialog.test.tsx::Dialog rotates the listed credential"},
			"app/dialog.test.tsx":                                {"app/dialog.test.tsx::Dialog renders"},
		},
	}
	config := testrepo.New(t).Config
	config.Checks = map[string]policy.CheckDefinition{"console-unit": check}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	reference := "app/dialog.test.tsx::rotatesthelistedcredential"
	requirements := config.RequiredChecks(nil, []string{reference, "app/other.test.tsx::absent"})
	if len(requirements.Checks) != 1 || requirements.Checks[0].Name != "console-unit" || len(requirements.Unmapped) != 1 || requirements.Unmapped[0] != "app/other.test.tsx::absent" {
		t.Fatalf("%+v", requirements)
	}
	// The executed case keeps the runner's own spelling.
	if cases := config.Checks["console-unit"].Witnesses[reference]; len(cases) != 1 || cases[0] != "app/dialog.test.tsx::Dialog rotates the listed credential" {
		t.Fatal(cases)
	}
	check.Witnesses["app/dialog.test.tsx::rotates  the listed\tcredential"] = []string{"app/dialog.test.tsx::other"}
	config = testrepo.New(t).Config
	config.Checks = map[string]policy.CheckDefinition{"console-unit": check}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatal("two spellings of one reference were accepted", err)
	}
}
