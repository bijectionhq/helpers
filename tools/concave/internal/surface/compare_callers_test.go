package surface_test

import (
	"concave/internal/surface"
	"strings"
	"testing"
)

func TestCallerComparisonSeparatesLocationsBindingsAndCoverage(t *testing.T) {
	f := fixture(t)
	source := "import { renamed as use } from './entry.js';\nuse('x');\n"
	f.Write("use.ts", source)
	repo, _ := f.Open()
	capture := func() *surface.Report {
		t.Helper()
		r, e := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	before := capture()
	f.Write("notes.md", "documentation\n")
	f.Commit()
	same := capture()
	comparison, err := surface.Compare(before, same)
	if err != nil || !comparison.Comparable || len(comparison.Callers) != 0 || comparison.BeforeHead == comparison.AfterHead {
		t.Fatal(comparison, err)
	}
	f.Write("use.ts", "// shifted\n"+strings.ReplaceAll(source, "use", "call"))
	after := capture()
	comparison, err = surface.Compare(same, after)
	if err != nil || !comparison.Comparable || len(comparison.Callers) != 3 {
		t.Fatal(comparison, err)
	}
	kinds := map[string]bool{}
	for _, change := range comparison.Callers {
		kinds[change.Kind] = true
	}
	if !kinds["bindings_changed"] || !kinds["references_changed"] || !kinds["import_location_changed"] {
		t.Fatal(kinds)
	}
	f.Write("use.ts", "import(name);\n")
	partial := capture()
	comparison, err = surface.Compare(after, partial)
	if err != nil || comparison.Comparable || comparison.Coverage == nil || len(comparison.Callers) != 1 || comparison.Callers[0].Kind != "edge_removed" {
		t.Fatal(comparison, err)
	}
	if len(comparison.BasisChanges) != 1 || comparison.BasisChanges[0] != "coverage_gaps" {
		t.Fatal(comparison)
	}
	partial.AdapterSHA256 = "different adapter"
	partial.SnapshotSHA256, _ = partial.Digest()
	comparison, err = surface.Compare(after, partial)
	if err != nil || comparison.Comparable || comparison.BasisChanges[0] != "adapter" {
		t.Fatal(comparison, err)
	}
	partial.SnapshotSHA256 = "corrupt"
	if _, err = surface.Compare(after, partial); err == nil {
		t.Fatal("edited artifact accepted")
	}
}
