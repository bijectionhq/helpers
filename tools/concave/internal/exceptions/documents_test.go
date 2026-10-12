package exceptions_test

import (
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestMDXAnnotationAuthoringUsesExistingBindingWorkflow(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	page := "npm-packages/docs/docs/guide.mdx"
	f.Write(page, "Original audience.\n")
	f.Config.Upstream = f.Commit()
	f.Save()
	f.Commit()
	f.Write(page, "Bijection audience.\n")
	repo, config := f.Open()
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{page}})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := exceptions.Prepare(repo, config, exceptions.Record, []string{"audience"}, exceptions.Options{Annotate: true, Path: page, Hunks: []string{report.Hunks[0].ID}, Metadata: policy.Explanation{Why: "Use the product session audience.", Witnesses: []string{"npm-packages/convex/index.ts"}}})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.SourcePatch == "" || proposal.Changes[0].Next == nil || !proposal.Changes[0].Next.Fence {
		t.Fatalf("%+v", proposal)
	}
	if err := exceptions.Write(repo, config, proposal); err != nil {
		t.Fatal(err)
	}
	repo, config = f.Open()
	checked, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{page}})
	if err != nil || len(checked.Findings) != 0 {
		t.Fatalf("recorded MDX exception: %v %+v", err, checked)
	}
}

func TestFrontmatterFieldsCarryTheirOwnYAMLFence(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	page := "npm-packages/docs/docs/guide.mdx"
	f.Write(page, "---\ntitle: Original\nsidebar_position: 1\ndescription: Same.\n---\n\nBody.\n")
	f.Config.Upstream = f.Commit()
	f.Save()
	f.Commit()
	f.Write(page, "---\ntitle: Product\ndescription: Same.\n---\n\nBody.\n")
	repo, config := f.Open()
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{page}})
	if err != nil || len(report.Hunks) != 1 {
		t.Fatal(err, report)
	}
	proposal, err := exceptions.Prepare(repo, config, exceptions.Record, []string{"frontmatter-title"}, exceptions.Options{Annotate: true, Path: page, Hunks: []string{report.Hunks[0].ID}, Metadata: policy.Explanation{Why: "The page uses the product title; the host orders pages.", Witnesses: []string{"npm-packages/convex/index.ts"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(proposal.SourcePatch, "+# concave:begin frontmatter-title\n") || strings.Contains(proposal.SourcePatch, "{/*") {
		t.Fatal(proposal.SourcePatch)
	}
	if err := exceptions.Write(repo, config, proposal); err != nil {
		t.Fatal(err)
	}
	repo, config = f.Open()
	checked, err := analysis.Analyze(repo, config, analysis.Options{Paths: []string{page}})
	if err != nil || len(checked.Findings) != 0 || checked.Files[0].Classification != "excepted" {
		t.Fatalf("recorded frontmatter exception: %v %+v", err, checked)
	}
	for name, source := range map[string]string{
		"crosses into the body": "---\n# concave:begin crossing\n# why: x\n# witness: a.ts\ntitle: Product\n---\n\n{/* concave:end crossing */}\n",
		"starts inside a value": "---\ndescription:\n# concave:begin partial\n# why: x\n# witness: a.ts\n#\n  folded text\n# concave:end partial\n---\n",
		"ends inside a value":   "---\n# concave:begin partial\n# why: x\n# witness: a.ts\n#\ndescription:\n  - one\n# concave:end partial\n  - two\n---\n",
	} {
		_, bad := policy.NewSession(repo, config).Annotations("npm-packages/docs/docs/other.mdx", source)
		if len(bad) == 0 || !strings.Contains(bad[0].Message, "a frontmatter fence must") {
			t.Fatalf("%s: %+v", name, bad)
		}
	}
}
