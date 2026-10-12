package syntax

import (
	"fmt"
	"sort"
	"strings"
)

type Declaration struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Range    Range  `json:"range"`
}
type Document struct {
	Declarations   []Declaration `json:"declarations"`
	MacroWitnesses []Declaration `json:"macro_witnesses,omitempty"`
	TestCases      []TestCase    `json:"test_cases,omitempty"`
	Comments       []Capture     `json:"-"`
	Attributes     []Range       `json:"-"`
}

func (e *Engine) Parse(path string, source []byte) (*Document, error) {
	language := Language(path)
	if language == "" {
		return nil, fmt.Errorf("declaration discovery does not support %s", path)
	}
	var kinds, comments, attrs []string
	switch language {
	case "Rust":
		kinds = []string{"function_item", "function_signature_item", "struct_item", "enum_item", "trait_item", "mod_item", "type_item", "const_item", "static_item", "field_declaration", "enum_variant", "macro_definition"}
		comments = []string{"line_comment", "block_comment"}
		attrs = []string{"attribute_item"}
	case "Go":
		kinds = []string{"function_declaration", "method_declaration", "type_spec"}
		comments = []string{"comment"}
	default:
		kinds = []string{"function_declaration", "generator_function_declaration", "class_declaration", "method_definition", "variable_declarator"}
		if language != "JavaScript" {
			kinds = append(kinds, "interface_declaration", "type_alias_declaration", "enum_declaration", "method_signature", "abstract_method_signature", "public_field_definition")
		}
		comments = []string{"comment"}
		attrs = []string{"decorator"}
	}
	rules := []map[string]any{NativeRule("parse-error", language, map[string]any{"kind": "ERROR"})}
	if language != "Rust" && language != "Go" {
		rules = append(rules, testCaseRule(language))
	}
	for _, kind := range kinds {
		rules = append(rules, NativeRule("decl-"+kind, language, map[string]any{"kind": kind, "has": map[string]any{"field": "name", "pattern": "$NAME"}}))
	}
	if language == "Rust" {
		rules = append(rules, NativeRule("decl-impl_item", language, map[string]any{"kind": "impl_item", "has": map[string]any{"field": "type", "pattern": "$NAME"}}))
		rules = append(rules, proptestWitnessRule())
	}
	for _, kind := range comments {
		rules = append(rules, NativeRule("comment", language, map[string]any{"kind": kind}))
	}
	for _, kind := range attrs {
		rules = append(rules, NativeRule("attribute", language, map[string]any{"kind": kind}))
	}
	matches, err := e.Scan(source, rules)
	if err != nil {
		return nil, err
	}
	doc := &Document{Declarations: []Declaration{}}
	var calls []testCall
	for _, m := range matches {
		switch m.Rule {
		case "witness-test-case":
			if title, ok := staticTitle(m.Meta.Single["TITLE"].Text); ok {
				calls = append(calls, testCall{m.Meta.Single["FN"].Text == "describe", title, m.Range})
			}
		case "parse-error":
			return nil, &SourceError{Path: path, Line: m.Range.Start.Line + 1, Message: fmt.Sprintf("syntax error at column %d; obligation scope is unavailable", m.Range.Start.Column+1)}
		case "comment":
			doc.Comments = append(doc.Comments, Capture{m.Text, m.Range})
		case "attribute":
			doc.Attributes = append(doc.Attributes, m.Range)
		case "witness-proptest":
			start, name, body := m.Meta.Single["FN"], m.Meta.Single["NAME"], m.Meta.Single["BODY"]
			if start.Text != "fn" || name.Text == "" || !strings.HasPrefix(body.Text, "{") || start.Range.Bytes.End > name.Range.Bytes.Start || name.Range.Bytes.End > body.Range.Bytes.Start {
				return nil, &Unavailable{fmt.Errorf("engine returned invalid proptest witness captures")}
			}
			span := Range{Bytes: Offset{start.Range.Bytes.Start, body.Range.Bytes.End}, Start: start.Range.Start, End: body.Range.End}
			doc.MacroWitnesses = append(doc.MacroWitnesses, Declaration{Kind: "proptest_input", Name: name.Text, Range: span})
		default:
			name := m.Meta.Single["NAME"].Text
			if name == "" {
				return nil, fmt.Errorf("declaration has no syntax name")
			}
			doc.Declarations = append(doc.Declarations, Declaration{Kind: strings.TrimPrefix(m.Rule, "decl-"), Name: name, Range: m.Range})
		}
	}
	sort.Slice(doc.Declarations, func(i, j int) bool {
		a, b := doc.Declarations[i].Range.Bytes, doc.Declarations[j].Range.Bytes
		if a.Start == b.Start {
			return a.End > b.End
		}
		return a.Start < b.Start
	})
	for i := range doc.Declarations {
		d := &doc.Declarations[i]
		d.Identity = d.Kind + ":" + d.Name
		// Same-named Rust impl methods may belong to different traits; Go
		// methods may belong to different receivers. Preserve that syntactic
		// ownership in attachment identity, without claiming type resolution.
		if d.Kind == "impl_item" || d.Kind == "method_declaration" {
			header := string(source[d.Range.Bytes.Start:d.Range.Bytes.End])
			if at := strings.IndexByte(header, 123); at >= 0 {
				header = header[:at]
			}
			d.Identity = d.Kind + ":" + strings.Join(strings.Fields(header), " ")
		}
		for j := i - 1; j >= 0; j-- {
			p := doc.Declarations[j]
			if Contains(p.Range, d.Range) {
				d.Identity = p.Identity + "/" + d.Identity
				break
			}
		}
	}
	sort.Slice(doc.Comments, func(i, j int) bool { return doc.Comments[i].Range.Bytes.Start < doc.Comments[j].Range.Bytes.Start })
	doc.TestCases = testCases(calls)
	return doc, nil
}
func Contains(outer, inner Range) bool {
	return outer.Bytes.Start <= inner.Bytes.Start && outer.Bytes.End >= inner.Bytes.End && outer.Bytes != inner.Bytes
}

// Attachable permits only whitespace, parsed attributes, and syntax modifiers
// before an immediate declaration. It never jumps over executable statements.
func (d *Document) Attachable(source []byte, start, end int) bool {
	if start > end {
		return false
	}
	b := append([]byte{}, source[start:end]...)
	for _, a := range d.Attributes {
		if a.Bytes.Start >= start && a.Bytes.End <= end {
			for i := a.Bytes.Start; i < a.Bytes.End; i++ {
				b[i-start] = 32
			}
		}
	}
	s := strings.TrimSpace(string(b))
	for _, prefix := range []string{"export default ", "export ", "declare ", "async ", "const ", "let ", "var ", "type "} {
		s = strings.TrimPrefix(s+" ", prefix)
		s = strings.TrimSpace(s)
	}
	return s == ""
}
