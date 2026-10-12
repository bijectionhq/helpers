package surface_test

import (
	"concave/internal/surface"
	"strings"
	"testing"
)

func TestCallerCoverageReportsLocatedGapsAndExactOmissions(t *testing.T) {
	f := fixture(t)
	f.Write("uses.ts", "import { renamed } from './entry.js';\nconst café = 'é'; renamed('x');\n"+strings.Repeat("renamed('x');\n", 69))
	f.Write("dynamic.ts", "import(name);\nrequire(name);\nfunction local(require: any) { require(name); require('./entry.js'); }\nimport(`./entry.js`);\n")
	f.Write("broken.ts", "import { renamed } from './entry.js';\nconst missing = ;\n")
	f.Write("equals.ts", "import sdk = require('./entry.js');\nsdk.renamed('x');\n")
	repo, _ := f.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != surface.Incomplete || report.Coverage.Complete || report.Coverage.ParseErrors != 1 || report.Coverage.UnresolvedDynamic != 2 || report.Coverage.OmittedReferences != 6 {
		t.Fatal(report.Status, report.Coverage)
	}
	issues := map[string]bool{}
	for _, issue := range report.Coverage.Issues {
		if issue.Line < 1 || issue.Column < 1 || issue.Message == "" {
			t.Fatal(issue)
		}
		issues[issue.Path+":"+issue.Kind] = true
	}
	if !issues["broken.ts:parse_error"] || !issues["dynamic.ts:nonliteral_import"] {
		t.Fatal(issues)
	}
	seen := map[string]surface.Consumer{}
	for _, consumer := range report.Consumers {
		if consumer.Path == "dynamic.ts" && consumer.Line != 4 {
			t.Fatal("shadowed require became an edge", consumer)
		}
		seen[consumer.Path] = consumer
	}
	use := seen["uses.ts"]
	if use.ReferencesComplete || len(use.References) != 64 || use.OmittedReferences != 6 || use.References[0].Column != len("const café = 'é'; ")+1 {
		t.Fatal(use)
	}
	if seen["broken.ts"].ReferencesComplete || len(seen["broken.ts"].References) != 0 {
		t.Fatal(seen["broken.ts"])
	}
	if len(seen["equals.ts"].References) != 1 || seen["equals.ts"].References[0].Name != "sdk" {
		t.Fatal(seen["equals.ts"])
	}
	if err = surface.Validate(repo, report); err != nil {
		t.Fatal("partial evidence could not be validated", err)
	}
}

func TestCallerCoverageBoundsIssueDetailsWithoutHidingCounts(t *testing.T) {
	f := fixture(t)
	f.Write("dynamic.ts", strings.Repeat("import(name);\n", 205))
	repo, _ := f.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != surface.Captured || report.Coverage.Complete || report.Coverage.UnresolvedDynamic != 205 || len(report.Coverage.Issues) != 200 || report.Coverage.OmittedIssues != 5 {
		t.Fatal(report.Status, report.Coverage)
	}
}
