package verification

import (
	"strings"
	"testing"

	"concave/internal/policy"
)

func TestGoWitnessParentExplainsExactSubtestMappings(t *testing.T) {
	check := policy.CheckDefinition{Results: "go-test-json", Witnesses: map[string][]string{"tests.go": {"pkg::TestOwner"}}}
	results := Results{Cases: []Case{{ID: "pkg::TestOwner/edit", Status: "passed"}, {ID: "pkg::TestOwner/delete", Status: "passed"}}}
	err := ValidateWitnesses(check, results)
	if err == nil || !strings.Contains(err.Error(), "Go counts leaf cases") || !strings.Contains(err.Error(), "pkg::TestOwner/delete, pkg::TestOwner/edit") {
		t.Fatal(err)
	}
	check.Witnesses["tests.go"] = []string{"pkg::TestOwner/edit", "pkg::TestOwner/delete"}
	if err = ValidateWitnesses(check, results); err != nil {
		t.Fatal(err)
	}
	results.Cases[1].Status = "skipped"
	if err = ValidateWitnesses(check, results); err == nil {
		t.Fatal("skipped subtest accepted")
	}
}
