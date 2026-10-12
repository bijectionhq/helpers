package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/syntax"
	"concave/internal/testrepo"
)

func structuralFixture(t *testing.T) *testrepo.Fixture {
	f := contractFixture(t)
	path := "npm-packages/convex/index.ts"
	before := "import { x } from \"convex/server\";\nconst wire = \"convex/server\";\n"
	after := strings.Replace(before, "\"convex/server\"", "\"@bijectionhq/bijection/server\"", 1)
	f.Write(path, before)
	f.Config.Upstream = f.Commit()
	fix := `"@bijectionhq/bijection/server"`
	rule := &syntax.Recipe{Language: "TypeScript", Rule: json.RawMessage(`{"kind":"string","regex":"^\"convex/server\"$","inside":{"kind":"import_statement"}}`), Fix: &fix}
	f.Config.Transforms = []policy.Transform{{ID: "public-import", Paths: []string{path}, ASTGrep: rule, Explanation: policy.Explanation{Why: "Adapt the public import only.", Witnesses: []string{path}}, Tests: []policy.TransformTest{
		{Name: "import-only", Path: path, Before: before, After: after},
		{Name: "wire-string-unchanged", Path: path, Before: "const wire = \"convex/server\";\n", After: "const wire = \"convex/server\";\n"},
	}}}
	f.Save()
	f.Commit()
	f.Write(path, after)
	return f
}
func TestStructuralRuleInspectTestAndExactEnforcement(t *testing.T) {
	f := structuralFixture(t)
	for _, args := range [][]string{{"rule", "test", "public-import", "--json"}, {"rule", "inspect", "public-import", "--json"}, {"check", "npm-packages/convex/index.ts", "--json"}} {
		n, out, err := run(f, args...)
		if n != 0 {
			t.Fatal(args, n, out, err)
		}
		if args[0] == "rule" && args[1] == "test" && (!strings.Contains(out, `"native_tests": {`) || !strings.Contains(out, `"cases": 2`)) {
			t.Fatal("native tests were not reported", out)
		}
		if args[0] == "rule" && args[1] == "inspect" && !strings.Contains(out, syntax.Identity) {
			t.Fatal("engine identity missing", out)
		}
	}
	f.Write("npm-packages/convex/index.ts", "import { x } from \"@bijectionhq/bijection/server\";\nconst wire = \"@bijectionhq/bijection/server\";\n")
	n, out, err := run(f, "check", "npm-packages/convex/index.ts", "--json")
	if n != 1 || !strings.Contains(out, "unexplained_change") {
		t.Fatal(n, out, err)
	}
	f.Config.Transforms[0].Tests[0].After = "wrong"
	f.Save()
	n, out, err = run(f, "rule", "test", "public-import", "--json")
	if n != 1 || !strings.Contains(out, `"passed": false`) {
		t.Fatal(n, out, err)
	}
}
func TestStructuralRecoveryRetainsAllowedImport(t *testing.T) {
	f := structuralFixture(t)
	land, err := os.ReadFile("../../../../scripts/land.py")
	if err != nil {
		t.Fatal(err)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Save()
	f.Write("npm-packages/convex/index.ts", "import { x } from \"@bijectionhq/bijection/server\";\nconst wire = \"convex/server\";\nextra();\n")
	f.Commit()
	reportDir := filepath.Join(f.Root, "target/report")
	n, out, stderr := run(f, "inspect", "npm-packages/convex/index.ts", "--rev", "HEAD", "--output", reportDir, "--json")
	if n != 0 || !strings.Contains(out, "public-import") || !strings.Contains(out, "unexplained_change") {
		t.Fatal(n, out, stderr)
	}
	var report analysis.Report
	if err = json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	hunk := ""
	for _, h := range report.Hunks {
		if h.After == "extra();\n" {
			hunk = h.ID
		}
	}
	if hunk == "" {
		t.Fatal("unrelated edit missing")
	}
	planDir := filepath.Join(f.Root, "target/plan")
	n, out, stderr = run(f, "recover", "plan", filepath.Join(reportDir, "inspection.json"), "--hunk", hunk, "--reason", "Remove unrelated behavior while preserving the public import.", "--output", planDir)
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	planBytes, err := os.ReadFile(filepath.Join(planDir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan recovery.Plan
	if err = json.Unmarshal(planBytes, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SyntaxEngine != syntax.Identity {
		t.Fatal("plan omitted engine identity", plan.SyntaxEngine)
	}
	for _, identity := range []string{"", "ast-grep 0.0.0"} {
		plan.SyntaxEngine = identity
		bad, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		f.Write("target/bad-plan.json", string(bad))
		n, out, stderr = run(f, "recover", "apply", filepath.Join(f.Root, "target/bad-plan.json"))
		if n != 3 || !strings.Contains(stderr, "syntax engine requirement changed") {
			t.Fatal(n, out, stderr)
		}
	}
	selectedEngine := os.Getenv("CONCAVE_AST_GREP")
	for _, enginePath := range []string{filepath.Join(f.Root, "absent-engine"), "/usr/bin/true"} {
		t.Setenv("CONCAVE_AST_GREP", enginePath)
		n, out, stderr = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
		if n != 2 {
			t.Fatal("unavailable engine applied plan", n, out, stderr)
		}
		unchanged, err := os.ReadFile(filepath.Join(f.Root, "npm-packages/convex/index.ts"))
		if err != nil || !strings.Contains(string(unchanged), "extra();") {
			t.Fatal("refused plan changed source", err)
		}
	}
	t.Setenv("CONCAVE_AST_GREP", selectedEngine)
	n, out, stderr = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	data, err := os.ReadFile(filepath.Join(f.Root, "npm-packages/convex/index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "import { x } from \"@bijectionhq/bijection/server\";\nconst wire = \"convex/server\";\n" {
		t.Fatal(string(data))
	}
	// An exception enclosing the structural match explains only what remains
	// after it: its preview's original is the transformed line.
	f.Write("npm-packages/convex/index.ts", "// concave:begin overlap\n// why: A custom server module.\n// witness: crates/core/adapter.rs\nimport { x } from \"custom/server\";\n// concave:end overlap\nconst wire = \"convex/server\";\n")
	n, out, stderr = run(f, "exception", "record", "overlap", "--path", "npm-packages/convex/index.ts", "--json")
	if n != 0 || !strings.Contains(out, `-import { x } from \"@bijectionhq/bijection/server\";`) {
		t.Fatal(n, out, stderr)
	}
}
func TestStructuralRulesFailClosed(t *testing.T) {
	for _, kind := range []string{"missing-engine", "wrong-engine", "invalid-rule", "zero-fixtures", "missing-fix", "two-recipes", "wrong-language"} {
		t.Run(kind, func(t *testing.T) {
			f := structuralFixture(t)
			switch kind {
			case "missing-engine":
				t.Setenv("CONCAVE_AST_GREP", f.Root+"/missing")
			case "wrong-engine":
				t.Setenv("CONCAVE_AST_GREP", "/usr/bin/true")
			case "invalid-rule":
				f.Config.Transforms[0].ASTGrep.Rule = json.RawMessage(`{"kind":"not_a_real_node"}`)
			case "zero-fixtures":
				f.Config.Transforms[0].Tests = nil
			case "missing-fix":
				f.Config.Transforms[0].ASTGrep.Fix = nil
			case "two-recipes":
				f.Config.Transforms[0].Replacements = []policy.Replacement{{From: "convex", To: "bijection"}}
			case "wrong-language":
				f.Config.Transforms[0].ASTGrep.Language = "Rust"
			}
			f.Save()
			n, _, _ := run(f, "rule", "test", "public-import", "--json")
			if n == 0 {
				t.Fatal("invalid rule or missing dependency passed")
			}
		})
	}
}

func TestRuleInspectValidatesLocalReferencesForEveryPath(t *testing.T) {
	f := contractFixture(t)
	first := "// concave:invariant local\n// Retain the interface.\nfn owner() {}\n"
	f.Write("crates/core/one.rs", first)
	f.Write("crates/core/two.rs", "fn owner() {}\n")
	f.Config.Upstream = f.Commit()
	f.Config.Transforms = []policy.Transform{{ID: "rename", Paths: []string{"crates/core/one.rs", "crates/core/two.rs"}, Replacements: []policy.Replacement{{From: "fn owner", To: "fn renamed"}}, Explanation: policy.Explanation{Why: "Public owner spelling.", Witnesses: []string{"crates/core/one.rs"}, Preserves: []string{"#local"}}}}
	f.Save()
	n, _, stderr := run(f, "rule", "inspect", "rename", "--json")
	if n != 2 || !strings.Contains(stderr, "two.rs#local") {
		t.Fatal("unchecked second owner", n, stderr)
	}
}

func TestStructuralRecipeComposesWithDisjointException(t *testing.T) {
	f := structuralFixture(t)
	path := "npm-packages/convex/index.ts"
	before := "import { x } from \"convex/server\";\n\nfunction adapter() { return 1; }\n"
	f.Write(path, before)
	f.Config.Upstream = f.Commit()
	f.Save()
	f.Commit()
	after := strings.Replace(before, "convex/server", "@bijectionhq/bijection/server", 1)
	after = strings.Replace(after, "function adapter() { return 1; }\n", "// concave:begin adapter\n// why: Keep the local adapter return value.\n// witness: crates/core/adapter.rs\nfunction adapter() { return 2; }\n// concave:end adapter\n", 1)
	f.Write(path, after)
	n, out, err := run(f, "exception", "record", "adapter", "--path", path, "--json")
	if n != 0 {
		t.Fatal(n, out, err)
	}
	// Recording is a preview; use the proposal's binding to exercise composition.
	var preview struct {
		Changes []struct {
			Exception policy.Exception `json:"next"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Changes[0].Exception.ID == "" {
		t.Fatal(out)
	}
	f.Config.Exceptions = append(f.Config.Exceptions, preview.Changes[0].Exception)
	f.Save()
	f.Commit()
	n, out, err = run(f, "check", path, "--json")
	if n != 0 || !strings.Contains(out, "public-import") || !strings.Contains(out, "adapter") {
		t.Fatal(n, out, err)
	}
}
