package verification

import (
	"reflect"
	"testing"
)

func TestVitestCountsSurviveRejectedReports(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       Results
		bad        bool
	}{
		{"failed assertion", `{"success":false,"numPassedTests":1,"numFailedTests":1,"numPendingTests":1,"numTodoTests":1,"testResults":[{"assertionResults":[{"status":"passed"},{"status":"failed"},{"status":"pending"},{"status":"todo"}]}]}`, Results{"vitest-json", 1, 1, 2, 2, nil}, true},
		{"suite failure", `{"success":false,"numPassedTests":1,"testResults":[{"assertionResults":[{"status":"passed"}]}]}`, Results{"vitest-json", 1, 0, 0, 1, nil}, true},
		{"inconsistent summary", `{"success":true,"numPassedTests":2,"testResults":[{"assertionResults":[{"status":"passed"}]}]}`, Results{"vitest-json", 1, 0, 0, 1, nil}, true},
		{"passed with skips", `{"success":true,"numPassedTests":1,"numPendingTests":1,"numTodoTests":1,"testResults":[{"assertionResults":[{"status":"passed"},{"status":"skipped"},{"status":"todo"}]}]}`, Results{"vitest-json", 1, 0, 2, 1, nil}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResults("vitest-json", []byte(tc.data))
			if !reflect.DeepEqual(got, tc.want) || (err != nil) != tc.bad {
				t.Fatalf("got %+v, %v; want %+v, rejected=%t", got, err, tc.want, tc.bad)
			}
		})
	}
}
