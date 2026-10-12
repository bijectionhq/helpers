package analysis_test

import (
	"slices"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func importerFixture(t *testing.T) *testrepo.Fixture {
	f := testrepo.New(t)
	f.Documents()
	f.Config.Roots = append(f.Config.Roots, "apps")
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}}
	f.Save()
	f.Write("apps/docs/_partial.mdx", "Shared text.\n")
	// c imports the partial directly and has its own document finding.
	f.Write("apps/docs/c.mdx", "import P from './_partial.mdx';\nimport M from './missing-c.mdx';\n\n<P /><M />\n")
	// e imports it only through _outer, and never names it.
	f.Write("apps/docs/_outer.mdx", "import P from './_partial.mdx';\n\n<P />\n")
	f.Write("apps/docs/e.mdx", "import O from './_outer.mdx';\n\n<O />\n")
	// b names the partial in prose but imports nothing.
	f.Write("apps/docs/b.mdx", "See _partial for details.\n")
	return f
}

func rowPaths(report *analysis.Report) []string {
	paths := []string{}
	for _, row := range report.Files {
		paths = append(paths, row.Path)
	}
	return paths
}

// A scoped check of a partial measures every page whose dependency identity
// includes it, directly or through another partial, and nothing else.
func TestScopedCheckRemeasuresDocumentImporters(t *testing.T) {
	f := importerFixture(t)
	f.Commit()
	f.Write("apps/docs/_partial.mdx", "Edited shared text.\n")
	repo, config := f.Open()
	for _, options := range []analysis.CheckOptions{{Paths: []string{"apps/docs/_partial.mdx"}}, {Changed: true}} {
		report, err := analysis.Check(repo, config, options)
		if err != nil {
			t.Fatal(err)
		}
		got := rowPaths(report)
		for _, want := range []string{"apps/docs/_outer.mdx", "apps/docs/_partial.mdx", "apps/docs/c.mdx", "apps/docs/e.mdx"} {
			if !slices.Contains(got, want) {
				t.Fatalf("%+v: importer %s not re-measured: %v", options, want, got)
			}
		}
		if slices.Contains(got, "apps/docs/b.mdx") {
			t.Fatalf("%+v: a page merely naming the partial was measured: %v", options, got)
		}
		documents := []string{}
		for _, document := range report.Documents {
			documents = append(documents, document.Path)
		}
		if !slices.Contains(documents, "apps/docs/c.mdx") || !slices.Contains(documents, "apps/docs/e.mdx") {
			t.Fatalf("importer identities not measured: %v", documents)
		}
	}
	// A selection importing nothing expands to nothing.
	importers, err := analysis.DocumentImporters(repo, config, "", []string{"crates/core/lib.rs"})
	if err != nil || len(importers) != 0 {
		t.Fatal(importers, err)
	}
}

// Publication parity measures the same importers: editing a partial changes
// the identity of its importer's own finding, a regression only the importer
// carries.
func TestParityRemeasuresDocumentImporters(t *testing.T) {
	f := importerFixture(t)
	baseline := f.Commit()
	f.Write("apps/docs/_partial.mdx", "Edited shared text.\n")
	result := parity(t, f, baseline, f.Commit())
	if result.Status != "failed" || len(result.Regressions) != 1 || result.Regressions[0].Path != "apps/docs/c.mdx" || !slices.Contains(result.Scope.Paths, "apps/docs/c.mdx") {
		t.Fatalf("importer regression missed by the scoped comparison: %+v", result)
	}
}
