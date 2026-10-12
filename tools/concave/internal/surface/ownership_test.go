package surface_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/surface"
)

func TestCallerCompilerBelongsToDeclaringPackage(t *testing.T) {
	f := fixture(t)
	modern, err := filepath.Abs("../../testdata/typescript-modern/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	f.Write("modern/package.json", `{"devDependencies":{"typescript":"5.9.3"}}`)
	if err = os.Symlink(modern, filepath.Join(f.Root, "modern/node_modules")); err != nil {
		t.Fatal(err)
	}
	f.Write("modern/use.ts", "import { renamed } from '../entry.js';\nusing resource = { [Symbol.dispose]() {} };\nrenamed('x');\n")
	repo, _ := f.Open()
	options := surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}}
	report, err := surface.Capture(repo, options)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Coverage.Complete || report.Status != surface.Captured {
		t.Fatal(report.Coverage, report.Diagnostics)
	}
	found := false
	for _, scope := range report.Compilers {
		if scope.Owner == "modern/package.json" {
			found = true
			if scope.TypeScript != "5.9.3" || scope.Files != 1 || scope.Basis != "package" {
				t.Fatal(scope)
			}
		}
	}
	if !found {
		t.Fatal(report.Compilers)
	}
	for _, edge := range report.Consumers {
		if edge.Path == "modern/use.ts" && (len(edge.References) != 1 || edge.References[0].Line != 3 || edge.Compiler != "modern/node_modules/typescript/lib/typescript.js") {
			t.Fatal(edge)
		}
	}
	// Tool bytes and previously absent ownership manifests participate in freshness.
	if _, ok := report.Inputs["modern/package.json"]; !ok {
		t.Fatal("owner manifest is not bound")
	}
	f.Write("modern/package.json", `{"devDependencies":{"typescript":"5.0.4"}}`)
	if err = surface.Validate(repo, report); err == nil {
		t.Fatal("changed owner accepted")
	}
}

func TestSurfaceIdentityIgnoresCompilerIDsAndLiteralUnionOrder(t *testing.T) {
	f := fixture(t)
	f.Write("keys.ts", "export const first: unique symbol = Symbol();\nexport const second: unique symbol = Symbol();\n")
	text := "import { first, second } from './keys.js';\nexport interface Shape { [first]: string; [second]: number; status: 'a' | 'b'; }\nexport interface Call { (x: 'a' | 'b'): 'x' | 'y'; (x:number):number; }\n"
	f.Write("entry.ts", text)
	repo, _ := f.Open()
	options := surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}}
	before, err := surface.Capture(repo, options)
	if err != nil {
		t.Fatal(err)
	}
	f.Write("keys.ts", "const irrelevant: unique symbol = Symbol();\nexport const first: unique symbol = Symbol();\nexport const second: unique symbol = Symbol();\n")
	f.Write("entry.ts", strings.ReplaceAll(strings.ReplaceAll(text, "'a' | 'b'", "'b' | 'a'"), "'x' | 'y'", "'y' | 'x'"))
	after, err := surface.Capture(repo, options)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := surface.Compare(before, after)
	if err != nil || !comparison.Comparable || len(comparison.Changes) != 0 {
		t.Fatal(comparison, err)
	}
	raw, _ := json.Marshal(after.Entries)
	if strings.Contains(string(raw), "__@") || !strings.Contains(string(raw), "keys.ts#first") || !strings.Contains(string(raw), "keys.ts#second") {
		t.Fatal(string(raw))
	}
	// Overload order and distinct unique-symbol origins remain observable.
	f.Write("entry.ts", strings.Replace(text, "(x: 'a' | 'b'): 'x' | 'y'; (x:number):number;", "(x:number):number; (x: 'a' | 'b'): 'x' | 'y';", 1))
	reordered, err := surface.Capture(repo, options)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := surface.Compare(before, reordered)
	if err != nil || len(changed.Changes) == 0 {
		t.Fatal("overload order was erased", changed, err)
	}
	f.Write("entry.ts", strings.Replace(text, "[first]: string; [second]: number;", "[first]: number; [second]: string;", 1))
	swapped, err := surface.Capture(repo, options)
	if err != nil {
		t.Fatal(err)
	}
	changed, err = surface.Compare(before, swapped)
	if err != nil || len(changed.Changes) == 0 {
		t.Fatal("unique keys were conflated", changed, err)
	}
}

func TestMissingCallerCompilerIsLocatedAndInstallationInvalidatesArtifact(t *testing.T) {
	f := fixture(t)
	f.Write("selected/entry.ts", "export const selected=1;\n")
	if err := os.Rename(filepath.Join(f.Root, "node_modules"), filepath.Join(f.Root, "selected/node_modules")); err != nil {
		t.Fatal(err)
	}
	f.Write("missing/package.json", `{"devDependencies":{"typescript":"5.9.3"}}`)
	f.Write("missing/use.ts", "import {selected} from '../selected/entry.js'; void selected;\n")
	repo, _ := f.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"selected/entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != surface.Incomplete || report.Coverage.UnscannedFiles != 1 || len(report.Coverage.Issues) != 1 || report.Coverage.Issues[0].Kind != "compiler_unavailable" || report.Coverage.Issues[0].Path != "missing/use.ts" {
		t.Fatal(report.Coverage)
	}
	if err = surface.Validate(repo, report); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(f.Root, "selected/node_modules"), filepath.Join(f.Root, "missing/node_modules")); err != nil {
		t.Fatal(err)
	}
	if err = surface.Validate(repo, report); err == nil {
		t.Fatal("new compiler installation did not invalidate evidence")
	}
}

func TestCompilerSymlinkRetargetIsNotHiddenBySurvivingOldBinary(t *testing.T) {
	f := fixture(t)
	real, err := filepath.Abs("../../testdata/typescript-legacy/node_modules/typescript/lib/typescript.js")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(real)
	for _, version := range []string{"a", "b"} {
		f.Write("versions/"+version+"/package.json", `{"main":"index.js"}`)
		f.Write("versions/"+version+"/index.js", "module.exports = require("+string(quoted)+"); // "+version+"\n")
	}
	f.Write("owner/package.json", `{"devDependencies":{"typescript":"5.0.4"}}`)
	f.Write("owner/use.ts", "import {renamed} from '../entry.js'; renamed('x');\n")
	link := filepath.Join(f.Root, "owner/node_modules/typescript")
	if err = os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(f.Root, "versions/a"), link); err != nil {
		t.Fatal(err)
	}
	repo, _ := f.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = surface.Validate(repo, report); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(f.Root, "versions/b"), link); err != nil {
		t.Fatal(err)
	}
	if err = surface.Validate(repo, report); err == nil {
		t.Fatal("retargeted compiler accepted while old binary survives")
	}
}
