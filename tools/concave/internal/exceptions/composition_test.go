package exceptions_test

import (
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

const composedOriginal = "const BRAND: &str = \"convex\";\n\nfn first() {}\n\nfn second() {}\n"

func compositionFixture(t *testing.T) (*testrepo.Fixture, string) {
	t.Helper()
	f := testrepo.New(t)
	f.Write(path, composedOriginal)
	f.Config.Upstream = f.Commit()
	f.Write("contract.md", "# Read\n")
	f.Config.Transforms = []policy.Transform{{ID: "public-name", Paths: []string{path}, Replacements: []policy.Replacement{{From: "\"convex\"", To: "\"bijection\""}}, Explanation: policy.Explanation{Why: "Use the public name in the inherited declaration.", Witnesses: []string{path}}}}
	f.Save()
	f.Commit()
	source := strings.Replace(composedOriginal, "\"convex\"", "\"bijection\"", 1)
	source = strings.Replace(source, "fn first() {}\n", fenced("fn first() { let native = \"convex\"; read(); }\n"), 1)
	f.Write(path, source)
	return f, source
}

func TestSharedRecipeAndNativeExceptionHaveIndependentLifecycles(t *testing.T) {
	f, source := compositionFixture(t)
	index := f.Git("ls-files", "--stage")
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, "record", []string{"read"}, exceptions.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if p.Changes[0].Next.Start != 2 || p.Changes[0].Next.End != 3 || len(p.Changes[0].Hunks) != 1 {
		t.Fatal(p)
	}
	if err = exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	r, c = f.Open()
	report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
	if err != nil || len(report.Findings) != 0 || len(report.Files[0].Approved) != 2 {
		t.Fatal(report, err)
	}
	// The exception deliberately includes the same source literal. Transforming
	// its owned code would violate the recorded body and corrupt its meaning.
	v, err := explain(r, c, "read", "")
	if err != nil || v.Status != "valid" {
		t.Fatal(v, err)
	}
	f.Write(path, strings.Replace(source, "read();", "read_more();", 1))
	r, c = f.Open()
	p, err = exceptions.Prepare(r, c, "refresh", []string{"read"}, exceptions.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err = exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	if f.Git("ls-files", "--stage") != index {
		t.Fatal("shared index changed")
	}
	rev := f.Commit()
	f.Write(path, "dirty source\n")
	r, c = f.Open()
	report, err = analysis.Analyze(r, c, analysis.Options{Paths: []string{path}, Rev: rev})
	if err != nil || len(report.Findings) != 0 {
		t.Fatal(report, err)
	}
	// Removing only the exception leaves the upstream recipe intact.
	f.Write(path, strings.Replace(composedOriginal, "\"convex\"", "\"bijection\"", 1))
	r, c = f.Open()
	p, err = exceptions.Prepare(r, c, "refresh", []string{"read"}, exceptions.Options{Path: path})
	if err != nil || p.Changes[0].Action != "remove" {
		t.Fatal(p, err)
	}
	if err = exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	r, c = f.Open()
	report, err = analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
	if err != nil || len(report.Findings) != 0 || len(report.Files[0].Approved) != 1 {
		t.Fatal(report, err)
	}
}

func TestComposedApprovalRetainsUnrelatedAndStaleFindings(t *testing.T) {
	for _, kind := range []string{"extra-code", "stale-body", "missing-recipe", "changed-upstream"} {
		t.Run(kind, func(t *testing.T) {
			f, source := compositionFixture(t)
			r, c := f.Open()
			p, err := exceptions.Prepare(r, c, "record", []string{"read"}, exceptions.Options{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			if err = exceptions.Write(r, c, p); err != nil {
				t.Fatal(err)
			}
			f.Commit()
			switch kind {
			case "extra-code":
				source = strings.Replace(source, "fn second() {}", "fn second() { extra(); }", 1)
			case "stale-body":
				source = strings.Replace(source, "read();", "unreviewed();", 1)
			case "missing-recipe":
				source = strings.Replace(source, "\"bijection\"", "\"convex\"", 1)
			case "changed-upstream":
				f.Write(path, composedOriginal+"// new upstream\n")
				_, current := f.Open()
				f.Config = current
				f.Config.Upstream = f.Commit()
				f.Save()
				f.Commit()
			}
			f.Write(path, source)
			r, c = f.Open()
			report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
			if err != nil || len(report.Findings) == 0 || report.Files[0].Classification != "unexplained" {
				t.Fatal(report, err)
			}
			if kind == "extra-code" && (len(report.Findings) != 1 || report.Findings[0].Upstream.Start != 4 || len(report.Files[0].Approved) != 2) {
				t.Fatal(report.Findings, report.Files)
			}
		})
	}
}

func TestExceptionAuthoringAndInspectionRejectCrossingRecipes(t *testing.T) {
	for _, kind := range []string{"insertion-at-start", "insertion-at-end", "stale-binding"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := compositionFixture(t)
			r, c := f.Open()
			p, err := exceptions.Prepare(r, c, "record", []string{"read"}, exceptions.Options{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			pair := policy.Replacement{From: "fn first() {}\n", To: "// added at start\nfn first() {}\n"}
			if kind == "insertion-at-start" {
				pair.To = "// added at start\nfn first() {}\n"
			}
			if kind == "insertion-at-end" {
				pair.To = "fn first() {}\n// added at end\n"
			}
			f.Config.Transforms[0].Replacements = append(f.Config.Transforms[0].Replacements, pair)
			f.Save()
			r, c = f.Open()
			if _, err = exceptions.Prepare(r, c, "record", []string{"read"}, exceptions.Options{Path: path}); err == nil || !strings.Contains(err.Error(), "transformation public-name crosses the boundary") {
				t.Fatal("crossing preview accepted", err)
			}
			// Direct policy edits must be rejected by inspection as well.
			x := *p.Changes[0].Next
			if kind == "stale-binding" {
				x.ReplacementSHA256 = strings.Repeat("0", 64)
			}
			f.Config.Exceptions = []policy.Exception{x}
			f.Save()
			r, c = f.Open()
			report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, finding := range report.Findings {
				if finding.Kind == "stale_transform" && strings.Contains(finding.Message, "crosses source owned by read") {
					found = true
				}
			}
			if !found || report.Files[0].Classification != "unexplained" {
				t.Fatal(report.Findings, report.Files)
			}
		})
	}
}

// A fence may enclose transformed lines. Transformations apply first and the
// fence explains what remains, so its binding anchors the transformed original.
func TestFenceEnclosingTransformationExplainsTheRemainingDifference(t *testing.T) {
	brand := "// concave:begin brand\n// why: The native build also exports its edition.\n// witness: tests::read\n%s// concave:end brand\n"
	setup := func(t *testing.T, code string) (*testrepo.Fixture, string) {
		f, source := compositionFixture(t)
		source = strings.Replace(source, "const BRAND: &str = \"bijection\";\n", strings.Replace(brand, "%s", code, 1), 1)
		f.Write(path, source)
		return f, source
	}
	transformed := strings.Replace(composedOriginal, "\"convex\"", "\"bijection\"", 1)
	t.Run("composed", func(t *testing.T) {
		f, _ := setup(t, "const BRAND: &str = \"bijection\";\nconst EDITION: u8 = 1;\n")
		r, c := f.Open()
		p, err := exceptions.Prepare(r, c, "record", []string{"brand"}, exceptions.Options{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		next := p.Changes[0].Next
		if next.Start != 0 || next.End != 1 || next.UpstreamSHA256 != repository.Digest([]byte(transformed)) {
			t.Fatal(next)
		}
		diff := p.Changes[0].SourceDiff
		if !strings.Contains(diff, "-const BRAND: &str = \"bijection\";") || strings.Contains(diff, "\"convex\"") {
			t.Fatal("preview must show only the remaining difference", diff)
		}
		if err = exceptions.Write(r, c, p); err != nil {
			t.Fatal(err)
		}
		r, c = f.Open()
		p, err = exceptions.Prepare(r, c, "record", []string{"read"}, exceptions.Options{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if err = exceptions.Write(r, c, p); err != nil {
			t.Fatal(err)
		}
		r, c = f.Open()
		report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
		if err != nil || len(report.Findings) != 0 || len(report.Files[0].Approved) != 3 {
			t.Fatal(report.Findings, report.Files, err)
		}
		// Without the recipe the explanation no longer describes the difference.
		f.Config = c
		f.Config.Transforms = nil
		f.Save()
		r, c = f.Open()
		report, err = analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
		if err != nil || !hasFinding(report, "stale_exception", "brand") {
			t.Fatal(report.Findings, err)
		}
	})
	t.Run("upstream-anchored", func(t *testing.T) {
		// A binding reviewed against the pinned upstream owns the edits its span
		// encloses; its explanation already covers the whole difference.
		f, source := setup(t, "const BRAND: &str = \"bijection\";\nconst EDITION: u8 = 1;\n")
		r, c := f.Open()
		p, err := exceptions.Prepare(r, c, "record", []string{"read"}, exceptions.Options{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if err = exceptions.Write(r, c, p); err != nil {
			t.Fatal(err)
		}
		r, c = f.Open()
		code := "const BRAND: &str = \"bijection\";\nconst EDITION: u8 = 1;\n"
		f.Config = c
		f.Config.Exceptions = append(f.Config.Exceptions, policy.Exception{ID: "brand", Path: path, UpstreamSHA256: repository.Digest([]byte(composedOriginal)), Start: 0, End: 1, ReplacementSHA256: repository.Digest([]byte(code)), Fence: true})
		f.Save()
		f.Write(path, source)
		r, c = f.Open()
		report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
		if err != nil || len(report.Findings) != 0 {
			t.Fatal(report.Findings, err)
		}
	})
	t.Run("obsolete", func(t *testing.T) {
		f, _ := setup(t, "const BRAND: &str = \"bijection\";\n")
		r, c := f.Open()
		code := "const BRAND: &str = \"bijection\";\n"
		f.Config = c
		f.Config.Exceptions = append(f.Config.Exceptions, policy.Exception{ID: "brand", Path: path, UpstreamSHA256: repository.Digest([]byte(transformed)), Start: 0, End: 1, ReplacementSHA256: repository.Digest([]byte(code)), Fence: true})
		f.Save()
		r, c = f.Open()
		report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
		if err != nil || !hasFinding(report, "obsolete_exception", "brand") {
			t.Fatal(report.Findings, err)
		}
	})
}

func hasFinding(report *analysis.Report, kind, exception string) bool {
	for _, finding := range report.Findings {
		if finding.Kind == kind && finding.Exception == exception {
			return true
		}
	}
	return false
}

// Containment uses pinned-upstream coordinates. A smaller fence can keep the
// raw anchor while an enclosing fence includes the product-name recipe.
func TestMergeFencesWithDifferentValidCompositionAnchors(t *testing.T) {
	for _, variant := range []string{"valid", "stale-original", "missing-recipe", "outside"} {
		t.Run(variant, func(t *testing.T) {
			f, source := compositionFixture(t)
			brandCode := "const BRAND: &str = \"bijection\";\nconst EDITION: u8 = 1;\n"
			brand := "// concave:begin brand\n// why: Export the edition with the product name.\n// witness: tests::read\n" + brandCode + "// concave:end brand\n"
			f.Write(path, strings.Replace(source, "const BRAND: &str = \"bijection\";\n", brand, 1))
			anchors := map[string]string{}
			for _, id := range []string{"brand", "read"} {
				r, c := f.Open()
				p, err := exceptions.Prepare(r, c, exceptions.Record, []string{id}, exceptions.Options{Path: path})
				if err != nil {
					t.Fatal(err)
				}
				anchors[id] = p.Changes[0].Next.UpstreamSHA256
				if err = exceptions.Write(r, c, p); err != nil {
					t.Fatal(err)
				}
			}
			if anchors["brand"] == anchors["read"] {
				t.Fatal("fixture must exercise distinct anchors")
			}
			_, c := f.Open()
			f.Config = c
			switch variant {
			case "stale-original":
				for i := range f.Config.Exceptions {
					if f.Config.Exceptions[i].ID == "read" {
						f.Config.Exceptions[i].UpstreamSHA256 = strings.Repeat("0", 64)
					}
				}
			case "missing-recipe":
				f.Config.Transforms = nil
			}
			f.Save()
			code := brandCode + "\nfn first() { let native = \"convex\"; read(); }\n"
			tail := "\nfn second() {}\n"
			if variant == "outside" {
				code = brandCode
				tail = "\n" + fenced("fn first() { let native = \"convex\"; read(); }\n") + tail
			}
			f.Write(path, "// concave:begin merged\n// why: Keep the generated declarations together.\n// witness: tests::read\n"+code+"// concave:end merged\n"+tail)
			r, c := f.Open()
			p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"merged"}, exceptions.Options{Path: path, Replace: []string{"brand", "read"}})
			if variant != "valid" {
				if err == nil {
					t.Fatal("invalid replacement was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = exceptions.Write(r, c, p); err != nil {
				t.Fatal(err)
			}
			r, c = f.Open()
			report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
			if err != nil || len(report.Findings) != 0 || len(c.Exceptions) != 1 || c.Exceptions[0].ID != "merged" {
				t.Fatal(report, err)
			}
		})
	}
}
