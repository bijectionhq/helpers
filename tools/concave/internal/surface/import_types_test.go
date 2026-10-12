package surface_test

import (
	"concave/internal/surface"
	"testing"
)

func TestCallersIncludeImportTypesWithoutInventingRuntimeUses(t *testing.T) {
	fixture := fixture(t)
	fixture.Write("barrel.ts", "export type { Shape as PublicShape } from './entry.js';\n")
	fixture.Write("namespace.ts", "export * as sdk from './entry.js';\n")
	fixture.Write("consumer.ts", `type Named = import("./entry.js").Shape;
type Module = typeof import("./entry.js");
type Function = typeof import("./entry.js").renamed;
type Alias = import("./barrel.js").PublicShape;
type Nested = import("./namespace.js").sdk.Shape;
type Package = import("@bijectionhq/bijection").Shape;
const runtime = import("./entry.js");
type Missing = import("./entry.js").NotExported;
const text = 'type Fake = import("./entry.js").Shape';
`)
	repo, _ := fixture.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}, Modules: map[string]string{"@bijectionhq/bijection": "entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	byLine := map[int]surface.Consumer{}
	for _, consumer := range report.Consumers {
		if consumer.Path == "consumer.ts" {
			if _, duplicate := byLine[consumer.Line]; duplicate {
				t.Fatal("duplicate edge", consumer)
			}
			byLine[consumer.Line] = consumer
		}
	}
	if len(byLine) != 7 {
		t.Fatalf("got %d edges, want six type imports and one runtime import: %+v", len(byLine), byLine)
	}
	for index, imported := range []string{"Shape", "*", "renamed", "PublicShape", "sdk", "Shape"} {
		consumer := byLine[index+1]
		if consumer.Kind != "type_import" || len(consumer.Bindings) != 1 || consumer.Bindings[0].Imported != imported || consumer.Bindings[0].Local != "" || len(consumer.References) != 0 {
			t.Errorf("line %d: %+v", index+1, consumer)
		}
	}
	if byLine[7].Kind != "dynamic" || report.Coverage.UnresolvedDynamic != 0 {
		t.Fatal(byLine, report.Coverage)
	}
}
