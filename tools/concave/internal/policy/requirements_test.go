package policy_test

import (
	"testing"

	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestRequiredCheckPathsAreBoundAndNeedCases(t *testing.T) {
	for _, kind := range []string{"valid", "unsafe", "duplicate", "unbound", "unbounded directory", "no cases"} {
		t.Run(kind, func(t *testing.T) {
			fixture := testrepo.New(t)
			check := policy.CheckDefinition{
				Paths: []string{"crates/core/"}, Inputs: []string{"crates/core/"},
				Command: []string{"go", "test", "-json", "./..."}, Directory: ".",
				Toolchain:      []policy.ToolCheck{{Command: []string{"go", "version"}, Expected: "pinned"}},
				TimeoutSeconds: 10, Results: "go-test-json",
				Witnesses: map[string][]string{"crates/core/test.go": {"pkg::TestOwn"}},
			}
			switch kind {
			case "unsafe":
				check.Paths = []string{"../outside"}
			case "duplicate":
				check.Paths = append(check.Paths, check.Paths...)
			case "unbound":
				check.Paths = []string{"elsewhere/"}
			case "unbounded directory":
				check.Inputs = []string{"crates/core/test.go"}
			case "no cases":
				check.Witnesses = nil
			}
			fixture.Config.Checks = map[string]policy.CheckDefinition{"owned": check}
			if err := fixture.Config.Validate(); (err == nil) != (kind == "valid") {
				t.Fatal(kind, err)
			}
		})
	}
}
