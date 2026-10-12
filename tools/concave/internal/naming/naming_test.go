package naming

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPolicy() Policy {
	return Policy{Vocabulary: "wiki/vocabulary.md", Scopes: []Scope{{ID: "metadata", Paths: []string{"sdk/"}, Surface: "sdk_metadata"}}, Rules: []Rule{{ID: "naming/sdk-field", Scopes: []string{"metadata"}, Roles: []string{"field"}, Predicate: "camel_case"}}}
}
func TestNamingPolicyRejectsAmbiguousSelectionAndExternalBindings(t *testing.T) {
	p := testPolicy()
	if _, err := p.Select([]string{"naming/missing"}); err == nil {
		t.Fatal("unknown selection accepted")
	}
	if _, err := p.Select([]string{"naming/sdk-field", "naming/sdk-field"}); err == nil {
		t.Fatal("duplicate selection accepted")
	}
	p.External = []External{{Rule: "naming/sdk-field", Path: "sdk/api.ts", Symbol: "Reply/provider_name"}}
	if p.Validate() == nil {
		t.Fatal("unexplained external spelling accepted")
	}
}
func TestNamingSurfaceCaseAndStableIdentity(t *testing.T) {
	p := testPolicy()
	rules, _ := p.Select(nil)
	fact := Fact{Path: "sdk/api.ts", Name: "execution_id", Symbol: "Reply/execution_id", Parent: "Reply", Role: "field", Language: "TypeScript", Line: 2, Column: 3}
	findings, coverage := Evaluate(p, rules, []Fact{fact}, nil)
	if len(findings) != 1 || findings[0].Expected != "executionId" || coverage[0].State != "failed" {
		t.Fatal(findings, coverage)
	}
	fact.Line = 300
	moved, _ := Evaluate(p, rules, []Fact{fact}, nil)
	if moved[0].ID != findings[0].ID {
		t.Fatal("line movement changed obligation identity")
	}
	fact.Surface = "wire"
	findings, coverage = Evaluate(p, rules, []Fact{fact}, nil)
	if len(findings) != 0 || coverage[0].State != "not_applicable" {
		t.Fatal("wire field acquired SDK authority", findings)
	}
}
func TestNamingKnownBooleanAndInstantOnly(t *testing.T) {
	p := testPolicy()
	p.Rules[0].Predicate = "is_prefix"
	facts := []Fact{{Path: "sdk/api.ts", Name: "ready", Symbol: "Reply/ready", Role: "field", Language: "TypeScript", Value: "boolean"}, {Path: "sdk/api.ts", Name: "time", Symbol: "Reply/time", Role: "field", Language: "TypeScript"}}
	rules, _ := p.Select(nil)
	found, c := Evaluate(p, rules, facts, nil)
	if len(found) != 1 || found[0].Expected != "isReady" || c[0].Facts != 1 {
		t.Fatal(found, c)
	}
	p.Rules[0].Predicate = "at_suffix"
	facts[0].Name = "completed"
	facts[0].Value = "instant"
	rules, _ = p.Select(nil)
	found, _ = Evaluate(p, rules, facts, nil)
	if len(found) != 1 || found[0].Expected != "completedAt" {
		t.Fatal(found)
	}
}
func TestNamingAcronymsFollowLanguage(t *testing.T) {
	for _, c := range []struct{ name, style, language, want string }{{"executionId", "camel_case", "Go", "executionID"}, {"PolicySHA256", "pascal_case", "Go", "PolicySHA256"}, {"HTTPClient", "pascal_case", "Go", "HTTPClient"}, {"query_binding", "camel_case", "TypeScript", "queryBinding"}, {"QueryBinding", "snake_case", "Rust", "query_binding"}} {
		if got := casing(c.name, c.style, c.language); got != c.want {
			t.Fatal(c, got)
		}
	}
}
func TestNamingVocabularyDeclarationDoesNotBanOrdinaryProse(t *testing.T) {
	v, err := ReadVocabulary("| Word | Meaning | Not |\n| --- | --- | --- |\n| wait | retained state | attempt |\n| external call | intent | effect |\n")
	if err != nil {
		t.Fatal(err)
	}
	v.Symbols["defineaction"] = "defineOperation"
	facts := DocumentFacts("wiki/topic.md", "A transaction attempt can roll back a local effect.\n## Attempt\n**attempt** owns delivery.\n```ts\nconst x = defineAction({});\n```", v)
	declared := 0
	for _, f := range facts {
		if f.Value != "prose" {
			declared++
		}
	}
	if declared != 3 {
		t.Fatal(facts)
	}
	if _, err := ReadVocabulary("| Word | Meaning | Not |\n| --- | --- | --- |\n| wait | x | |\n| wait | y | |\n"); err == nil {
		t.Fatal("duplicate glossary accepted")
	}
}
func TestNamingCapturedSourceWithoutGitAndOutsideScope(t *testing.T) {
	root := t.TempDir()
	vocab := "| Word | Meaning | Not |\n| --- | --- | --- |\n| operation | x | action |\n"
	page := "## Action\n"
	for path, body := range map[string]string{"wiki/vocabulary.md": vocab, "wiki/topic.md": page, "elsewhere/foreign.md": "## Action\n"} {
		full := filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(full), 0700)
		os.WriteFile(full, []byte(body), 0600)
	}
	p := Policy{Vocabulary: "wiki/vocabulary.md", Scopes: []Scope{{ID: "docs", Paths: []string{"wiki/", "elsewhere/"}, Surface: "documentation"}}, Rules: []Rule{{ID: "naming/terminology", Scopes: []string{"docs"}, Roles: []string{"term"}, Predicate: "canonical_term"}}}
	input := Input{Format: 1, Root: root, Target: "wiki", SourceID: strings.Repeat("a", 64), PolicySHA256: strings.Repeat("b", 64), Files: map[string]string{"wiki/vocabulary.md": Digest([]byte(vocab)), "wiki/topic.md": Digest([]byte(page)), "elsewhere/foreign.md": Digest([]byte(page))}}
	report, err := Run(context.Background(), input, p)
	if err != nil || report.State != "failed" || len(report.Findings) != 1 || report.Findings[0].Path != "wiki/topic.md" {
		t.Fatal(report, err)
	}
	os.WriteFile(filepath.Join(root, "wiki/topic.md"), []byte("changed"), 0600)
	if _, err := Run(context.Background(), input, p); err == nil {
		t.Fatal("changed captured source accepted")
	}
}
func TestNamingInputRefusesLinksOutsideCustody(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.Symlink(outside, filepath.Join(root, "link"))
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := Read(r, "link"); err == nil {
		t.Fatal("escaped custody")
	}
}
