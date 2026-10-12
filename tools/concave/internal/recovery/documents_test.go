package recovery_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestMappedDocumentRecoveryBindsImportsAndLeavesOriginalUntouched(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	upstream := "import Example from './partial.mdx';\n\n# Original\n"
	f.Write("npm-packages/docs/docs/page.mdx", upstream)
	f.Config.Upstream = f.Commit()
	f.Write("apps/docs/page.mdx", strings.Replace(upstream, "Original", "Changed", 1))
	f.Write("apps/docs/partial.mdx", "Example source.\n")
	f.Config.Roots = append(f.Config.Roots, "apps/docs/")
	f.Config.Families = append(f.Config.Families, policy.Family{ID: "docs", Title: "Docs", Paths: []string{"apps/docs/"}})
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}, Correspondences: []policy.Correspondence{{Path: "apps/docs/page.mdx", UpstreamPath: "npm-packages/docs/docs/page.mdx"}}}
	f.Save()
	f.Commit()
	repo, config := f.Open()
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{"apps/docs/page.mdx"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Hunks) != 1 || report.Files[0].UpstreamPath != "npm-packages/docs/docs/page.mdx" {
		t.Fatalf("%+v", report)
	}
	repeat, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{"apps/docs/page.mdx"}})
	if err != nil || repeat.SnapshotSHA256 != report.SnapshotSHA256 {
		t.Fatal("unstable document identity", err)
	}
	raw, _ := json.Marshal(report)
	plan, err := recovery.Prepare(repo, config, report, raw, []string{report.Hunks[0].ID}, nil, "restore heading")
	if err != nil {
		t.Fatal(err)
	}
	f.Write("apps/docs/partial.mdx", "Concurrent dependency edit.\n")
	if err = recovery.Validate(repo, config, plan); err == nil {
		t.Fatal("changed dependency accepted")
	}
	f.Write("apps/docs/partial.mdx", "Example source.\n")
	if err = recovery.Apply(repo, config, plan); err != nil {
		t.Fatal(err)
	}
	if err = recovery.ValidateApplied(repo, config, plan); err != nil {
		t.Fatal(err)
	}
	if f.Git("diff", "--", "npm-packages/docs/docs/page.mdx") != "" {
		t.Fatal("original source changed")
	}
}

func TestMDXExceptionBindingRejectsChangedBodyAndIgnoresExampleComments(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	before := "Use convex.\n"
	f.Write("npm-packages/docs/docs/page.mdx", before)
	f.Config.Upstream = f.Commit()
	f.Config.Families = append(f.Config.Families, policy.Family{ID: "docs", Title: "Docs", Paths: []string{"apps/docs/"}})
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}, Correspondences: []policy.Correspondence{{Path: "apps/docs/page.mdx", UpstreamPath: "npm-packages/docs/docs/page.mdx"}}}
	f.Write("apps/docs/page.mdx", "Use bijection.\n")
	f.Save()
	f.Commit()
	repo, config := f.Open()
	text := "{/* concave:begin audience\nwhy: Intentional product audience.\nwitness: npm-packages/convex/index.ts\n*/}\n\nUse bijection.\n{/* concave:end audience */}\n"
	parsed, bad := policy.NewSession(repo, config).Annotations("apps/docs/page.mdx", text)
	if len(bad) > 0 || len(parsed) != 1 {
		t.Fatalf("%+v %+v", parsed, bad)
	}
	a := parsed["audience"]
	if strings.TrimSpace(a.Code) != "Use bijection." {
		t.Fatalf("body: %q", a.Code)
	}
	f.Write("apps/docs/page.mdx", text)
	proposal, err := exceptions.Prepare(repo, config, exceptions.Record, []string{"audience"}, exceptions.Options{Path: "apps/docs/page.mdx"})
	if err != nil || len(proposal.Changes) != 1 || proposal.Changes[0].Next == nil {
		t.Fatalf("record annotation: %v %+v", err, proposal)
	}
	config.Exceptions = []policy.Exception{*proposal.Changes[0].Next}
	pairs := map[string]repository.SourcePair{"apps/docs/page.mdx": {Before: []byte(before), After: []byte(text)}}
	hs, err := repo.DiffSources(pairs)
	if err != nil {
		t.Fatal(err)
	}
	_, _, findings := policy.NewSession(repo, config).ClassifyRestorations("apps/docs/page.mdx", []byte(before), []byte(text), hs["apps/docs/page.mdx"], nil, nil)
	if len(findings) > 0 {
		t.Fatal(findings)
	}
	_, _, findings = policy.NewSession(repo, config).ClassifyRestorations("apps/docs/page.mdx", []byte(before), []byte(strings.Replace(text, "Use bijection.", "Use other.", 1)), nil, nil, nil)
	if len(findings) == 0 {
		t.Fatal("changed exception body accepted")
	}
}

func TestNativeSnippetPlanAppliesExactImportsAndBindsRenderer(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	for _, name := range []string{"npm-packages/docs/src/snippet.tsx", "npm-packages/docs/recovery/snippets.mjs", "npm-packages/docs/recovery/migrate.mjs", "npm-packages/docs/recovery/content.mjs", "npm-packages/bijection/scripts/facade.mjs", "npm-packages/bijection/scripts/parity-transforms.json"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		f.Write(name, string(data))
	}
	if err := os.Symlink(filepath.Join(root, "npm-packages/bijection/node_modules"), filepath.Join(f.Root, "npm-packages/bijection/node_modules")); err != nil {
		t.Fatal(err)
	}
	before := "---\ntitle: Guide\n---\n\nimport Example from \"!!raw-loader!@site/../private-demos/example.ts\";\n\n<Snippet source={Example} />\n"
	f.Write("npm-packages/private-demos/example.ts", "console.log(1);\n")
	page := "npm-packages/docs/docs/guide.mdx"
	f.Write(page, before)
	f.Config.Upstream = f.Commit()
	f.Config.Documents = &policy.Documents{Paths: []string{"npm-packages/docs/docs/"}}
	f.Save()
	f.Commit()
	f.Write(page, "---\ntitle: Guide\n---\n\n```ts\nconsole.log(1);\n```\n")
	repo, config := f.Open()
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{page}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	plan, err := recovery.PrepareDocument(repo, config, report, raw, page, "restore the exact source-backed example")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Examples) != 1 || plan.Examples[0].Status != "recovered" {
		t.Fatal(plan.Examples)
	}
	f.Write("npm-packages/private-demos/example.ts", "console.log(2);\n")
	if recovery.Validate(repo, config, plan) == nil {
		t.Fatal("changed snippet accepted")
	}
	f.Write("npm-packages/private-demos/example.ts", "console.log(1);\n")
	if err = recovery.Apply(repo, config, plan); err != nil {
		t.Fatal(err)
	}
	_, result, err := repo.Snapshot(page)
	if err != nil || string(result) != before {
		t.Fatalf("%v\n%q != %q", err, result, before)
	}
	if err = recovery.ValidateApplied(repo, config, plan); err != nil {
		t.Fatal(err)
	}
}
