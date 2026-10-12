package verification

import (
	"strings"
	"testing"
)

// Verbose unittest output yields one case per module.Class.test identity, its
// docstring line included; the summary must exist and agree with the cases.
func TestPythonUnittestResultsBindEveryCaseToTheSummary(t *testing.T) {
	output := `test_package_names (test_release_packages.Rename.test_package_names) ... ok
test_documented (test_release_packages.Rename.test_documented)
The documented behavior. ... ok
test_broken (test_release_packages.Rename.test_broken) ... FAIL
test_crashed (test_release_packages.Rename.test_crashed) ... ERROR
test_later (test_release_packages.Rename.test_later) ... skipped 'not yet'

======================================================================
FAIL: test_broken (test_release_packages.Rename.test_broken)
----------------------------------------------------------------------
Ran 5 tests in 0.004s

FAILED (failures=1, errors=1, skipped=1)
`
	r, err := parseResults("python-unittest", []byte(output))
	if err == nil || !strings.Contains(err.Error(), "tests failed") {
		t.Fatal(err)
	}
	if r.Passed != 2 || r.Failed != 2 || r.Skipped != 1 || len(r.Cases) != 5 {
		t.Fatal(r)
	}
	statuses := map[string]string{}
	for _, c := range r.Cases {
		statuses[c.ID] = c.Status
	}
	if statuses["test_release_packages.Rename.test_documented"] != "passed" || statuses["test_release_packages.Rename.test_crashed"] != "failed" || statuses["test_release_packages.Rename.test_later"] != "skipped" {
		t.Fatal(statuses)
	}
	passing := "test_a (m.C.test_a) ... ok\n\nRan 1 test in 0.000s\n\nOK\n"
	if r, err := parseResults("python-unittest", []byte(passing)); err != nil || r.Executed != 1 {
		t.Fatal(r, err)
	}
	for name, broken := range map[string]string{
		"missing summary": "test_a (m.C.test_a) ... ok\n",
		"count mismatch":  "test_a (m.C.test_a) ... ok\nRan 2 tests in 0.000s\n",
		"no outcome":      "test_a (m.C.test_a)\nRan 1 test in 0.000s\n",
		"zero tests":      "Ran 0 tests in 0.000s\n\nOK\n",
	} {
		if _, err := parseResults("python-unittest", []byte(broken)); err == nil {
			t.Fatal(name, "accepted")
		}
	}
}
