package policy_test

import (
	"concave/internal/repository"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/testrepo"
)

func literalTransform() policy.Transform {
	return policy.Transform{ID: "fixture-urls", Paths: []string{"npm-packages/convex/index.ts"}, Replacements: []policy.Replacement{{From: ".convex.cloud", To: ".bijection.cloud"}}, Explanation: policy.Explanation{Why: "Use the Bijection deployment identity in rendered fixture URLs.", Witnesses: []string{"owner.ts"}}}
}
func TestLiteralTransformValidation(t *testing.T) {
	for _, kind := range []string{"valid", "module", "empty paths", "duplicate path", "directory", "unsafe path", "protected", "two transforms", "missing source", "two sources", "empty literal", "same literal", "repeated literal", "missing explanation", "missing manifest", "span overlap"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			x := literalTransform()
			switch kind {
			case "module":
				x.Module = "example.js"
				x.Replacements = nil
				f.Config.TransformationManifest = "manifest.json"
			case "empty paths":
				x.Paths = nil
			case "duplicate path":
				x.Paths = append(x.Paths, x.Paths[0])
			case "directory":
				x.Paths = []string{"npm-packages/"}
			case "unsafe path":
				x.Paths = []string{"../owner.ts"}
			case "protected":
				x.Paths = f.Config.PreserveExactly
			case "missing source":
				x.Replacements = nil
			case "two sources":
				x.Module = "example.js"
				f.Config.TransformationManifest = "manifest.json"
			case "empty literal":
				x.Replacements[0].From = ""
			case "same literal":
				x.Replacements[0].To = x.Replacements[0].From
			case "repeated literal":
				x.Replacements = append(x.Replacements, x.Replacements[0])
			case "missing explanation":
				x.Explanation.Why = ""
			case "missing manifest":
				x.Module = "example.js"
				x.Replacements = nil
			case "span overlap":
				y := fileException()
				y.Path = x.Paths[0]
				f.Config.Exceptions = []policy.Exception{y}
			}
			f.Config.Transforms = []policy.Transform{x}
			if kind == "two transforms" {
				x.ID = "overlap"
				f.Config.Transforms = append(f.Config.Transforms, x)
			}
			err := f.Config.Validate()
			if valid := kind == "valid" || kind == "module"; (err == nil) != valid {
				t.Fatal(kind, err)
			}
		})
	}
}
func TestLiteralTransformReconstructsOnlyDeclaredSource(t *testing.T) {
	x := literalTransform()
	x.Paths = append(x.Paths, "second.ts")
	c := &policy.Config{Transforms: []policy.Transform{x}}
	before := []byte("const url = 'https://one.convex.cloud';\nconst wire = 'convex';\n")
	after := []byte("const url = 'https://one.bijection.cloud';\nconst wire = 'convex';\n")
	for _, path := range x.Paths {
		class, ids, fs := c.Classify(path, before, after, nil)
		if class != "transformed" || len(ids) != 1 || len(fs) != 0 {
			t.Fatal(class, ids, fs)
		}
	}
	for _, v := range []struct {
		path          string
		before, after []byte
	}{
		{"elsewhere.ts", before, after},
		{x.Paths[0], before, []byte("const url = 'https://one.bijection.cloud';\nconst wire = 'bijection';\n")},
		{x.Paths[0], before, append(append([]byte{}, after...), []byte("extra();\n")...)},
		{x.Paths[0], before, before},
		{x.Paths[0], after, after},
	} {
		class, _, fs := c.Classify(v.path, v.before, v.after, nil)
		if class != "unexplained" || len(fs) == 0 {
			t.Fatal("escaped literal scope", class, fs)
		}
	}
	c.Transforms[0].Replacements = append(c.Transforms[0].Replacements, policy.Replacement{From: "missing original", To: "new"})
	if class, _, _ := c.Classify(x.Paths[0], before, after, nil); class != "unexplained" {
		t.Fatal("missing literal accepted")
	}
	c.Transforms[0].Replacements = []policy.Replacement{{From: "convex", To: "bijection"}, {From: "bijection", To: "convex"}}
	if class, _, fs := c.Classify(x.Paths[0], before, before, nil); class != "unexplained" || len(fs) == 0 {
		t.Fatal("cancelling rule accepted")
	}
}

func TestLiteralTransformBoundsExpansionBeforeAllocation(t *testing.T) {
	x := literalTransform()
	x.Replacements = []policy.Replacement{{From: "x", To: strings.Repeat("y", 2048)}}
	if _, err := x.Apply(x.Paths[0], policy.Manifest{}, strings.Repeat("x", repository.MaxBytes)); err == nil {
		t.Fatal("unbounded expansion accepted")
	}
	x.Replacements = []policy.Replacement{{From: "x", To: "y"}}
	if _, err := x.Apply(x.Paths[0], policy.Manifest{}, strings.Repeat("x", repository.MaxBytes+1)); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestReplacementScopesAreExplicitSubsets(t *testing.T) {
	for _, kind := range []string{"valid", "outside", "duplicate", "empty", "directory", "uncovered"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			x := literalTransform()
			x.Paths = append(x.Paths, "crates/core/lib.rs")
			x.Replacements[0].Paths = []string{x.Paths[0]}
			x.Replacements = append(x.Replacements, policy.Replacement{From: "pub struct One;", To: "#[derive(Test)]\npub struct One;", Paths: []string{x.Paths[1]}})
			switch kind {
			case "outside":
				x.Replacements[0].Paths = []string{"outside.rs"}
			case "duplicate":
				x.Replacements[0].Paths = []string{x.Paths[0], x.Paths[0]}
			case "empty":
				x.Replacements[0].Paths = []string{}
			case "directory":
				x.Replacements[0].Paths = []string{"npm-packages/"}
			case "uncovered":
				x.Replacements[1].Paths = []string{x.Paths[0]}
			}
			f.Config.Transforms = []policy.Transform{x}
			if err := f.Config.Validate(); (err == nil) != (kind == "valid") {
				t.Fatal(kind, err)
			}
		})
	}
}

// Token rules rename whole names only. They never produce names that exist
// nowhere: the inherited npm package, unreleased scopes, upstream hosts and
// repositories, and wire contract names keep their text.
func TestTokenTransformRenamesWholeNamesOnly(t *testing.T) {
	path := "apps/docs/page.mdx"
	x := policy.Transform{ID: "docs-names", Paths: []string{path}, Tokens: &policy.Tokens{
		Rules: []policy.Replacement{
			{From: "Convex", To: "Bijection"}, {From: "Convex dashboard", To: "Bijection console"},
			{From: "convex/", To: "bijection/"}, {From: "npx convex dev", To: "bijection dev"},
			{From: "@convex-dev/agent", To: "@bijectionhq/agent"}, {From: "convex-svelte", To: "@bijectionhq/svelte"},
			{From: "ConvexReactClient", To: "BijectionReactClient"},
			{From: "https://dashboard.convex.dev", To: "https://console.bijection.com"},
			{From: "CONVEX_URL", To: "BIJECTION_URL", Paths: []string{path}},
		},
		Except: []string{"@convex-dev/", "npx convex@", "/convex-svelte"},
	}}
	cases := map[string]string{
		"Convex's Convex dashboard.\n":                          "Bijection's Bijection console.\n",
		"import { v } from \"convex/values\";\n":                "import { v } from \"bijection/values\";\n",
		"npx convex dev --once\n":                               "bijection dev --once\n",
		"@convex-dev/agent/react and convex-svelte/sveltekit\n": "@bijectionhq/agent/react and @bijectionhq/svelte/sveltekit\n",
		"new ConvexReactClient(CONVEX_URL)\n":                   "new BijectionReactClient(BIJECTION_URL)\n",
		"[console](https://dashboard.convex.dev)\n":             "[console](https://console.bijection.com)\n",
	}
	for before, want := range cases {
		got, err := x.Apply(path, policy.Manifest{}, before+"CONVEX_URL\n")
		if err != nil || got != want+"BIJECTION_URL\n" {
			t.Fatalf("%q: %q %v", before, got, err)
		}
	}
	unchanged := []string{
		"npm install convex\n", "npm create convex@latest\n", "npx convex@latest dev\n",
		"@convex-dev/workflow and @convex-dev/agent-extra\n", "convex-nuxt and get-convex/agent\n",
		"https://github.com/get-convex/convex/blob/main/convex/x.ts\n", "https://www.convex.dev\n",
		"https://dashboard.convex.dev/t/settings\n", "ConvexError isConvexValidator VITE_CONVEX_SITE_URL\n",
		"[test](/testing/convex-svelte)\n", "ConvexHull\n",
	}
	for _, before := range unchanged {
		got, err := x.Apply(path, policy.Manifest{}, before+"CONVEX_URL\n")
		if err != nil || got != before+"BIJECTION_URL\n" {
			t.Fatalf("%q: %q %v", before, got, err)
		}
	}
	if _, err := x.Apply(path, policy.Manifest{}, "Convex\n"); err == nil || !strings.Contains(err.Error(), "expected token absent") {
		t.Fatal("a scoped rule silently stopped matching", err)
	}
	if _, err := x.Apply(path, policy.Manifest{}, "nothing to rename\n"); err == nil {
		t.Fatal("a transformation without a change was accepted")
	}
}
