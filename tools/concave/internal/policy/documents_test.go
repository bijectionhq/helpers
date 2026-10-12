package policy_test

import (
	"testing"

	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestDocumentCorrespondenceRejectsAmbiguityAndDoesNotRewriteUpstream(t *testing.T) {
	f := testrepo.New(t)
	original := "npm-packages/docs/docs/guide.mdx"
	f.Write(original, "Original.\n")
	f.Config.Upstream = f.Commit()
	good := policy.Correspondence{Path: "apps/docs/guide.mdx", UpstreamPath: original}
	for _, pairs := range [][]policy.Correspondence{
		{good, good},
		{good, {Path: "apps/docs/other.mdx", UpstreamPath: original}},
		{{Path: "../escape.mdx", UpstreamPath: original}},
		{{Path: good.Path, UpstreamPath: good.Path}},
	} {
		f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}, Correspondences: pairs}
		if err := f.Config.Validate(); err == nil {
			t.Fatalf("invalid mapping accepted: %+v", pairs)
		}
	}
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}, Correspondences: []policy.Correspondence{good}}
	f.Save()
	repo, config := f.Open()
	mapped, err := config.UpstreamTree(repo)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[original] != mapped[good.Path] {
		t.Fatal("mapped source differs")
	}
	originalTree, err := repo.Tree(config.Upstream)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := originalTree[good.Path]; exists {
		t.Fatal("mapping mutated the cached Git tree")
	}
	config.Documents.Correspondences[0].UpstreamPath = "npm-packages/docs/docs/missing.mdx"
	if _, err = config.UpstreamTree(repo); err == nil {
		t.Fatal("missing source accepted")
	}
}

// The document parser environment, when named, is a declared environment, and
// a candidate's deadline reserves its preparation once.
func TestDocumentParserEnvironmentIsDeclared(t *testing.T) {
	f := testrepo.New(t)
	f.Config.Environments = map[string]policy.Environment{"docs": {Directory: "npm-packages", Packages: []string{"convex"}, TimeoutSeconds: 60}}
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}, Environment: "docs"}
	if err := f.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	f.Config.Documents.Environment = "missing"
	if err := f.Config.Validate(); err == nil {
		t.Fatal("unknown parser environment accepted")
	}
}
