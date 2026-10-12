package syntax

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type NamedDeclaration struct {
	Name     string
	Symbol   string
	Parent   string
	Role     string
	Value    string
	Language string
	Surface  string
	Range    Range
}

var identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
var directValue = regexp.MustCompile(`:\s*(?:Option<)?(bool|boolean|Instant|Timestamp|UnixNanoseconds|EpochMillis)\b`)
var goValue = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s+(bool|Instant|Timestamp)\b`)
var jsonTag = regexp.MustCompile(`json:"([^",]+)(?:,[^"]*)?"`)
var serdeName = regexp.MustCompile(`rename\s*=\s*"([^"]+)"`)

// Names uses the same validated syntax transport as contract discovery.
func (e *Engine) Names(path string, source []byte) ([]NamedDeclaration, error) {
	language := Language(path)
	if language == "" {
		return nil, fmt.Errorf("unsupported naming syntax %s", path)
	}
	document, err := e.Parse(path, source)
	if err != nil {
		return nil, err
	}
	kinds := []string{}
	switch language {
	case "Go":
		kinds = []string{"field_declaration", "var_spec", "const_spec"}
	case "Rust": // Fields and named declarations are already in Parse.
	default:
		kinds = []string{"property_signature", "public_field_definition"}
	}
	rules := []map[string]any{}
	for _, kind := range kinds {
		rules = append(rules, NativeRule("name-"+kind, language, map[string]any{"kind": kind, "has": map[string]any{"field": "name", "pattern": "$NAME"}}))
	}
	if language == "Go" {
		rules = append(rules, NativeRule("name-go-local", language, map[string]any{"kind": "identifier", "pattern": "$NAME", "inside": map[string]any{"kind": "expression_list", "inside": map[string]any{"kind": "short_var_declaration", "field": "left"}}}))
	}
	matches := []Match{}
	if len(rules) > 0 {
		matches, err = e.Scan(source, rules)
		if err != nil {
			return nil, err
		}
	}
	declarations := append([]Declaration{}, document.Declarations...)
	for _, m := range matches {
		declarations = append(declarations, Declaration{Kind: strings.TrimPrefix(m.Rule, "name-"), Name: m.Meta.Single["NAME"].Text, Range: m.Range})
	}
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].Range.Bytes.Start < declarations[j].Range.Bytes.Start })
	result := []NamedDeclaration{}
	for _, d := range declarations {
		role := ""
		switch d.Kind {
		case "struct_item", "enum_item", "trait_item", "type_item", "type_spec", "class_declaration", "interface_declaration", "type_alias_declaration", "enum_declaration":
			role = "type"
		case "function_item", "function_signature_item", "function_declaration", "generator_function_declaration", "method_declaration", "method_definition", "method_signature", "abstract_method_signature":
			role = "function"
		case "field_declaration", "property_signature", "public_field_definition":
			role = "field"
		case "variable_declarator", "go-local", "var_spec", "const_spec":
			role = "variable"
		case "const_item", "static_item":
			role = "constant"
		}
		name := strings.Trim(d.Name, "\"'")
		if role == "" || !identifier.MatchString(name) {
			continue
		}
		parent := ""
		start := -1
		for _, outer := range document.Declarations {
			if Contains(outer.Range, d.Range) && outer.Range.Bytes.Start >= start {
				switch outer.Kind {
				case "struct_item", "enum_item", "trait_item", "type_item", "type_spec", "class_declaration", "interface_declaration", "type_alias_declaration", "impl_item":
					parent = outer.Name
					start = outer.Range.Bytes.Start
				}
			}
		}
		symbol := name
		if parent != "" {
			symbol = parent + "/" + name
		}
		text := string(source[d.Range.Bytes.Start:d.Range.Bytes.End])
		value := ""
		if role == "field" {
			match := directValue.FindStringSubmatch(text)
			if language == "Go" {
				match = goValue.FindStringSubmatch(text)
			}
			if len(match) > 1 {
				if match[1] == "bool" || match[1] == "boolean" {
					value = "boolean"
				} else {
					value = "instant"
				}
			}
		}
		f := NamedDeclaration{Name: name, Symbol: symbol, Parent: parent, Role: role, Value: value, Language: language, Range: d.Range}
		result = append(result, f)
		if role == "field" {
			match := jsonTag.FindStringSubmatch(text)
			if language == "Rust" {
				for _, a := range document.Attributes {
					if a.Bytes.End <= d.Range.Bytes.Start && strings.TrimSpace(string(source[a.Bytes.End:d.Range.Bytes.Start])) == "" {
						if found := serdeName.FindStringSubmatch(string(source[a.Bytes.Start:a.Bytes.End])); len(found) > 1 {
							match = found
						}
					}
				}
			}
			if len(match) > 1 && match[1] != "-" {
				wire := f
				wire.Name = match[1]
				wire.Symbol += "/wire"
				wire.Surface = "wire"
				result = append(result, wire)
			}
		}
	}
	return result, nil
}
