package analysis_test

import (
	"concave/internal/policy"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/testrepo"
)

func TestDocumentExamplesCannotDeclareCodeContracts(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	f.Write("guide.md", "# Concave\n\n```rs\n// concave:invariant example\n// Example only.\nfn sample() {}\n```\n")
	f.Save()
	f.Commit()
	repo, config := f.Open()
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{"guide.md"}, Contracts: true})
	if err != nil || len(report.Findings) != 0 {
		t.Fatalf("example became a contract: %v %+v", err, report)
	}
}

func TestHistoricalDocumentsRefuseDifferentParserInputs(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}}
	f.Write("apps/docs/page.mdx", "# Example\n")
	f.Save()
	revision := f.Commit()
	repo, config := f.Open()
	f.Write("tools/concave/testdata/mdx/package.json", `{"name":"different-parser-definition"}`)
	_, err := analysis.Analyze(repo, config, analysis.Options{Rev: revision, Paths: []string{"apps/docs/page.mdx"}})
	if err == nil || !strings.Contains(err.Error(), "parser inputs differ") {
		t.Fatal("historical parser identity was not checked", err)
	}
}

// The lockfile resolves every workspace project. A revision whose lockfile
// differs from the installed one only in another project names the same
// parser; one that resolves a package the parser loads does not.
func TestHistoricalDocumentsBindTheParsersLockfileClosure(t *testing.T) {
	const lock = "tools/concave/testdata/pnpm-lock.yaml"
	f := testrepo.New(t)
	f.Documents()
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}}
	f.Write("apps/docs/page.mdx", "# Example\n")
	f.Save()
	revision := f.Commit()
	repo, config := f.Open()
	options := analysis.Options{Rev: revision, Paths: []string{"apps/docs/page.mdx"}}
	data, err := os.ReadFile(filepath.Join(f.Root, lock))
	if err != nil {
		t.Fatal(err)
	}
	installed := string(data)
	edit := func(source, old, new string) string {
		t.Helper()
		if strings.Count(source, old) != 1 {
			t.Fatalf("lockfile has %d of %q", strings.Count(source, old), old)
		}
		return strings.Replace(source, old, new, 1)
	}

	other := edit(installed, "\npackages:\n", "\n  concave-test-project:\n    dependencies:\n      gray-matter:\n        specifier: 0.0.0\n        version: 0.0.0\n\npackages:\n")
	other = edit(other, "\nsnapshots:\n", "\n  gray-matter@0.0.0:\n    resolution: {integrity: sha512-concave-test}\n\nsnapshots:\n")
	f.Write(lock, other+"\n  gray-matter@0.0.0: {}\n")
	report, err := analysis.Analyze(repo, config, options)
	if err != nil || report.DocumentEngine == "" {
		t.Fatal("another project's resolution refused the installed parser", err)
	}

	loaded := regexp.MustCompile(`(?m)^  gray-matter@[^:\n]+:\n    resolution: \{integrity: `)
	if len(loaded.FindAllString(installed, -1)) != 1 {
		t.Fatal("the lockfile does not resolve the adapter's gray-matter exactly once")
	}
	f.Write(lock, loaded.ReplaceAllString(installed, "${0}concave-test-"))
	if _, err = analysis.Analyze(repo, config, options); err == nil || !strings.Contains(err.Error(), "parser inputs differ") {
		t.Fatal("a different parser package was accepted", err)
	}

	f.Write(lock, "not a lockfile\n")
	if _, err = analysis.Analyze(repo, config, options); err == nil || !strings.Contains(err.Error(), "parser inputs differ") {
		t.Fatal("an unreadable lockfile was accepted", err)
	}
}
