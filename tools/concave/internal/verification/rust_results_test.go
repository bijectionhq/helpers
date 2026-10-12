package verification

import (
	"concave/internal/policy"
	"strings"
	"testing"
)

const rustPass = `{"type":"suite","event":"started","test_count":1}
{"type":"test","event":"started","name":"tests::reads"}
{"type":"test","event":"ok","name":"tests::reads"}
{"type":"suite","event":"ok","passed":1,"failed":0,"ignored":0}
`

func TestRustWitnessRequiresExactExecutedCase(t *testing.T) {
	c := policy.CheckDefinition{Results: "rust-test-json", Target: "store/lib", Witnesses: map[string][]string{"file.rs#reads": {"store/lib::tests::reads"}}}
	r, err := checkResults(c, []byte(rustPass))
	if err != nil || r.Executed != 1 || len(r.Cases) != 1 {
		t.Fatal(r, err)
	}
	for _, data := range []string{
		strings.ReplaceAll(rustPass, "tests::reads", "tests::other"),
		strings.Split(rustPass, `{"type":"suite","event":"ok"`)[0],
		rustPass + rustPass,
		strings.Replace(rustPass, `"passed":1`, `"passed":2`, 1),
		`{"type":"suite","event":"started","test_count":0}` + "\n" + `{"type":"suite","event":"ok","passed":0,"failed":0,"ignored":0}`,
		`{"type":"suite","event":"started","test_count":1}` + "\n" + `{"type":"test","event":"ignored","name":"tests::reads"}` + "\n" + `{"type":"suite","event":"ok","passed":0,"failed":0,"ignored":1}`,
	} {
		if _, err := checkResults(c, []byte(data)); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestStructuredCaseIdentities(t *testing.T) {
	for _, tc := range []struct{ format, data, id string }{
		{"go-test-json", `{"Action":"pass","Package":"pkg","Test":"TestRead"}`, "pkg::TestRead"},
		{"vitest-json", `{"success":true,"numPassedTests":1,"testResults":[{"name":"read.test.ts","assertionResults":[{"fullName":"reader reads","status":"passed"}]}]}`, "read.test.ts::reader reads"},
		{"jest-json", `{"numFailedTestSuites":0,"numFailedTests":0,"numPassedTestSuites":1,"numPassedTests":1,"numPendingTests":0,"numRuntimeErrorTestSuites":0,"numTodoTests":0,"numTotalTests":1,"success":true,"testResults":[{"name":"Dialog.test.tsx","status":"passed","assertionResults":[{"ancestorTitles":["Dialog"],"fullName":"Dialog rotates the listed credential","status":"passed","title":"rotates the listed credential","failureMessages":[]}]}]}`, "Dialog.test.tsx::Dialog rotates the listed credential"},
		{"node-test-tap", "TAP version 13\n# Subtest: reads\nok 1 - reads\n# tests 1\n# pass 1\n# fail 0\n# skipped 0\n# cancelled 0\n# todo 0\n", "reads"},
	} {
		r, err := parseResults(tc.format, []byte(tc.data))
		if err != nil || len(r.Cases) != 1 || r.Cases[0].ID != tc.id {
			t.Fatal(r, err)
		}
	}
}

// Jest reports absolute suite paths like Vitest; a suite that fails to load
// has no failed assertion and is still a failure.
func TestJestWitnessUsesRepositoryRelativeIdentityAndRejectsABrokenSuite(t *testing.T) {
	check := policy.CheckDefinition{Results: "jest-json", Witnesses: map[string][]string{"app/Dialog.test.tsx::rotatesthelistedcredential": {"app/Dialog.test.tsx::Dialog rotates the listed credential"}}}
	data := []byte(`{"success":true,"numPassedTests":1,"numPendingTests":1,"testResults":[{"name":"/checkout/app/Dialog.test.tsx","assertionResults":[{"fullName":"Dialog rotates the listed credential","status":"passed"},{"fullName":"Dialog later","status":"pending"}]}]}`)
	result, err := checkResults(check, data, "/checkout")
	if err != nil || result.Passed != 1 || result.Skipped != 1 || result.Cases[1].ID != "app/Dialog.test.tsx::Dialog rotates the listed credential" {
		t.Fatal(result, err)
	}
	if _, err := checkResults(check, data, "/elsewhere"); err == nil {
		t.Fatal("unrelated source root satisfied witness")
	}
	broken := []byte(`{"success":false,"numFailedTestSuites":1,"numRuntimeErrorTestSuites":1,"numPassedTests":0,"testResults":[{"name":"/checkout/app/Dialog.test.tsx","status":"failed","message":"Cannot find module","assertionResults":[]}]}`)
	if _, err := checkResults(check, broken, "/checkout"); err == nil || !strings.Contains(err.Error(), "Jest suite failed") {
		t.Fatal("a suite that did not load passed", err)
	}
}

// test.each rows may share one title. Each execution keeps an identity: the
// first the title, later ones numbered in report order, so a mapping can
// require a particular row and totals still agree with the cases.
func TestRepeatedTitlesAreNumberedInReportOrder(t *testing.T) {
	data := []byte(`{"success":false,"numPassedTests":2,"numFailedTests":1,"testResults":[{"name":"/checkout/app/config.test.ts","assertionResults":[{"fullName":"refuses invalid configuration","status":"passed"},{"fullName":"refuses invalid configuration","status":"failed","failureMessages":["second row"]},{"fullName":"refuses invalid configuration","status":"passed"}]}]}`)
	for _, format := range []string{"jest-json", "vitest-json"} {
		result, err := checkResults(policy.CheckDefinition{Results: format}, data, "/checkout")
		if err == nil || !strings.Contains(err.Error(), "tests failed") || len(result.Cases) != 3 {
			t.Fatal(format, result, err)
		}
		statuses := map[string]string{}
		for _, c := range result.Cases {
			statuses[c.ID] = c.Status
		}
		for id, status := range map[string]string{
			"app/config.test.ts::refuses invalid configuration":    "passed",
			"app/config.test.ts::refuses invalid configuration #2": "failed",
			"app/config.test.ts::refuses invalid configuration #3": "passed",
		} {
			if statuses[id] != status {
				t.Fatal(format, statuses)
			}
		}
	}
}

func TestVitestWitnessUsesRepositoryRelativeIdentity(t *testing.T) {
	check := policy.CheckDefinition{Results: "vitest-json", Witnesses: map[string][]string{"src/read.test.ts": {"src/read.test.ts::reader reads"}}}
	data := []byte(`{"success":true,"numPassedTests":1,"testResults":[{"name":"/checkout/src/read.test.ts","assertionResults":[{"fullName":"reader reads","status":"passed"}]}]}`)
	result, err := checkResults(check, data, "/checkout")
	if err != nil || result.Cases[0].ID != "src/read.test.ts::reader reads" {
		t.Fatal(result, err)
	}
	if _, err := checkResults(check, data, "/elsewhere"); err == nil {
		t.Fatal("unrelated source root satisfied witness")
	}
}
