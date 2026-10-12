package analysis_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

const transformPath = "npm-packages/convex/index.ts"
const transformOther = "npm-packages/client/fixture.ts"
const transformSource = "const endpoint = 'https://one.convex.cloud';\n\nconst second = 'https://two.convex.cloud';\n\nconst wire = 'convex';\n"

func transformFixture(t *testing.T) *testrepo.Fixture {
	f := testrepo.New(t)
	f.Write(transformPath, transformSource)
	f.Write(transformOther, transformSource)
	f.Config.Upstream = f.Commit()
	f.Write("contract.md", "# Branding\n")
	f.Config.Transforms = []policy.Transform{{ID: "fixture-urls", Paths: []string{transformPath, transformOther}, Replacements: []policy.Replacement{{From: ".convex.cloud", To: ".bijection.cloud"}}, Explanation: policy.Explanation{Why: "Use deployment hostnames in rendered fixtures.", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Commit()
	f.Write(transformPath, strings.ReplaceAll(transformSource, ".convex.cloud", ".bijection.cloud"))
	f.Write(transformOther, strings.ReplaceAll(transformSource, ".convex.cloud", ".bijection.cloud"))
	return f
}
func TestLiteralTransformSnapshotAndPartialCoverage(t *testing.T) {
	f := transformFixture(t)
	r := scan(t, f, analysis.Options{Paths: []string{transformPath, transformOther}})
	if len(r.Findings) != 0 {
		t.Fatal(r.Findings)
	}
	for _, row := range r.Files {
		if row.Classification != "transformed" || row.Explanations["fixture-urls"].Why == "" {
			t.Fatal(row)
		}
	}
	f.Write(transformPath, strings.ReplaceAll(transformSource, ".convex.cloud", ".bijection.cloud")+"\nnewBehavior();\n")
	r = scan(t, f, analysis.Options{Paths: []string{transformPath}})
	if len(r.Findings) != 1 || r.Findings[0].Rule != "source/covered" || r.Findings[0].Line != 6 || len(r.Files[0].Approved) != 1 {
		t.Fatal(r.Findings, r.Files)
	}
	f.Write(transformPath, strings.ReplaceAll(transformSource, ".convex.cloud", ".bijection.cloud"))
	rev := f.Commit()
	f.Write("contract.md", "# Other\n")
	f.Write(transformPath, "dirty source\n")
	r = scan(t, f, analysis.Options{Rev: rev, Paths: []string{transformPath}})
	if len(r.Findings) != 0 {
		t.Fatal("dirty files changed committed meaning", r.Findings)
	}
}
func TestLiteralTransformFailsClosed(t *testing.T) {
	for _, kind := range []string{"partial rewrite", "missing witness", "missing path", "mode", "symlink", "binary unchanged", "binary changed", "unknown upstream path", "missing module", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			f := transformFixture(t)
			switch kind {
			case "partial rewrite":
				f.Write(transformPath, strings.Replace(transformSource, ".convex.cloud", ".bijection.cloud", 1))
			case "missing witness":
				f.Config.Transforms[0].Explanation.Witnesses = []string{"missing.ts"}
				f.Save()
			case "missing path":
				if e := os.Remove(filepath.Join(f.Root, transformPath)); e != nil {
					t.Fatal(e)
				}
			case "mode":
				if e := os.Chmod(filepath.Join(f.Root, transformPath), 0755); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Remove(filepath.Join(f.Root, transformPath)); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink("../../crates/core/lib.rs", filepath.Join(f.Root, transformPath)); e != nil {
					t.Fatal(e)
				}
			case "binary unchanged", "binary changed":
				f.Write(transformPath, "\x00.convex.cloud")
				f.Config.Upstream = f.Commit()
				f.Save()
				f.Commit()
				if kind == "binary changed" {
					f.Write(transformPath, "\x00.bijection.cloud")
				}
			case "unknown upstream path":
				f.Config.Transforms[0].Paths = append(f.Config.Transforms[0].Paths, "absent.ts")
				f.Save()
			case "missing module", "manifest":
				f.Config.TransformationManifest = "rewrites.json"
				f.Config.Transforms[0].Module = "fixture.js"
				f.Config.Transforms[0].Replacements = nil
				manifest := `{"rewrites":{"fixture.js":[[".convex.cloud",".bijection.cloud"]]}}`
				if kind == "missing module" {
					manifest = `{"rewrites":{}}`
				}
				f.Write("rewrites.json", manifest)
				f.Save()
			}
			repo, c := f.Open()
			r, e := analysis.Analyze(repo, c, analysis.Options{Paths: []string{transformPath}})
			if kind == "unknown upstream path" {
				if e == nil {
					t.Fatal("unknown path accepted")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if kind == "manifest" {
				if len(r.Findings) > 0 || r.Files[0].Classification != "transformed" {
					t.Fatal(r.Findings)
				}
				return
			}
			if len(r.Findings) == 0 || r.Files[0].Classification != "unexplained" {
				t.Fatal(kind, r.Findings, r.Files)
			}
		})
	}
}

func TestLiteralTransformationHunksKeepUnrelatedDriftVisible(t *testing.T) {
	for _, kind := range []string{"insert", "delete", "multiline", "ordered", "crlf", "eof", "wrong-owner", "extra-in-same-hunk", "missing-insertion"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			before := "pub struct One;\n\npub struct Two;\n\nfn other() {}\n"
			pairs := []policy.Replacement{{From: "pub struct One;\n", To: "#[derive(Test)]\npub struct One;\n"}}
			if kind == "delete" {
				pairs = []policy.Replacement{{From: "pub struct One;\n", To: ""}}
			}
			if kind == "multiline" {
				pairs = []policy.Replacement{{From: "One;\n\npub struct Two", To: "First;\n\npub struct Second"}}
			}
			if kind == "ordered" {
				pairs = append(pairs, policy.Replacement{From: "derive(Test)", To: "derive(Arbitrary)"})
			}
			if kind == "crlf" {
				before = strings.ReplaceAll(before, "\n", "\r\n")
				pairs[0].From = strings.ReplaceAll(pairs[0].From, "\n", "\r\n")
				pairs[0].To = strings.ReplaceAll(pairs[0].To, "\n", "\r\n")
			}
			if kind == "eof" {
				before = strings.TrimSuffix(before, "\n")
				pairs = []policy.Replacement{{From: "fn other() {}", To: "fn other() {}\n"}}
			}
			f.Write(transformPath, before)
			f.Config.Upstream = f.Commit()
			f.Config.Transforms = []policy.Transform{{ID: "precise-source", Paths: []string{transformPath}, Replacements: pairs, Explanation: policy.Explanation{Why: "Retain explicitly reviewed fixture support.", Witnesses: []string{"crates/core/lib.rs"}}}}
			f.Save()
			f.Commit()
			after := before
			for _, pair := range pairs {
				after = strings.ReplaceAll(after, pair.From, pair.To)
			}
			// Drift is separate from the permitted recipe, except the deliberately
			// merged-hunk case. No other finding may be hidden by matching bytes.
			if kind == "eof" {
				after = strings.Replace(after, "pub struct One;", "pub struct Changed;", 1)
			} else {
				after = strings.Replace(after, "fn other() {}", "fn unrelated_drift() {}", 1)
			}
			switch kind {
			case "wrong-owner":
				after = strings.Replace(after, "#[derive(Test)]\n", "", 1)
				after = strings.Replace(after, "pub struct Two;", "#[derive(Test)]\npub struct Two;", 1)
			case "extra-in-same-hunk":
				after = strings.Replace(after, "#[derive(Test)]\n", "#[derive(Test)]\nextra();\n", 1)
			case "missing-insertion":
				after = strings.Replace(after, "#[derive(Test)]\n", "", 1)
			}
			f.Write(transformPath, after)
			r := scan(t, f, analysis.Options{Paths: []string{transformPath}})
			wantFindings := 1
			if kind == "wrong-owner" || kind == "extra-in-same-hunk" {
				wantFindings = 2
			}
			if len(r.Findings) != wantFindings || r.Files[0].Classification != "unexplained" {
				t.Fatal(r.Findings, r.Files)
			}
			for _, finding := range r.Findings {
				if finding.Rule != "source/covered" {
					t.Fatal(finding)
				}
			}
			if kind != "wrong-owner" && kind != "extra-in-same-hunk" && kind != "missing-insertion" {
				// Check the remaining location, not just failure: the authorized
				// insertion/deletion must disappear from the review queue.
				wantStart := 4
				if kind == "eof" {
					wantStart = 0
				}
				if r.Findings[0].Upstream == nil || r.Findings[0].Upstream.Start != wantStart {
					t.Fatal(r.Findings)
				}
			}
		})
	}
}

func TestPathScopedRecipeCannotApproveTheSameEditInAnotherFile(t *testing.T) {
	f := testrepo.New(t)
	before := "pub struct One;\n\npub struct Two;\n"
	f.Write(transformPath, before)
	f.Write(transformOther, before)
	f.Config.Upstream = f.Commit()
	f.Config.Transforms = []policy.Transform{{ID: "explicit-owners", Paths: []string{transformPath, transformOther},
		Replacements: []policy.Replacement{
			{From: "pub struct One;", To: "#[derive(Test)]\npub struct One;", Paths: []string{transformPath}},
			{From: "pub struct Two;", To: "#[derive(Test)]\npub struct Two;", Paths: []string{transformOther}},
		}, Explanation: policy.Explanation{Why: "Keep test generation on each reviewed owner.", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Commit()
	f.Write(transformPath, "#[derive(Test)]\n"+before)
	f.Write(transformOther, strings.Replace(before, "pub struct Two;", "#[derive(Test)]\npub struct Two;", 1))
	r := scan(t, f, analysis.Options{Paths: []string{transformPath, transformOther}})
	if len(r.Findings) != 0 {
		t.Fatal(r.Findings)
	}
	rev := f.Commit()
	f.Write(transformOther, "#[derive(Test)]\n"+before)
	r = scan(t, f, analysis.Options{Paths: []string{transformPath, transformOther}})
	if len(r.Findings) != 1 || r.Findings[0].Path != transformOther {
		t.Fatal(r.Findings)
	}
	r = scan(t, f, analysis.Options{Rev: rev, Paths: []string{transformPath, transformOther}})
	if len(r.Findings) != 0 {
		t.Fatal("dirty source changed committed recipe scope", r.Findings)
	}
}

func TestHistoricalRestorationsAndTransformsHaveDisjointOwners(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		name := "disjoint"
		if overlap {
			name = "overlap"
		}
		t.Run(name, func(t *testing.T) {
			f := historyFixture(t, func(s string) string { return s })
			pair := policy.Replacement{From: "// bottom", To: "// renamed bottom"}
			if overlap {
				pair = policy.Replacement{From: "fn first() {}\n", To: historyTest + "fn first() {}\n"}
			}
			f.Config.Transforms = []policy.Transform{{ID: "reviewed-edit", Paths: []string{"crates/core/lib.rs"}, Replacements: []policy.Replacement{pair}, Explanation: policy.Explanation{Why: "An explicit source recipe.", Witnesses: []string{"crates/core/lib.rs"}}}}
			f.Save()
			source := historyPrefix + historyTest + historySuffix
			if !overlap {
				source = strings.Replace(source, pair.From, pair.To, 1)
			}
			f.Write("crates/core/lib.rs", source)
			r := scan(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
			if overlap {
				if !has(r, "stale_transform") {
					t.Fatal(r.Findings)
				}
			} else if len(r.Findings) != 0 || len(r.Files[0].Approved) != 2 {
				t.Fatal(r.Findings, r.Files)
			}
		})
	}
}
