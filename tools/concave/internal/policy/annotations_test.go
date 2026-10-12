package policy_test

import (
	"concave/internal/contracts"
	"concave/internal/policy"
	"concave/internal/repository"
	"fmt"
	"strings"
	"testing"
)

const header = "// why: protect the current read\n// witness: tests::read\n"

func TestTypeScriptModuleExceptionsAndRepeatedPreserves(t *testing.T) {
	for _, path := range []string{"owner.mts", "owner.cts"} {
		if !policy.SupportsAnnotations(path) {
			t.Fatal(path)
		}
		text := "// concave:begin read\n" + header + "// preserves: #atomic\n// preserves: other.ts#stable\nchanged();\n// concave:end read\n"
		annotations, findings := scripts(t).Annotations(path, text)
		if len(findings) != 0 || len(annotations["read"].Explanation.Preserves) != 2 {
			t.Fatal(path, annotations, findings)
		}
	}
}

func annotation(code string) string {
	return "// concave:begin read\n" + header + code + "// concave:end read\n"
}
func TestStructuredAnnotations(t *testing.T) {
	text := annotation("read();\n")
	as, bad := policy.Annotations("x.rs", text)
	if len(bad) != 0 || as["read"].Explanation.Why != "protect the current read" || as["read"].Code != "read();\n" {
		t.Fatal(as, bad)
	}
	for name, s := range map[string]string{
		"missing why":      strings.Replace(text, "// why: protect the current read\n", "", 1),
		"empty why":        strings.Replace(text, "protect the current read", "", 1),
		"missing witness":  strings.Replace(text, "// witness: tests::read\n", "", 1),
		"duplicate key":    strings.Replace(text, header, header+"// why: another reason\n", 1),
		"retired contract": strings.Replace(text, header, header+"// contract: wiki.md#unstable\n", 1),
		"unknown key":      strings.Replace(text, header, header+"// ignore: all\n", 1),
		"nested":           strings.Replace(text, "read();", "// concave:begin nested\nread();", 1),
		"duplicate id":     text + text,
		"unclosed":         strings.Replace(text, "// concave:end read\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, bad := policy.Annotations("x.rs", s)
			if len(bad) == 0 {
				t.Fatal("invalid annotation accepted")
			}
			for _, f := range bad {
				if f.Rule != "exception/valid" || f.Line < 1 {
					t.Fatal(f)
				}
			}
		})
	}
}
func TestAnnotationsIgnoreLiteralAndBlockCommentMarkers(t *testing.T) {
	for _, s := range []string{"/*\n" + annotation("fake();\n") + "*/\n", "let s = r###\"\n" + annotation("fake();\n") + "\"###;\n", "let s = \"\n" + annotation("fake();\n") + "\";\n"} {
		as, bad := policy.Annotations("x.rs", s+annotation("real();\n"))
		if len(bad) != 0 || len(as) != 1 || as["read"].Code != "real();\n" {
			t.Fatal(as, bad)
		}
	}
	text := "const s = `escaped \\`\n" + annotation("fake();\n") + "`;\n" + annotation("real();\n")
	as, bad := scripts(t).Annotations("x.ts", text)
	if len(bad) != 0 || as["read"].Code != "real();\n" {
		t.Fatal(as, bad)
	}
}
func TestAnnotationContinuationsAndRemoval(t *testing.T) {
	s := strings.Replace(annotation("read();\n"), "// witness:", "// while retaining pending writes.\n// remove-when: upstream supports this read\n// witness:", 1)
	as, bad := policy.Annotations("x.rs", s)
	if len(bad) != 0 || as["read"].Explanation.RemoveWhen == "" || !strings.HasSuffix(as["read"].Explanation.Why, "pending writes.") {
		t.Fatal(as, bad)
	}
}
func TestFindingRangesAndStableRule(t *testing.T) {
	c := policy.Config{}
	h := repository.Hunk{Start: 4, End: 6, CurrentStart: 7, CurrentEnd: 10}
	_, _, fs := c.Classify("x.rs", []byte("old"), []byte("new"), []repository.Hunk{h})
	if len(fs) != 1 || fs[0].Rule != "source/covered" || fs[0].Line != 8 || fs[0].EndLine != 10 || fs[0].Upstream.Start != 4 || fs[0].Upstream.End != 6 {
		t.Fatal(fs)
	}
}
func TestAnnotationSpanRejectsBoundaryCrossing(t *testing.T) {
	a := policy.Annotation{Start: 5, End: 9}
	hs := []repository.Hunk{{Start: 2, End: 3, CurrentStart: 2, CurrentEnd: 4}, {Start: 4, End: 5, CurrentStart: 5, CurrentEnd: 9}}
	span, err := policy.AnnotationSpan(a, hs)
	if err != nil || span.Start != 4 || span.End != 5 {
		t.Fatal(span, err)
	}
	a.Start = 6
	if _, err = policy.AnnotationSpan(a, hs); err == nil {
		t.Fatal("crossing hunk accepted")
	}
}
func TestReferenceValidation(t *testing.T) {
	m := policy.Explanation{Why: "read invariant", Witnesses: []string{"tests::read"}}
	read := func(p string) ([]byte, error) {
		if p != "x.rs" {
			return nil, fmt.Errorf("missing witness")
		}
		return []byte("fn read() {}\n"), nil
	}
	if err := policy.CheckReferences("x.rs", m, contracts.New(read, nil)); err != nil {
		t.Fatal(err)
	}
	m.Witnesses = []string{"missing.rs::read"}
	if err := policy.CheckReferences("x.rs", m, contracts.New(read, nil)); err == nil {
		t.Fatal("missing witness accepted")
	}
}

func TestFormatterWrappedReferences(t *testing.T) {
	s := annotation("read();\n")
	s = strings.Replace(s, "tests::read", "tests::\n// read", 1)
	as, bad := policy.Annotations("x.rs", s)
	if len(bad) > 0 || as["read"].Explanation.Witnesses[0] != "tests::read" {
		t.Fatal(as, bad)
	}
	s = strings.Replace(s, "tests::\n// read", "test.rs::\n// tests::read", 1)
	as, bad = policy.Annotations("x.rs", s)
	if len(bad) > 0 || as["read"].Explanation.Witnesses[0] != "test.rs::tests::read" {
		t.Fatal(as, bad)
	}
}

func TestAnnotationParagraphSeparators(t *testing.T) {
	text := "// concave:begin read\n//\n// why: protect the current read\n//\n//\n// witness: tests::read\n//\n// witness: other.rs::tests::read\n//\n// remove-when: upstream owns this\n//\nread();\n// concave:end read\n"
	as, bad := policy.Annotations("x.rs", text)
	a := as["read"]
	if len(bad) != 0 || a.Code != "read();\n" || a.Explanation.Why != "protect the current read" || len(a.Explanation.Witnesses) != 2 || a.Explanation.RemoveWhen != "upstream owns this" {
		t.Fatal(as, bad)
	}
}

func TestRustDocumentationEndsAnnotationMetadata(t *testing.T) {
	for _, doc := range []string{
		"/// Exact native revision.\n/// More documentation.\n",
		"//! Exact native revision.\n",
		"/// witness: this is item documentation, not an extra reference.\n",
		"/// concave:begin ignored-documentation\n",
	} {
		code := doc + "fn read() {}\n"
		as, bad := policy.Annotations("x.rs", annotation(code))
		a := as["read"]
		if len(bad) != 0 || len(as) != 1 || a.Code != code || len(a.Explanation.Witnesses) != 1 || a.Explanation.Witnesses[0] != "tests::read" {
			t.Fatal(as, bad)
		}
		if a.CodeSHA256() != repository.Digest([]byte(code)) {
			t.Fatal("documentation was not retained in the replacement fingerprint", a.Code)
		}
	}
}
