package analysis

import (
	"reflect"
	"testing"

	"concave/internal/policy"
)

func TestPolicyExceptionChangesRespectBothSidesOfReviewContext(t *testing.T) {
	selected := policy.Exception{ID: "adapter", Path: "selected.rs", Start: 1, End: 2}
	outside := selected
	outside.Path = "outside.rs"
	moved := selected
	moved.Path = "elsewhere.rs"
	expanded := selected
	expanded.End++
	shrunk := expanded
	shrunk.Start++
	for _, test := range []struct {
		name          string
		before, after []policy.Exception
		kinds         []string
		excluded      int
	}{
		{"added inside", nil, []policy.Exception{selected}, []string{"exception_coverage_expanded"}, 0},
		{"added outside", nil, []policy.Exception{outside}, nil, 1},
		{"removed inside", []policy.Exception{selected}, nil, []string{"exception_removed"}, 0},
		{"removed outside", []policy.Exception{outside}, nil, nil, 1},
		{"moved out", []policy.Exception{selected}, []policy.Exception{outside}, []string{"exception_coverage_expanded", "exception_binding_changed"}, 0},
		{"moved in", []policy.Exception{outside}, []policy.Exception{selected}, []string{"exception_coverage_expanded", "exception_binding_changed"}, 0},
		{"moved outside", []policy.Exception{outside}, []policy.Exception{moved}, nil, 2},
		{"expanded", []policy.Exception{selected}, []policy.Exception{expanded}, []string{"exception_coverage_expanded", "exception_binding_changed"}, 0},
		{"shrunk", []policy.Exception{expanded}, []policy.Exception{shrunk}, []string{"exception_binding_changed"}, 0},
		{"unchanged", []policy.Exception{selected, outside}, []policy.Exception{selected, outside}, nil, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			changes, excluded := policyContractChanges(&policy.Config{Exceptions: test.before}, &policy.Config{Exceptions: test.after}, []ReviewSource{{Version: "baseline", Path: selected.Path, Unavailable: "deleted"}})
			var kinds []string
			for _, change := range changes {
				kinds = append(kinds, change.Kind)
			}
			if !reflect.DeepEqual(kinds, test.kinds) || excluded != test.excluded {
				t.Fatal(kinds, excluded)
			}
		})
	}
}

func TestPolicyGlobalChangesRemainVisibleOutsideSourceContext(t *testing.T) {
	before := &policy.Config{Cases: map[string]policy.Case{"other": {Witnesses: []string{"old.rs"}}}}
	after := &policy.Config{Contract: "new.rs#invariant", ManifestSHA256: "changed", Cases: map[string]policy.Case{"other": {Witnesses: []string{"new.rs"}}}}
	changes, excluded := policyContractChanges(before, after, nil)
	var kinds []string
	for _, change := range changes {
		kinds = append(kinds, change.Kind)
	}
	if excluded != 0 || !reflect.DeepEqual(kinds, []string{"witnesses_changed", "transformations_changed", "obligation_scope_changed"}) {
		t.Fatal(changes, excluded)
	}
}
