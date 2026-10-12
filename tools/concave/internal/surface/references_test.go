package surface_test

import (
	"concave/internal/surface"
	"reflect"
	"strings"
	"testing"
)

func TestCallerReferencesResolveLocalBindings(t *testing.T) {
	fixture := fixture(t)
	fixture.Write("entry.ts", "export { identity as renamed, identity as default, type Shape } from './source.js';\n")
	cases := []struct {
		name, source string
		references   []surface.Reference
	}{
		{"direct.ts", "value('x');", []surface.Reference{{Line: 2, Name: "value"}}},
		{"parameter.ts", "function f(value: any) { value('x'); }\nvalue('x');", []surface.Reference{{Line: 3, Name: "value"}}},
		{"closure.ts", "function f() { return value('x'); }", []surface.Reference{{Line: 2, Name: "value"}}},
		{"block.ts", "{ const value = 1; void value; }\nvalue('x');", []surface.Reference{{Line: 3, Name: "value"}}},
		{"hoisted.ts", "function f() { void value; var value; }\nvalue('x');", []surface.Reference{{Line: 3, Name: "value"}}},
		{"destructured.ts", "function f({ value }: any) { return value; }\nvalue('x');", []surface.Reference{{Line: 3, Name: "value"}}},
		{"catch.ts", "try {} catch (value) { void value; }\nvalue('x');", []surface.Reference{{Line: 3, Name: "value"}}},
		{"properties.ts", "const object = { value: 1 }; object.value;\nconst keyed = { [value('x')]: 1 };\nconst shorthand = { value };", []surface.Reference{{Line: 3, Name: "value"}, {Line: 4, Name: "value"}}},
		{"shadowed-shorthand.ts", "function f(value: any) { return { value }; }\nconst object = { value };", []surface.Reference{{Line: 3, Name: "value"}}},
		{"method.ts", "class C { value() {} } const object = { value() {} };", []surface.Reference{}},
		{"loop.ts", "for (let value of []) { void value; } value('x');", []surface.Reference{{Line: 2, Name: "value"}}},
		{"named-expression.ts", "const f = function value() { return value; }; value('x');", []surface.Reference{{Line: 2, Name: "value"}}},
		{"types.ts", "type Local<Shape> = Shape;\ntype Actual = Shape;", []surface.Reference{{Line: 3, Name: "Shape"}}},
		{"type-query.ts", "type Signature = typeof value;", []surface.Reference{{Line: 2, Name: "value"}}},
		{"order.ts", "const object = { shape: null as Shape, value, again: value };", []surface.Reference{{Line: 2, Name: "Shape"}, {Line: 2, Name: "value"}, {Line: 2, Name: "value"}}},
		{"unused.ts", "const object = { value: 1 };", []surface.Reference{}},
		{"javascript.js", "function f(value) { return value; }\nvalue('x');", []surface.Reference{{Line: 3, Name: "value"}}},
		{"shadow-limit.ts", "function f(value: any) {\n" + strings.Repeat("value('x');\n", 70) + "}\nvalue('x');", []surface.Reference{{Line: 74, Name: "value"}}},
	}
	for _, test := range cases {
		imports := "import { renamed as value, type Shape } from './entry.js';\n"
		if strings.HasSuffix(test.name, ".js") {
			imports = "import { renamed as value } from './entry.js';\n"
		}
		fixture.Write(test.name, imports+test.source+"\n")
	}
	fixture.Write("multiple.ts", "import { renamed as first } from './entry.js';\nimport { renamed as second } from './entry.js';\nsecond('x'); first('x');\n")
	fixture.Write("namespace.ts", "import * as sdk from './entry.js';\nsdk.renamed('x');\nfunction f(sdk: any) { sdk.renamed('x'); }\n")
	fixture.Write("jsx.tsx", "import Widget from './entry.js';\nconst element = <Widget Widget={1}></Widget>;\nfunction f(Widget: any) { return <Widget />; }\n")
	fixture.Write("reexport.ts", "export { renamed as value } from './entry.js';\nconst value = 1; void value;\n")
	fixture.Write("limit.ts", "import { renamed as value } from './entry.js';\n"+strings.Repeat("value('x');\n", 70))
	repo, _ := fixture.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	locations := func(references []surface.Reference) []surface.Reference {
		result := make([]surface.Reference, len(references))
		copy(result, references)
		for index := range result {
			if result[index].Column < 1 {
				t.Fatal("missing reference column", result[index])
			}
			result[index].Column = 0
		}
		return result
	}
	consumers := map[string]surface.Consumer{}
	for _, consumer := range report.Consumers {
		consumers[consumer.Path] = consumer
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			consumer, found := consumers[test.name]
			if !found || !reflect.DeepEqual(locations(consumer.References), test.references) {
				t.Fatalf("found=%t references=%+v; want %+v", found, consumer.References, test.references)
			}
		})
	}
	for _, test := range []struct {
		name       string
		references []surface.Reference
	}{
		{"namespace.ts", []surface.Reference{{Line: 2, Name: "sdk"}}},
		{"jsx.tsx", []surface.Reference{{Line: 2, Name: "Widget"}, {Line: 2, Name: "Widget"}}},
		{"reexport.ts", []surface.Reference{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			consumer, found := consumers[test.name]
			if !found || !reflect.DeepEqual(locations(consumer.References), test.references) {
				t.Fatalf("found=%t references=%+v; want %+v", found, consumer.References, test.references)
			}
		})
	}
	t.Run("multiple-imports", func(t *testing.T) {
		var edges []surface.Consumer
		for _, consumer := range report.Consumers {
			if consumer.Path == "multiple.ts" {
				edges = append(edges, consumer)
			}
		}
		if len(edges) != 2 {
			t.Fatal(edges)
		}
		for index, name := range []string{"first", "second"} {
			if !reflect.DeepEqual(locations(edges[index].References), []surface.Reference{{Line: 3, Name: name}}) {
				t.Fatal(edges)
			}
		}
	})
	t.Run("reference-bound", func(t *testing.T) {
		references := consumers["limit.ts"].References
		if len(references) != 64 {
			t.Fatal("reference bound", len(references))
		}
		for i, reference := range references {
			if reference.Line != i+2 || reference.Name != "value" {
				t.Fatal(references)
			}
		}
	})
}
