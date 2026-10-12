package syntax

import (
	"sort"
	"strings"
)

// TestCase is one statically titled it/test call of a Jest or Vitest file.
// Titles are those of the describe blocks enclosing it, outermost first, then
// its own: joined by spaces they are the full name the runner reports.
type TestCase struct {
	Titles []string `json:"titles"`
	Range  Range    `json:"range"`
}

type testCall struct {
	suite bool
	title string
	span  Range
}

// testCaseRule locates describe, it and test calls whose first argument is a
// string. A test has no declaration to name it, so a witness names its title.
// Modified and parameterized forms (it.skip, test.each) are not cases: one
// never runs and the other has no static title.
func testCaseRule(language string) map[string]any {
	return NativeRule("witness-test-case", language, map[string]any{
		"kind": "call_expression",
		"all": []any{
			map[string]any{"has": map[string]any{"field": "function", "kind": "identifier", "regex": `^(describe|it|test)$`, "pattern": "$FN"}},
			map[string]any{"has": map[string]any{"field": "arguments", "has": map[string]any{
				"nthChild": 1, "pattern": "$TITLE",
				"any": []any{map[string]any{"kind": "string"}, map[string]any{"kind": "template_string"}},
			}}},
		},
	})
}

// staticTitle reads a string literal the runner reports unchanged. A template
// with a substitution, or an escape other than a quote or backslash, has no
// title to compare from source.
func staticTitle(literal string) (string, bool) {
	if len(literal) < 2 || !strings.ContainsRune("\"'`", rune(literal[0])) || literal[len(literal)-1] != literal[0] {
		return "", false
	}
	body := literal[1 : len(literal)-1]
	if literal[0] == '`' && strings.Contains(body, "${") {
		return "", false
	}
	var title strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' {
			i++
			if i == len(body) || !strings.ContainsRune("\\\"'`", rune(body[i])) {
				return "", false
			}
		}
		title.WriteByte(body[i])
	}
	return title.String(), true
}

func testCases(calls []testCall) []TestCase {
	sort.Slice(calls, func(i, j int) bool {
		a, b := calls[i].span.Bytes, calls[j].span.Bytes
		if a.Start == b.Start {
			return a.End > b.End
		}
		return a.Start < b.Start
	})
	var cases []TestCase
	for _, call := range calls {
		if call.suite {
			continue
		}
		titles := []string{}
		for _, outer := range calls {
			if outer.suite && Contains(outer.span, call.span) {
				titles = append(titles, outer.title)
			}
		}
		cases = append(cases, TestCase{Titles: append(titles, call.title), Range: call.span})
	}
	return cases
}

// Named reports whether a witness symbol names this case: the full name, or
// any trailing run of its titles. A fence reference holds no whitespace, so
// titles compare without theirs.
func (c TestCase) Named(symbol string) bool {
	want := strings.Join(strings.Fields(symbol), "")
	for from := range c.Titles {
		if strings.Join(strings.Fields(strings.Join(c.Titles[from:], " ")), "") == want {
			return true
		}
	}
	return false
}
