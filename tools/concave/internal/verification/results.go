package verification

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Results struct {
	Format   string `json:"format"`
	Passed   int    `json:"passed"`
	Failed   int    `json:"failed"`
	Skipped  int    `json:"skipped"`
	Executed int    `json:"executed"`
	Cases    []Case `json:"cases,omitempty"`
}

// ID is runner-qualified within a named check: Go package::test, Vitest or
// Jest file::fullName, TAP nesting/name, Rust package/target::test, or Python
// module.Class.test.
type Case struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

var ErrTestsFailed = errors.New("tests failed")

func parseResults(format string, data []byte) (Results, error) {
	r, err := decodeResults(format, data)
	sort.Slice(r.Cases, func(i, j int) bool { return r.Cases[i].ID < r.Cases[j].ID })
	for i, c := range r.Cases {
		if c.ID == "" || i > 0 && r.Cases[i-1].ID == c.ID {
			err = fmt.Errorf("missing or duplicate test identity: %q", c.ID)
		}
	}
	// Keep the reported outcomes useful even when the report is rejected.
	// Failure receipts and terminal output share this accounting.
	r.Executed = r.Passed + r.Failed
	if err != nil || format == "command" {
		return r, err
	}
	if len(r.Cases) != r.Passed+r.Failed+r.Skipped {
		return r, fmt.Errorf("test report omits case identities")
	}
	passed, failed, skipped := 0, 0, 0
	for _, c := range r.Cases {
		switch c.Status {
		case "passed":
			passed++
		case "failed":
			failed++
		case "skipped":
			skipped++
		default:
			return r, fmt.Errorf("unknown test outcome %q", c.Status)
		}
	}
	if passed != r.Passed || failed != r.Failed || skipped != r.Skipped {
		return r, fmt.Errorf("test identities disagree with reported totals")
	}
	if r.Executed == 0 {
		return r, fmt.Errorf("zero executed tests is not verification")
	}
	if r.Failed > 0 {
		return r, fmt.Errorf("%d %w", r.Failed, ErrTestsFailed)
	}
	return r, nil
}

func decodeResults(format string, data []byte) (Results, error) {
	r := Results{Format: format}
	if format == "command" {
		return r, nil
	}
	switch format {
	case "go-test-json":
		outcomes := map[string]string{}
		diagnostics := map[string]string{}
		started := map[string]bool{}
		packages := map[string]bool{}
		parents := map[string]bool{}
		s := bufio.NewScanner(bytes.NewReader(data))
		s.Buffer(make([]byte, 4096), 4<<20)
		for s.Scan() {
			var e struct{ Action, Package, Test, Output string }
			if err := json.Unmarshal(s.Bytes(), &e); err != nil {
				return r, fmt.Errorf("invalid Go test JSON: %w", err)
			}
			if e.Test == "" && e.Action == "output" && strings.HasPrefix(e.Output, "ok") && strings.Contains(e.Output, "(cached)") {
				return r, fmt.Errorf("cached Go results are not a fresh execution; configure go test -count=1")
			}
			if e.Test == "" {
				if e.Action == "start" {
					packages[e.Package] = false
				}
				if e.Action == "pass" || e.Action == "fail" || e.Action == "skip" {
					packages[e.Package] = true
				}
				continue
			}
			key := e.Package + "::" + e.Test
			if e.Action == "output" {
				diagnostics[key] = diagnostic(diagnostics[key] + e.Output)
			}
			if e.Action == "run" {
				started[key] = true
			}
			if e.Action == "pass" || e.Action == "fail" || e.Action == "skip" {
				if _, exists := outcomes[key]; exists {
					return r, fmt.Errorf("duplicate Go test outcome: %s", key)
				}
				outcomes[key] = e.Action
			}
			for name := e.Test; strings.Contains(name, "/"); {
				name = name[:strings.LastIndex(name, "/")]
				parents[e.Package+"::"+name] = true
			}
		}
		if err := s.Err(); err != nil {
			return r, err
		}
		for key := range started {
			if _, ok := outcomes[key]; !ok {
				return r, fmt.Errorf("incomplete Go test: %s", key)
			}
		}
		for name, ended := range packages {
			if !ended {
				return r, fmt.Errorf("incomplete Go package: %s", name)
			}
		}
		for key, status := range outcomes {
			if parents[key] {
				continue
			}
			r.Cases = append(r.Cases, Case{ID: key, Status: map[string]string{"pass": "passed", "fail": "failed", "skip": "skipped"}[status], Diagnostic: failureDiagnostic(status == "fail", diagnostics[key])})
			switch status {
			case "pass":
				r.Passed++
			case "fail":
				r.Failed++
			case "skip":
				r.Skipped++
			}
		}
	// Jest's --json report and Vitest's JSON reporter share one shape.
	case "vitest-json", "jest-json":
		runner := map[string]string{"vitest-json": "Vitest", "jest-json": "Jest"}[format]
		var v struct {
			Success     bool `json:"success"`
			Passed      int  `json:"numPassedTests"`
			Failed      int  `json:"numFailedTests"`
			Skipped     int  `json:"numPendingTests"`
			Todo        int  `json:"numTodoTests"`
			TestResults []struct {
				Name       string `json:"name"`
				Assertions []struct {
					Status          string   `json:"status"`
					FullName        string   `json:"fullName"`
					FailureMessages []string `json:"failureMessages"`
				} `json:"assertionResults"`
			} `json:"testResults"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return r, err
		}
		// Rows of one test.each may share a title, and nothing else names them:
		// a repeat within its file is numbered in report order from the second.
		repeats := map[string]int{}
		for _, suite := range v.TestResults {
			for _, test := range suite.Assertions {
				if suite.Name != "" && test.FullName != "" {
					status := test.Status
					if status == "pending" || status == "todo" {
						status = "skipped"
					}
					id := suite.Name + "::" + test.FullName
					if repeats[id]++; repeats[id] > 1 {
						id = fmt.Sprintf("%s #%d", id, repeats[id])
					}
					r.Cases = append(r.Cases, Case{ID: id, Status: status, Diagnostic: failureDiagnostic(status == "failed", strings.Join(test.FailureMessages, "\n"))})
				}
				switch test.Status {
				case "passed":
					r.Passed++
				case "failed":
					r.Failed++
				case "pending", "todo", "skipped":
					r.Skipped++
				default:
					return r, fmt.Errorf("unknown %s outcome %s", runner, test.Status)
				}
			}
		}
		if r.Passed != v.Passed || r.Failed != v.Failed || r.Skipped != v.Skipped+v.Todo {
			return r, fmt.Errorf("inconsistent %s results", runner)
		}
		if !v.Success && r.Failed == 0 {
			return r, fmt.Errorf("%s suite failed without a reported failed test", runner)
		}
	case "node-test-tap":
		counts := map[string]int{}
		s := bufio.NewScanner(bytes.NewReader(data))
		s.Buffer(make([]byte, 4096), 4<<20)
		parents := map[int]string{}
		failedCase, failedDepth, inDiagnostic := -1, 0, false
		for s.Scan() {
			line := s.Text()
			trimmed := strings.TrimSpace(line)
			depth := len(line) - len(strings.TrimLeft(line, " "))
			if failedCase >= 0 && depth > failedDepth {
				if trimmed == "---" {
					inDiagnostic = true
					continue
				}
				if trimmed == "..." {
					inDiagnostic = false
					failedCase = -1
					continue
				}
				if inDiagnostic {
					r.Cases[failedCase].Diagnostic = diagnostic(r.Cases[failedCase].Diagnostic + line + "\n")
					continue
				}
			}
			if strings.HasPrefix(trimmed, "# Subtest: ") {
				parents[depth] = strings.TrimPrefix(trimmed, "# Subtest: ")
				for d := range parents {
					if d > depth {
						delete(parents, d)
					}
				}
			}
			if strings.HasPrefix(trimmed, "ok ") || strings.HasPrefix(trimmed, "not ok ") {
				_, name, ok := strings.Cut(trimmed, " - ")
				if ok {
					status := "passed"
					if strings.HasPrefix(trimmed, "not ok") {
						status = "failed"
					}
					if strings.Contains(name, " # SKIP") || strings.Contains(name, " # TODO") {
						status = "skipped"
					}
					name, _, _ = strings.Cut(name, " # ")
					prefix := ""
					for d := 0; d < depth; d++ {
						if p, ok := parents[d]; ok {
							prefix += p + "/"
						}
					}
					r.Cases = append(r.Cases, Case{ID: prefix + name, Status: status})
					failedCase, inDiagnostic = -1, false
					if status == "failed" {
						failedCase, failedDepth = len(r.Cases)-1, depth
					}
				}
			}
			fields := strings.Fields(s.Text())
			if len(fields) != 3 || fields[0] != "#" {
				continue
			}
			switch fields[1] {
			case "tests", "pass", "fail", "skipped", "cancelled", "todo":
				n, err := strconv.Atoi(fields[2])
				if err != nil || n < 0 {
					return r, fmt.Errorf("invalid TAP count")
				}
				if _, ok := counts[fields[1]]; ok {
					return r, fmt.Errorf("duplicate TAP summary")
				}
				counts[fields[1]] = n
			}
		}
		if err := s.Err(); err != nil {
			return r, err
		}
		if len(counts) != 6 {
			return r, fmt.Errorf("missing Node TAP summary")
		}
		r.Passed = counts["pass"]
		r.Failed = counts["fail"] + counts["cancelled"]
		r.Skipped = counts["skipped"] + counts["todo"]
		if r.Passed+r.Failed+r.Skipped != counts["tests"] {
			return r, fmt.Errorf("inconsistent Node TAP results")
		}
	case "rust-test-json":
		return rustResults(data)
	case "python-unittest":
		return pythonResults(data)
	default:
		return r, fmt.Errorf("unknown result format")
	}
	return r, nil
}

// Failure output is bounded observation, never a test outcome or evidence identity.
func diagnostic(text string) string {
	const limit = 4096
	if len(text) > limit {
		text = "… truncated\n" + text[len(text)-limit:]
	}
	return strings.ToValidUTF8(text, "�")
}

func failureDiagnostic(failed bool, text string) string {
	if !failed {
		return ""
	}
	return strings.TrimSpace(diagnostic(text))
}

func (results Results) evidence() Results {
	if results.Cases != nil {
		results.Cases = append([]Case{}, results.Cases...)
		for index := range results.Cases {
			results.Cases[index].Diagnostic = ""
		}
	}
	return results
}
