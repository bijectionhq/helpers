package policy_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/syntax"
)

// scripts reads TypeScript and JavaScript fences through the pinned parser.
func scripts(t *testing.T) *policy.Session {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return &policy.Session{Syntax: syntax.New(context.Background(), root)}
}

// Product copy no longer bends around the scanner: apostrophes in JSX text and
// URLs in template strings are not literals that hide later fences.
func TestScriptCommentsComeFromTheParser(t *testing.T) {
	s := scripts(t)
	for name, prefix := range map[string]string{
		"jsx apostrophe":     "export const A = () => <p>The deployment's page</p>;\n",
		"template url":       "const u = `https://${host}/x`;\n",
		"template comment":   "const t = `\n" + annotation("fake();\n") + "`;\n",
		"regex quote":        "const pattern = /[\"'`]/g;\n",
		"regex slashes":      "const p = /\\/\\//;\n",
		"division comment":   "const n = a / b /*\n// concave:begin fake\n*/;\n",
		"jsx text slashes":   "export const B = () => (\n  <div>\n    // concave:begin fake\n  </div>\n);\n",
		"jsx attribute":      "export const C = () => <a title=\"// concave:begin fake\" href=\"it's\" />;\n",
		"string with marker": "const s = \"// concave:begin fake\";\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := "x.tsx"
			as, bad := s.Annotations(path, prefix+annotation("read();\n"))
			if len(bad) != 0 || len(as) != 1 || as["read"].Code != "read();\n" {
				t.Fatal(as, bad)
			}
		})
	}
}

// A JSX expression container holding one block comment, alone on its lines,
// carries a fence among JSX children; other block comments never do.
func TestJSXChildrenCarryContainerFences(t *testing.T) {
	s := scripts(t)
	text := "export const D = () => (\n  <div>\n    {/* concave:begin read\n    why: protect the current read\n    witness: tests::read\n    */}\n    <Read />\n    {/* concave:end read */}\n    <p>{/* concave:begin inline */} text</p>\n  </div>\n);\n/* concave:begin block */\n"
	for _, path := range []string{"x.tsx", "x.jsx", "x.js"} {
		as, bad := s.Annotations(path, text)
		if len(bad) != 0 || len(as) != 1 || strings.TrimSpace(as["read"].Code) != "<Read />" || as["read"].Explanation.Why != "protect the current read" {
			t.Fatal(path, as, bad)
		}
	}
	if _, bad := s.Annotations("x.ts", "const x = 1;\n// concave:begin read\n"); len(bad) == 0 {
		t.Fatal("unclosed fence accepted")
	}
}

func TestScriptCommentsNeedTheParser(t *testing.T) {
	if _, bad := policy.Annotations("x.ts", annotation("read();\n")); len(bad) != 1 || !strings.Contains(bad[0].Message, "syntax engine") {
		t.Fatal(bad)
	}
	s := scripts(t)
	if _, bad := s.Annotations("x.ts", "const x = <<;\n"+annotation("read();\n")); len(bad) != 0 {
		t.Fatal("a distant syntax error hid a fence", bad)
	}
	if _, bad := s.Annotations("x.ts", "const x = <<; // concave:begin read\n"); len(bad) == 0 || !strings.Contains(bad[0].Message, "does not parse near an annotation") {
		t.Fatal(bad)
	}
}
