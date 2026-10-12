package analysis

import (
	"reflect"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
)

func TestComparisonSeparatesHistoryFromSourceScope(t *testing.T) {
	before := Report{Format: 1, Kind: "concave-inspection", Files: []Row{{Path: "a"}}, Findings: []policy.Finding{{Path: "a", Kind: "drift"}}}
	for _, scope := range []OptionsJSON{{History: true}, {History: true, HistoryPairs: []HistoryPair{{Block: "block", Hunk: "hunk"}}}} {
		after := before
		after.Scope = scope
		after.Findings = nil
		got, err := Compare(&before, &after)
		if err != nil || !got.Comparable || !reflect.DeepEqual(got.BasisChanges, []string{"history"}) || len(got.ResolvedFindings) != 1 || len(got.Files) != 0 {
			t.Fatal(got, err)
		}
		if !reflect.DeepEqual(after.Scope, scope) {
			t.Fatal("comparison mutated diagnostic scope")
		}
	}
	for _, scope := range []OptionsJSON{{Paths: []string{"a"}}, {Family: "other"}, {Case: "other"}} {
		after := before
		after.Scope = scope
		after.Findings = nil
		got, err := Compare(&before, &after)
		if err != nil || got.Comparable || len(got.ResolvedFindings) != 0 {
			t.Fatal("source scope change lost", got, err)
		}
	}
}

func TestComparisonUsesRegularFileIdentityWithoutWeakeningOtherStates(t *testing.T) {
	blob := &repository.State{Kind: "blob", Mode: "100644", OID: "same"}
	file := &repository.State{Kind: "file", Mode: "100644", OID: "same", SHA256: "digest", Size: 12}
	for _, tc := range []struct {
		name          string
		before, after *repository.State
		changed       bool
	}{
		{"loaded-bytes", blob, file, false},
		{"changed-bytes", file, &repository.State{Kind: "file", Mode: "100644", OID: "different", SHA256: "other", Size: 12}, true},
		{"executable-mode", blob, &repository.State{Kind: "blob", Mode: "100755", OID: "same"}, true},
		{"missing-file", file, nil, true},
		{"unknown-object", file, &repository.State{Kind: "file", Mode: "100644", SHA256: "digest", Size: 12}, true},
		{"oversize", blob, &repository.State{Kind: "oversize", Mode: "100644", OID: "same"}, true},
		{"symbolic-link", blob, &repository.State{Kind: "blob", Mode: "120000", OID: "same"}, true},
		{"nonregular", blob, &repository.State{Kind: "nonregular", Mode: "100644", OID: "same"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := Report{Format: 1, Kind: "concave-inspection", Files: []Row{{Path: "a", Selected: tc.before, Upstream: tc.before}}}
			after := before
			after.Files = []Row{{Path: "a", Selected: tc.after, Upstream: tc.after}}
			for _, pair := range [][2]*Report{{&before, &after}, {&after, &before}} {
				got, err := Compare(pair[0], pair[1])
				if err != nil || !got.Comparable || (len(got.Files) != 0) != tc.changed {
					t.Fatal(got, err)
				}
			}
		})
	}
	if repository.Same(blob, file) {
		t.Fatal("strict source validation was weakened")
	}
}
