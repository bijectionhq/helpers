package analysis

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestCallerSelectionPrioritizesRawChangesAndExplainsBounds(t *testing.T) {
	f := testrepo.New(t)
	for _, path := range []string{"a.ts", "b.ts", "z.ts"} {
		f.Write(path, "export const value=1;\n")
	}
	base := f.Commit()
	f.Write("z.ts", "export const value=2;\n")
	repo, _ := f.Open()
	snapshot, err := repository.OpenSnapshot(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.Tree(base)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string][]Location{"a.ts": {{Path: "a.ts", Line: 1}}, "b.ts": {{Path: "b.ts", Line: 1}}, "z.ts": {{Path: "z.ts", Line: 1}}}
	for _, test := range []struct {
		name     string
		budget   callerBudget
		scope    []string
		want     []string
		excluded int
		reason   CallerOmissionReason
	}{
		{"files", callerBudget{1, 1000, 100}, nil, []string{"z.ts"}, 0, "file_bound"},
		{"bytes", callerBudget{10, 22, 100}, nil, []string{"z.ts"}, 0, "byte_bound"},
		{"locations", callerBudget{10, 1000, 1}, nil, []string{"z.ts"}, 0, "location_bound"},
		{"explicit", callerBudget{1, 1000, 100}, []string{"a.ts"}, []string{"a.ts"}, 2, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates, err := selectCallers(snapshot, tree, paths, test.scope, test.budget)
			resolved := map[string]FileContracts{}
			got := []string{}
			for _, candidate := range candidates {
				if len(candidate.locations) > 0 {
					got = append(got, candidate.path)
					resolved[candidate.path] = FileContracts{Path: candidate.path}
				}
			}
			coverage := callerCoverage(candidates, resolved, test.scope, test.budget)
			if err != nil || !reflect.DeepEqual(got, test.want) || coverage.ExcludedFiles != test.excluded {
				t.Fatal(got, coverage, err)
			}
			for _, omission := range coverage.Omissions {
				if omission.Reason != test.reason || omission.Locations != 1 {
					t.Fatal(omission)
				}
			}
			if coverage.ResolvedFiles+coverage.UnresolvedFiles+coverage.ExcludedFiles != len(paths) {
				t.Fatal("coverage does not partition input", coverage)
			}
		})
	}
	if _, err = selectCallers(snapshot, tree, paths, []string{"absent/"}, callerBudget{1, 100, 1}); err == nil {
		t.Fatal("empty scope accepted")
	}
	scope, err := normalizeCallerScope([]string{"z.ts", "a.ts", "z.ts"})
	if err != nil || !reflect.DeepEqual(scope, []string{"a.ts", "z.ts"}) {
		t.Fatal(scope, err)
	}
	if _, err = normalizeCallerScope([]string{"../"}); err == nil {
		t.Fatal("unsafe scope accepted")
	}
}

func TestCallerSelectionExplainsUnavailableSymlink(t *testing.T) {
	f := testrepo.New(t)
	f.Write("source.ts", "export const value=1;\n")
	base := f.Commit()
	if err := os.Symlink("source.ts", filepath.Join(f.Root, "link.ts")); err != nil {
		t.Fatal(err)
	}
	repo, _ := f.Open()
	snapshot, err := repository.OpenSnapshot(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.Tree(base)
	if err != nil {
		t.Fatal(err)
	}
	budget := callerBudget{1, 1000, 10}
	selected, err := selectCallers(snapshot, tree, map[string][]Location{"link.ts": {{Path: "link.ts", Line: 1}}}, nil, budget)
	coverage := callerCoverage(selected, nil, nil, budget)
	if err != nil || coverage.ResolvedFiles != 0 || len(coverage.Omissions) != 1 || coverage.Omissions[0].Reason != "source_unavailable" || !strings.Contains(coverage.Omissions[0].Detail, "bounded regular UTF-8 source") {
		t.Fatal(selected, coverage, err)
	}
}

func TestCallerCoverageDerivesOutcomesAndActionOnce(t *testing.T) {
	candidates := []callerCandidate{
		{path: "unavailable.ts", totalLocations: 1, reason: callerSourceUnavailable},
		{path: "partial.ts", sourceBytes: 3, totalLocations: 3, locations: []Location{{Line: 1}, {Line: 2}}, reason: callerLocationBound},
		{path: "bounded.ts", sourceBytes: 3, totalLocations: 1, reason: callerFileBound},
		{path: "invalid.ts", sourceBytes: 3, totalLocations: 1, locations: []Location{{Line: 1}}},
		{path: "excluded.ts", totalLocations: 4, excluded: true},
	}
	budget := callerBudget{1, 100, 2}
	resolved := map[string]FileContracts{"partial.ts": {Path: "partial.ts"}}
	coverage := callerCoverage(candidates, resolved, nil, budget)
	if coverage.ResolvedFiles != 1 || coverage.ResolvedLocations != 2 || coverage.ResolvedBytes != 3 || coverage.UnresolvedFiles != 3 || coverage.ExcludedLocations != 4 || coverage.OmittedLocations != 4 {
		t.Fatal(coverage)
	}
	if coverage.TotalLocations != coverage.ResolvedLocations+coverage.OmittedLocations+coverage.ExcludedLocations || coverage.TotalFiles != coverage.ResolvedFiles+coverage.UnresolvedFiles+coverage.ExcludedFiles {
		t.Fatal("coverage does not partition outcomes", coverage)
	}
	if coverage.NextAction == nil || coverage.NextAction.Kind != NarrowCallerScope || coverage.NextAction.Path != "bounded.ts" {
		t.Fatal("unavailable, invalid or oversized source suggested", coverage.NextAction)
	}
	coverage = callerCoverage(candidates, resolved, []string{"bounded.ts"}, budget)
	if coverage.NextAction.Kind != InspectCallerGaps {
		t.Fatal("same scope suggested forever", coverage.NextAction)
	}
	if coverage.Omissions[3].Reason != callerInvalidContract {
		t.Fatal("invalid contract counted as resolved", coverage)
	}
}
