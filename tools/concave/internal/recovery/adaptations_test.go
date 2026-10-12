package recovery_test

import (
	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/testrepo"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecoveryRetainsDeclaredTransformation(t *testing.T) {
	f := testrepo.New(t)
	path := "crates/core/lib.rs"
	f.Write(path, "const NAME: &str = \"convex\"; // original\n")
	f.Config.Upstream = f.Commit()
	f.Config.Transforms = []policy.Transform{{ID: "brand", Paths: []string{path}, Replacements: []policy.Replacement{{From: "convex", To: "bijection"}}, Explanation: policy.Explanation{Why: "Product name.", Witnesses: []string{"packaging/facade.ts"}}}}
	f.Save()
	f.Commit()
	f.Write(path, "const NAME: &str = \"bijection\"; // changed\n")
	r, c := f.Open()
	report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	plan, err := recovery.Prepare(r, c, report, raw, []string{report.Hunks[0].ID}, nil, "restore behavior and retain product name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Patch, "+const NAME: &str = \"bijection\"; // original") || len(plan.Findings) != 0 {
		t.Fatal(plan.Patch, plan.Findings)
	}
	if err := recovery.Apply(r, c, plan); err != nil {
		t.Fatal(err)
	}
}

func TestConvertedMetadataRequiresReviewedPatch(t *testing.T) {
	f := testrepo.New(t)
	f.Documents()
	source, target := "npm-packages/docs/docs/page.mdx", "apps/docs/page.mdx"
	f.Write(source, "---\ntitle: Convex\nslug: home\n---\n\nText.\n")
	f.Config.Upstream = f.Commit()
	f.Write(target, "---\ntitle: Bijection\n---\n\nText.\n")
	f.Config.Documents = &policy.Documents{Paths: []string{"apps/docs/"}, Correspondences: []policy.Correspondence{{Path: target, UpstreamPath: source}}}
	f.Save()
	f.Commit()
	r, c := f.Open()
	report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{target}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	_, err = recovery.Prepare(r, c, report, raw, []string{report.Hunks[0].ID}, nil, "restore metadata")
	if err == nil || !strings.Contains(err.Error(), "reviewed --patch") {
		t.Fatal(err)
	}
}
