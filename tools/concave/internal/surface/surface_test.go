package surface_test

import (
	"concave/internal/surface"
	"concave/internal/testrepo"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) *testrepo.Fixture {
	t.Helper()
	f := testrepo.New(t)
	modules, err := filepath.Abs("../../testdata/typescript-legacy/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(modules, "typescript")); err != nil {
		t.Fatal("surface integration tests need Concave's pinned TypeScript fixtures; run just concave-setup: ", err)
	}
	if err = os.Symlink(modules, filepath.Join(f.Root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	f.Write(".gitignore", "target/\nnode_modules\n")
	f.Write("entry.ts", "export { identity as renamed, type Shape } from './source.js';\nexport type { runtimeOnly as hidden } from './source.js';\n")
	f.Write("source.ts", "export interface Shape { value: string }\nexport function identity(value: string): string { return value; }\nexport const runtimeOnly = 1;\n")
	f.Commit()
	return f
}

func TestSurfaceDetectsSignatureRuntimeAndOwnerChanges(t *testing.T) {
	f := fixture(t)
	r, _ := f.Open()
	o := surface.Options{Mode: "snapshot", Entries: []string{"entry.ts"}}
	a, err := surface.Capture(r, o)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "captured" || len(a.Entries["entry.ts"].Exports) != 3 || strings.Join(a.Entries["entry.ts"].Runtime, ",") != "renamed" {
		t.Fatalf("%+v", a)
	}
	f.Write("source.ts", "export interface Shape { value: number }\nexport function identity(value: string): number { return value.length; }\nexport const runtimeOnly = 1;\n")
	b, err := surface.Capture(r, o)
	if err != nil {
		t.Fatal(err)
	}
	c, err := surface.Compare(a, b)
	if err != nil || !c.Comparable {
		t.Fatal(c, err)
	}
	kinds := map[string]bool{}
	for _, change := range c.Changes {
		kinds[change.Export+":"+change.Kind] = true
	}
	if !kinds["Shape:signature_changed"] || !kinds["renamed:signature_changed"] {
		t.Fatal(c.Changes)
	}
	if err = surface.Validate(r, a); err == nil {
		t.Fatal("stale snapshot accepted")
	}
	if err = surface.Validate(r, b); err != nil {
		t.Fatal(err)
	}
	f.Write("entry.ts", "export { identity as renamed, type Shape, runtimeOnly } from './source.js';\n")
	d, err := surface.Capture(r, o)
	if err != nil {
		t.Fatal(err)
	}
	c, err = surface.Compare(b, d)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range c.Changes {
		found = found || change.Kind == "runtime_exports_changed"
	}
	if !found {
		t.Fatal(c.Changes)
	}
}

func TestCallersFollowAliasesReexportsNamespacesAndDynamicImports(t *testing.T) {
	f := fixture(t)
	f.Write("direct.ts", "import { renamed as local } from './entry.js'; local('x');\n")
	f.Write("barrel.ts", "export { renamed as publicName } from './entry.js';\n")
	f.Write("indirect.ts", "import * as ns from './barrel.js'; ns.publicName('x');\n")
	f.Write("package.ts", "import { renamed as client } from '@bijectionhq/bijection/browser'; client('x');\n")
	f.Write("dynamic.ts", "const known = import('./entry.js'); const unknown = import(location.hash);\n")
	f.Write("decoy.ts", "const x = `import { renamed } from './entry.js'`;\n")
	r, _ := f.Open()
	p, err := surface.Capture(r, surface.Options{Mode: "callers", Entries: []string{"entry.ts"}, Modules: map[string]string{"@bijectionhq/bijection/browser": "entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]surface.Consumer{}
	for _, c := range p.Consumers {
		paths[c.Path] = c
	}
	for _, name := range []string{"direct.ts", "barrel.ts", "indirect.ts", "package.ts", "dynamic.ts"} {
		if _, ok := paths[name]; !ok {
			t.Fatal("missing caller", name, p.Consumers)
		}
	}
	if _, ok := paths["decoy.ts"]; ok {
		t.Fatal("string mistaken for import")
	}
	if len(paths["direct.ts"].References) != 1 || paths["direct.ts"].Bindings[0].Local != "local" || p.Coverage.UnresolvedDynamic != 1 || p.Coverage.UnscannedFiles != 0 {
		t.Fatal(p.Coverage, paths)
	}
	f.Write("new-consumer.ts", "import './entry.js';\n")
	if err = surface.Validate(r, p); err == nil {
		t.Fatal("new source inventory accepted")
	}
}

func TestCompilerDiagnosticsAndNodePinPreventCompleteClaim(t *testing.T) {
	f := fixture(t)
	f.Write("source.ts", "export const identity = missingName;\nexport interface Shape {}\nexport const runtimeOnly=1;\n")
	r, _ := f.Open()
	p, err := surface.Capture(r, surface.Options{Mode: "snapshot", Entries: []string{"entry.ts"}})
	if err != nil || p.Status != "incomplete" || len(p.Diagnostics) == 0 {
		t.Fatal(p, err)
	}
	f.Write(".nvmrc", "0.0.0\n")
	if _, err = surface.Capture(r, surface.Options{Mode: "snapshot", Entries: []string{"entry.ts"}}); err == nil || !strings.Contains(err.Error(), "differs from .nvmrc") {
		t.Fatal(err)
	}
}
