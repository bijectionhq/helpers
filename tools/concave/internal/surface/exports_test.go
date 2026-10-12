package surface_test

import (
	"concave/internal/surface"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TypeScript is an independent oracle for export ownership and diagnostics.
// In particular, its checker exposes a first symbol even for an ambiguous star:
// diagnostic 2308 is what makes that apparent export unusable.
func TestCallerExportResolutionAgreesWithTypeScript(t *testing.T) {
	fixture := fixture(t)
	fixture.Write("entry.ts", "export default function primary() {}\nexport const named = 1;\n")
	fixture.Write("other.ts", "export const named = 2;\n")
	fixture.Write("left.ts", "export * from './entry.js';\n")
	fixture.Write("right.ts", "export { named } from './entry.js';\n")
	fixture.Write("cycle.ts", "export * from './cyclic.js';\n")
	cases := []struct {
		name, code, binding string
		selected, ambiguous bool
	}{
		{"star", "export * from './entry.js';", "named", true, false},
		{"default-star", "export * from './entry.js';", "default", false, false},
		{"default-explicit", "export { default } from './entry.js';", "default", true, false},
		{"override", "export * from './entry.js'; export const named = 2;", "named", false, false},
		{"override-before", "export const named = 2; export * from './entry.js';", "named", false, false},
		{"destructured", "export * from './entry.js'; export const { named } = { named: 2 };", "named", false, false},
		{"local", "const own = 2; export { own as named }; export * from './entry.js';", "named", false, false},
		{"imported", "import { named as value } from './entry.js'; export { value as renamed };", "renamed", true, false},
		{"explicit", "export * from './other.js'; export { named } from './entry.js';", "named", true, false},
		{"diamond", "export * from './left.js'; export * from './right.js';", "named", true, false},
		{"ambiguous", "export * from './entry.js'; export * from './other.js';", "named", false, true},
		{"ambiguous-reversed", "export * from './other.js'; export * from './entry.js';", "named", false, true},
		{"cyclic", "export * from './cycle.js'; export * from './entry.js';", "named", true, false},
		{"namespace", "export * as ns from './entry.js';", "ns", true, false},
		{"namespace-ambiguous", "export * as ns from './ambiguous.js';", "ns", false, false},
		{"named-ambiguous", "export { named } from './ambiguous.js';", "named", false, false},
	}
	for _, test := range cases {
		fixture.Write(test.name+".ts", test.code+"\n")
		fixture.Write("use-"+test.name+".ts", "import { "+test.binding+" as used } from './"+test.name+".js'; void used;\n")
	}
	fixture.Write("side-effect.ts", "import './entry.js';\n")
	fixture.Write("empty-import.ts", "import {} from './entry.js';\n")
	repo, _ := fixture.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != surface.Captured || report.Coverage.ReexportLimit {
		t.Fatal(report.Status, report.Coverage)
	}
	consumers := map[string]surface.Consumer{}
	for _, consumer := range report.Consumers {
		consumers[consumer.Path] = consumer
	}
	for _, test := range cases {
		_, found := consumers["use-"+test.name+".ts"]
		if found != test.selected {
			t.Errorf("%s: consumer=%t, want %t", test.name, found, test.selected)
		}
	}
	for _, path := range []string{"side-effect.ts", "empty-import.ts"} {
		consumer, found := consumers[path]
		if !found || len(consumer.Bindings) != 0 || len(consumer.References) != 0 {
			t.Errorf("missing side-effect dependency: %s: %+v", path, consumer)
		}
	}
	script := `
const ts = require('typescript'), fs = require('fs');
const program = ts.createProgram(fs.readdirSync('.').filter(p => p.endsWith('.ts')), {target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler, skipLibCheck: true});
const checker = program.getTypeChecker(), result = {};
for (const source of program.getSourceFiles()) {
 if (!source.fileName.endsWith('.ts') || source.fileName.includes('node_modules')) continue;
 const module = checker.getSymbolAtLocation(source);
 const owners = {};
 for (let symbol of module ? checker.getExportsOfModule(module) : []) {
  const name = symbol.name;
  if (symbol.flags & ts.SymbolFlags.Alias) symbol = checker.getAliasedSymbol(symbol);
  owners[name] = (symbol.declarations || []).map(d => require('path').basename(d.getSourceFile().fileName));
 }
 result[require('path').basename(source.fileName)] = {owners, ambiguous: program.getSemanticDiagnostics(source).some(d => d.code === 2308)};
}
process.stdout.write(JSON.stringify(result));`
	command := exec.Command("node", "-e", script)
	command.Dir = fixture.Root
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("TypeScript oracle: %v: %s", err, data)
	}
	var oracle map[string]struct {
		Owners    map[string][]string `json:"owners"`
		Ambiguous bool                `json:"ambiguous"`
	}
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		actual, present := oracle[test.name+".ts"]
		if !present || actual.Ambiguous != test.ambiguous {
			t.Errorf("%s oracle: %+v", test.name, actual)
		}
		// Ambiguous forwarding deliberately rejects the checker's first-symbol choice.
		if strings.Contains(test.name, "ambiguous") {
			continue
		}
		selected := false
		for _, owner := range actual.Owners[test.binding] {
			selected = selected || owner == "entry.ts"
		}
		if selected != test.selected {
			t.Errorf("%s ownership disagrees with TypeScript: %+v", test.name, actual)
		}
	}
	before, err := report.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Timings) == 0 {
		t.Fatal("missing phase observations")
	}
	report.Timings["adapter"] += time.Second
	after, err := report.Digest()
	if err != nil || before != after {
		t.Fatal("timing changed artifact identity", err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), `"timings"`) {
		t.Fatal("timings leaked into artifact")
	}
}
