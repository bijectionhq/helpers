package command_test

import (
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestDerivedEntriesAreListedAndExplained(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/generated.json", "{}\n")
	f.Config.Upstream = f.Commit()
	f.Config.Checks = map[string]policy.CheckDefinition{"fresh": {Command: []string{"true"}, Directory: ".", Inputs: []string{"crates/core/generated.json"}, Toolchain: []policy.ToolCheck{{Command: []string{"true"}}}, TimeoutSeconds: 1, Results: "command"}}
	f.Config.Derived = []policy.Derived{{ID: "generated-contract", Paths: []string{"crates/core/generated.json"}, Sources: []string{"crates/core/lib.rs"}, Generator: "generate-contract", Check: "fresh", Explanation: policy.Explanation{Why: "Serialized from lib.rs declarations.", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Commit()

	n, out, errout := run(f, "list")
	if n != 0 || !strings.Contains(out, "generated-contract derives 1 inherited paths (freshness check fresh)") || !strings.Contains(out, "1 derived entries") {
		t.Fatal(n, out, errout)
	}
	n, out, errout = run(f, "list", "--json")
	var listed struct {
		Derived []policy.Derived `json:"derived"`
	}
	if n != 0 || json.Unmarshal([]byte(out), &listed) != nil || len(listed.Derived) != 1 || listed.Derived[0].Generator != "generate-contract" {
		t.Fatal(n, out, errout)
	}
	n, out, errout = run(f, "rule", "explain", "generated-contract")
	for _, want := range []string{"generated-contract (derived)", "source: crates/core/lib.rs", "generator: generate-contract", "freshness check: fresh", "verify run fresh"} {
		if n != 0 || !strings.Contains(out, want) {
			t.Fatal(want, n, out, errout)
		}
	}
	n, out, errout = run(f, "rule", "explain", "generated-contract", "--json")
	var explained struct {
		Kind    string          `json:"kind"`
		Derived *policy.Derived `json:"derived"`
	}
	if n != 0 || json.Unmarshal([]byte(out), &explained) != nil || explained.Kind != "concave-rule-explanation" || explained.Derived == nil || explained.Derived.Check != "fresh" {
		t.Fatal(n, out, errout)
	}
	if n, _, errout = run(f, "rule", "inspect", "generated-contract"); n != 2 || !strings.Contains(errout, "freshness check is fresh") {
		t.Fatal(n, errout)
	}
}
