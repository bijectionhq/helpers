package verification

import (
	"errors"
	"strings"
	"testing"
)

func TestFailedCaseDiagnosticsFollowRunnerIdentity(t *testing.T) {
	for _, test := range []struct{ format, data, id, detail string }{
		{"go-test-json", `{"Action":"output","Package":"p","Test":"TestFail","Output":"assertion: expected refusal\n"}
{"Action":"output","Package":"p","Test":"TestPass","Output":"unrelated output\n"}
{"Action":"pass","Package":"p","Test":"TestPass"}
{"Action":"fail","Package":"p","Test":"TestFail"}`, "p::TestFail", "expected refusal"},
		{"vitest-json", `{"success":false,"numPassedTests":1,"numFailedTests":1,"testResults":[{"name":"auth.test.ts","assertionResults":[{"fullName":"rejects","status":"failed","failureMessages":["AssertionError: expected refusal"]},{"fullName":"accepts","status":"passed"}]}]}`, "auth.test.ts::rejects", "expected refusal"},
		{"rust-test-json", `{"type":"suite","event":"started","test_count":1}
{"type":"test","event":"started","name":"rejects"}
{"type":"test","event":"failed","name":"rejects","stdout":"assertion failed: expected refusal"}
{"type":"suite","event":"failed","passed":0,"failed":1,"ignored":0}`, "rejects", "expected refusal"},
		{"node-test-tap", "TAP version 13\n# Subtest: auth\n    # Subtest: rejects\n    not ok 1 - rejects\n      ---\n      error: expected refusal\n      ...\nnot ok 1 - auth\n  ---\n  error: child failed\n  ...\n# tests 2\n# pass 0\n# fail 2\n# skipped 0\n# cancelled 0\n# todo 0\n", "auth/rejects", "expected refusal"},
	} {
		t.Run(test.format, func(t *testing.T) {
			results, err := parseResults(test.format, []byte(test.data))
			if !errors.Is(err, ErrTestsFailed) {
				t.Fatalf("valid failure treated as malformed: %v", err)
			}
			found := false
			for _, result := range results.Cases {
				if result.ID == test.id {
					found = true
					if !strings.Contains(result.Diagnostic, test.detail) || strings.Contains(result.Diagnostic, "unrelated") {
						t.Fatal(result)
					}
				} else if strings.Contains(result.Diagnostic, test.detail) {
					t.Fatal("diagnostic attributed to another case", result)
				}
			}
			if !found {
				t.Fatal(results)
			}
		})
	}
	malformed := `{"success":false,"numFailedTests":2,"testResults":[{"name":"a","assertionResults":[{"fullName":"b","status":"failed"}]}]}`
	if _, err := parseResults("vitest-json", []byte(malformed)); err == nil || errors.Is(err, ErrTestsFailed) {
		t.Fatal("invalid totals treated as a valid failure", err)
	}
}

func TestDiagnosticObservationIsBoundedAndOutsideEvidenceIdentity(t *testing.T) {
	detail := diagnostic(strings.Repeat("x", 8192) + " useful assertion")
	if len(detail) > 4200 || !strings.Contains(detail, "useful assertion") {
		t.Fatal(len(detail), detail)
	}
	receipt := &Receipt{Results: Results{Cases: []Case{{ID: "p::test", Status: "failed", Diagnostic: "attempt 1"}}}}
	if err := receipt.seal(); err != nil {
		t.Fatal(err)
	}
	evidence, exact := receipt.EvidenceSHA256, receipt.ReceiptSHA256
	receipt.Results.Cases[0].Diagnostic = "attempt 2"
	if err := receipt.seal(); err != nil {
		t.Fatal(err)
	}
	if receipt.EvidenceSHA256 != evidence || receipt.ReceiptSHA256 == exact || receipt.Results.Cases[0].Diagnostic != "attempt 2" {
		t.Fatal("observation changed deterministic evidence or was erased")
	}
}
