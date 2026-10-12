package verification

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
)

// One named check owns one harness. Compiler output belongs on stderr; Cargo's
// --message-format=json is deliberately not interpreted as test evidence.
func rustResults(data []byte) (Results, error) {
	r := Results{Format: "rust-test-json"}
	started, ended := false, false
	count := 0
	pending := map[string]bool{}
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 4096), 4<<20)
	for s.Scan() {
		var e struct {
			Type, Event, Name, Stdout string
			TestCount                 int `json:"test_count"`
			Passed, Failed, Ignored   int
		}
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			return r, fmt.Errorf("invalid Rust test JSON: %w", err)
		}
		if ended {
			return r, fmt.Errorf("multiple or trailing Rust test suites")
		}
		switch e.Type {
		case "suite":
			switch e.Event {
			case "started":
				if started {
					return r, fmt.Errorf("multiple Rust test suites")
				}
				started = true
				count = e.TestCount
			case "ok", "failed":
				if !started || len(pending) > 0 || count != r.Passed+r.Failed+r.Skipped || e.Passed != r.Passed || e.Failed != r.Failed || e.Ignored != r.Skipped {
					return r, fmt.Errorf("incomplete or inconsistent Rust test suite")
				}
				ended = true
				if e.Event == "failed" && r.Failed == 0 {
					return r, fmt.Errorf("Rust harness failed")
				}
			default:
				return r, fmt.Errorf("unknown Rust suite event %q", e.Event)
			}
		case "test":
			if !started || e.Name == "" {
				return r, fmt.Errorf("Rust test outside named suite")
			}
			switch e.Event {
			case "started":
				if pending[e.Name] {
					return r, fmt.Errorf("duplicate Rust test start")
				}
				pending[e.Name] = true
			case "ok", "failed", "ignored":
				if e.Event != "ignored" && !pending[e.Name] {
					return r, fmt.Errorf("Rust test completed without start: %s", e.Name)
				}
				delete(pending, e.Name)
				status := map[string]string{"ok": "passed", "failed": "failed", "ignored": "skipped"}[e.Event]
				r.Cases = append(r.Cases, Case{ID: e.Name, Status: status, Diagnostic: failureDiagnostic(status == "failed", e.Stdout)})
				switch status {
				case "passed":
					r.Passed++
				case "failed":
					r.Failed++
				case "skipped":
					r.Skipped++
				}
			default:
				return r, fmt.Errorf("unknown Rust test event %q", e.Event)
			}
		default:
			return r, fmt.Errorf("unknown Rust event type %q", e.Type)
		}
	}
	if err := s.Err(); err != nil {
		return r, err
	}
	if !ended {
		return r, fmt.Errorf("missing Rust suite completion")
	}
	return r, nil
}
