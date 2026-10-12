package verification

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A verbose unittest outcome line, "name (module.Class.name) ... outcome";
// a docstring puts its first line between the identity and the outcome.
var (
	unittestCase    = regexp.MustCompile(`^(\w+) \(([\w.]+)\)(?: \.\.\. (.+))?$`)
	unittestOutcome = regexp.MustCompile(`^.* \.\.\. (ok|FAIL|ERROR|skipped.*|expected failure|unexpected success)$`)
	unittestRan     = regexp.MustCompile(`^Ran (\d+) tests? in `)
)

// pythonResults reads `python -m unittest -v` output routed to stdout. A case
// is its module.Class.test identity; the "Ran N tests" summary must agree with
// the cases, and a missing summary is never a pass.
func pythonResults(data []byte) (Results, error) {
	r := Results{Format: "python-unittest"}
	ran, pending := -1, ""
	record := func(id, outcome string) error {
		status := ""
		switch {
		case outcome == "ok":
			status, r.Passed = "passed", r.Passed+1
		case outcome == "FAIL", outcome == "ERROR", outcome == "unexpected success":
			status, r.Failed = "failed", r.Failed+1
		case strings.HasPrefix(outcome, "skipped"), outcome == "expected failure":
			status, r.Skipped = "skipped", r.Skipped+1
		default:
			return fmt.Errorf("unknown unittest outcome %q", outcome)
		}
		r.Cases = append(r.Cases, Case{ID: id, Status: status})
		return nil
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 4096), 4<<20)
	for s.Scan() {
		line := s.Text()
		if m := unittestCase.FindStringSubmatch(line); m != nil {
			if pending != "" {
				return r, fmt.Errorf("unittest case %s has no outcome", pending)
			}
			if m[3] == "" {
				pending = m[2]
				continue
			}
			if err := record(m[2], m[3]); err != nil {
				return r, err
			}
			continue
		}
		if pending != "" {
			if m := unittestOutcome.FindStringSubmatch(line); m != nil {
				if err := record(pending, m[1]); err != nil {
					return r, err
				}
				pending = ""
			}
			continue
		}
		if m := unittestRan.FindStringSubmatch(line); m != nil {
			if ran >= 0 {
				return r, fmt.Errorf("duplicate unittest summary")
			}
			ran, _ = strconv.Atoi(m[1])
		}
	}
	if err := s.Err(); err != nil {
		return r, err
	}
	if pending != "" {
		return r, fmt.Errorf("unittest case %s has no outcome", pending)
	}
	if ran < 0 {
		return r, fmt.Errorf("missing unittest summary")
	}
	if ran != len(r.Cases) {
		return r, fmt.Errorf("unittest summary counts %d tests but %d reported outcomes", ran, len(r.Cases))
	}
	return r, nil
}
