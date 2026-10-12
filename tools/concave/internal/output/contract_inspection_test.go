package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
)

func TestContractInspectionBoundsEntriesWithoutLosingWitnessScope(t *testing.T) {
	witness := analysis.Witness{Reference: "tests.rs::safe", Path: "tests.rs", Symbol: "safe", Resolution: "symbol_found"}
	missing := analysis.Witness{Reference: "tests.rs::missing", Path: "tests.rs", Symbol: "missing", Resolution: "symbol_not_resolved"}
	view := &analysis.ContractContext{
		Witnesses:   []analysis.Witness{witness, witness},
		Definitions: map[string]analysis.Obligation{"core.rs#safe": {Text: "Preserve the invariant."}},
		Files: []analysis.FileContracts{
			{Path: "empty.rs"},
			{Path: "core.rs",
				Obligations: []analysis.Obligation{{Reference: "core.rs#safe", Scope: "declaration", Path: "core.rs", Line: 2}},
				Exceptions:  []analysis.ScopedException{{ID: "native", Start: 5, End: 9, Explanation: policy.Explanation{Why: "Keep native behavior."}}},
				Witnesses:   []analysis.Witness{witness, witness, missing},
			},
			{Path: "other.rs", Witnesses: []analysis.Witness{witness}},
		},
		Limits: []string{"Discovery is not execution."},
	}
	before, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name         string
		top          int
		want, absent []string
	}{
		{"one", 1, []string{"Case witness tests.rs::safe: symbol_found; executed=false", "5 more contract entries"}, []string{"core.rs\n", "other.rs\n", "Keep native behavior."}},
		{"three", 3, []string{"core.rs\n", "core.rs#safe [declaration]", "exception native:5-9", "3 more contract entries"}, []string{"other.rs\n", "tests.rs::missing"}},
		{"all", 0, []string{"other.rs\n  witness tests.rs::safe", "tests.rs::missing: symbol_not_resolved; executed=false"}, []string{"more contract entries"}},
		{"exact", 6, []string{"other.rs\n  witness tests.rs::safe"}, []string{"more contract entries"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			Contracts(&out, view, test.top)
			for _, want := range append(test.want, "Limit: Discovery is not execution.") {
				if !strings.Contains(out.String(), want) {
					t.Fatal(want, out.String())
				}
			}
			for _, absent := range append(test.absent, "empty.rs", "executed=true") {
				if strings.Contains(out.String(), absent) {
					t.Fatal(absent, out.String())
				}
			}
			if test.top == 0 && strings.Count(out.String(), "tests.rs::safe: symbol_found") != 3 {
				t.Fatal("lost applicability or repeated the same witness within one owner", out.String())
			}
		})
	}
	after, err := json.Marshal(view)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("presentation changed evidence", err)
	}
}

func TestHistoricalWitnessDoesNotClaimCurrentEvidence(t *testing.T) {
	witness := analysis.Witness{ObservedExecution: true, Evidence: "historical", Mutation: "witnessed"}
	text := executedText(witness)
	if !strings.Contains(text, "historical observation") || !strings.Contains(text, "not revalidated") || witness.Executed {
		t.Fatal(text)
	}
}

func TestContractInspectionEmptyContextIsExplicit(t *testing.T) {
	var out bytes.Buffer
	Contracts(&out, nil, 1)
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
	Contracts(&out, &analysis.ContractContext{Files: []analysis.FileContracts{{Path: "empty.rs"}}}, 1)
	if !strings.Contains(out.String(), "No discovered obligations, divergence explanations or witnesses in this scope.") || strings.Contains(out.String(), "empty.rs") {
		t.Fatal(out.String())
	}
}
