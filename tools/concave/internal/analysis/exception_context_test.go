package analysis

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
)

func TestExceptionChangeContextPreservesFileBindingShape(t *testing.T) {
	binding := policy.Exception{ID: "retired", Path: "retired.rs", File: &policy.FileBinding{Upstream: policy.FileIdentity{SHA256: "old", Mode: "100644"}}, Reason: "Retired adapter."}
	changes := []ContractChange{{After: binding}}
	attachExceptionContext(changes, nil, nil)
	data, err := json.Marshal(changes[0].After)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"file", "explanation", "reason"} {
		if len(fields[key]) == 0 {
			t.Fatal("missing file binding context", key, string(data))
		}
	}
	for _, key := range []string{"start", "end", "fence", "upstream_sha256", "replacement_sha256", "explanation_unavailable"} {
		if len(fields[key]) != 0 {
			t.Fatal("invented span identity or missing rationale", key, string(data))
		}
	}
}

func TestExceptionChangeContextUsesCapturedVersions(t *testing.T) {
	fence := func(reason string) string {
		return "// concave:begin adapter\n// why: " + reason + "\n// witness: tests/check.rs::verify\nfn adapted() {}\n// concave:end adapter\n"
	}
	old := policy.Exception{ID: "adapter", Path: "old.rs", Fence: true, Start: 1, End: 2, ReplacementSHA256: "old-binding"}
	next := old
	next.Path, next.ReplacementSHA256 = "next.rs", "new-binding"
	for _, scenario := range []string{"captured", "consumer", "missing-source", "upstream-only", "missing-body", "unavailable-source", "missing-fence", "invalid-fence", "policy-owned"} {
		t.Run(scenario, func(t *testing.T) {
			before, after := old, next
			blobs := map[string]string{}
			capture := func(version, path, body string) ReviewSource {
				sha := repository.Digest([]byte(body))
				blobs[sha] = body
				return ReviewSource{Version: version, Path: path, SHA256: sha}
			}
			sources := []ReviewSource{capture("baseline", before.Path, fence("Previous source rationale.")), capture("candidate", after.Path, fence("Candidate source rationale."))}
			unavailable := ""
			switch scenario {
			case "consumer":
				sources[1].Version = "consumer"
			case "missing-source":
				sources = sources[:1]
				unavailable = "source not captured"
			case "upstream-only":
				sources[1].Version = "upstream"
				unavailable = "source not captured"
			case "missing-body":
				delete(blobs, sources[1].SHA256)
				unavailable = "body unavailable"
			case "unavailable-source":
				sources[1].Unavailable = "selected file deleted"
				unavailable = "selected file deleted"
			case "missing-fence":
				sources[1] = capture("candidate", after.Path, "fn restored() {}\n")
				unavailable = "named fence absent"
			case "invalid-fence":
				sources[1] = capture("candidate", after.Path, strings.Replace(fence("Ambiguous rationale."), "// witness:", "// unknown:", 1))
				unavailable = "annotations are invalid"
			case "policy-owned":
				before.Fence, after.Fence = false, false
				before.Reason, after.Reason = "Previous policy rationale.", "Candidate policy rationale."
			}
			changes := []ContractChange{{Kind: "exception_binding_changed", Path: after.Path, Before: before, After: after}, {Kind: "exception_coverage_expanded", Path: after.Path, Before: policy.Exception{}, After: after}}
			attachExceptionContext(changes, sources, blobs)
			previous := changes[0].Before.(ExceptionContext)
			current := changes[0].After.(ExceptionContext)
			if !reflect.DeepEqual(previous.Exception, before) || !reflect.DeepEqual(current.Exception, after) || changes[1].Before != nil {
				t.Fatal("binding or absent-side identity changed", changes)
			}
			oldReason, newReason := "Previous source rationale.", "Candidate source rationale."
			if scenario == "policy-owned" {
				oldReason, newReason = before.Reason, after.Reason
			}
			if previous.Explanation == nil || previous.Explanation.Why != oldReason {
				t.Fatal("baseline borrowed candidate source", previous)
			}
			if unavailable != "" {
				if current.Explanation != nil || !strings.Contains(current.ExplanationUnavailable, unavailable) {
					t.Fatal("unavailable rationale invented", current)
				}
			} else if current.Explanation == nil || current.Explanation.Why != newReason || current.ExplanationUnavailable != "" {
				t.Fatal(current)
			}
			raw, err := json.Marshal(current)
			if err != nil || !strings.Contains(string(raw), `"replacement_sha256":"new-binding"`) || strings.Contains(string(raw), `"Exception"`) {
				t.Fatal("binding fields no longer at the top level", string(raw), err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if unavailable != "" {
				if len(fields["explanation"]) != 0 || !strings.Contains(string(fields["explanation_unavailable"]), unavailable) {
					t.Fatal("JSON lost unavailable rationale", string(raw))
				}
			} else if !strings.Contains(string(fields["explanation"]), newReason) || len(fields["explanation_unavailable"]) != 0 {
				t.Fatal("JSON lost captured rationale", string(raw))
			}
		})
	}
}
