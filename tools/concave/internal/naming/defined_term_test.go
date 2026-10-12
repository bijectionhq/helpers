package naming

import "testing"

func TestNamingUndefinedDeclaredTermAndOrdinaryProse(t *testing.T) {
	v, err := ReadVocabulary("| Word | Meaning | Not |\n| --- | --- | --- |\n| answer | complete output | |\n")
	if err != nil {
		t.Fatal(err)
	}
	canonical := map[string]string{"defined:answer": "answer"}
	p := Policy{Vocabulary: "wiki/vocabulary.md", Scopes: []Scope{{ID: "docs", Paths: []string{"wiki/"}, Surface: "documentation"}}, Rules: []Rule{{ID: "naming/defined-term", Scopes: []string{"docs"}, Roles: []string{"term"}, Predicate: "defined_term"}}}
	rules, _ := p.Select(nil)
	facts := DocumentFacts("wiki/topic.md", "An invented word in ordinary prose.\n**invented** is a new primitive.\n**answer** is complete.", v)
	found, c := Evaluate(p, rules, facts, canonical)
	if len(found) != 1 || found[0].Actual != "invented" || c[0].Facts != 2 {
		t.Fatal(found, c)
	}
}
func TestNamingExternalBindingCannotSuppressAnotherSymbol(t *testing.T) {
	p := testPolicy()
	p.External = []External{{Rule: "naming/sdk-field", Path: "sdk/api.ts", Symbol: "Reply/provider_name", Reason: "provider field", Authority: "provider contract"}}
	rules, _ := p.Select(nil)
	facts := []Fact{{Path: "sdk/api.ts", Name: "provider_name", Symbol: "Reply/provider_name", Role: "field", Language: "TypeScript"}, {Path: "sdk/api.ts", Name: "other_name", Symbol: "Reply/other_name", Role: "field", Language: "TypeScript"}}
	found, _ := Evaluate(p, rules, facts, nil)
	if len(found) != 1 || found[0].Actual != "other_name" {
		t.Fatal(found)
	}
	found, _ = Evaluate(p, rules, []Fact{{Path: "sdk/api.ts", Role: "file"}}, nil)
	if len(found) != 1 || found[0].Symbol != "Reply/provider_name" {
		t.Fatal("stale exemption accepted", found)
	}
}
