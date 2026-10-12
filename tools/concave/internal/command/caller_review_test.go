package command_test

import (
	"concave/internal/surface"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCallerEvidenceResolvesObligationsWitnessesAndCLIComparisons(t *testing.T) {
	f := contractFixture(t)
	modules, err := filepath.Abs("../../testdata/typescript-legacy/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(modules, filepath.Join(f.Root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	f.Write(".gitignore", "target/\nnode_modules\n")
	entry := "npm-packages/convex/index.ts"
	f.Write(entry, "// concave:invariant callee\n// Preserve the caller identity.\nexport function request() { return 1; }\n")
	f.Write("npm-packages/caller.ts", "export function caller() {}\n")
	f.Config.Upstream = f.Commit()
	f.Config.Families[1].Contracts = []string{"contracts/auth.rs#authentication"}
	f.Save()
	f.Commit()
	f.Write("tests/witness.rs", "fn verify() { assert!(true); }\n")
	f.Write("npm-packages/caller.ts", "import { request as fetch } from 'sdk';\n// concave:invariant caller-safety\n// Keep caller identity intact.\nexport function caller() {\n// concave:begin caller-adapter\n// why: Adapt the request boundary.\n// preserves: #caller-safety\n// witness: tests/witness.rs::verify\nreturn fetch();\n// concave:end caller-adapter\n}\n")
	artifact := filepath.Join(f.Root, "target/callers.json")
	n, out, stderr := run(f, "surface", "callers", entry, "--module", "sdk="+entry, "--json")
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	f.Write("target/callers.json", out)
	var capture surface.Report
	if err = json.Unmarshal([]byte(out), &capture); err != nil {
		t.Fatal(err)
	}
	n, text, stderr := run(f, "surface", "callers", entry, "--module", "sdk="+entry, "--top", "1")
	if n != 0 || !strings.Contains(text, "use npm-packages/caller.ts:9:") || !strings.Contains(text, "coverage complete") {
		t.Fatal(n, text, stderr)
	}
	for _, args := range [][]string{{"--module", "sdk"}, {"--module", "sdk=" + entry, "--module", "sdk=" + entry}, {"--top", "-1"}} {
		if n, _, _ = run(f, append([]string{"surface", "callers", entry}, args...)...); n != 2 {
			t.Fatal("invalid arguments accepted", args, n)
		}
	}
	packet := contractReview(t, f, entry, "--callers", artifact)
	if packet.CallerContext == nil || packet.CallerCoverage.ResolvedFiles != 1 || packet.CallerCoverage.OmittedLocations != 0 {
		t.Fatal(packet.CallerCoverage)
	}
	found := false
	for _, location := range packet.CallerLocations {
		if location.Path == "npm-packages/caller.ts" && location.Line == 9 {
			found = true
			refs := strings.Join(location.Obligations, ",")
			if !strings.Contains(refs, "caller.ts#caller-safety") || !strings.Contains(refs, "contracts/auth.rs#authentication") || location.Origins[0].Kind != "import_binding_use" {
				t.Fatal(location)
			}
		}
	}
	if !found {
		t.Fatal("aliased call lost its enclosing invariant", packet.CallerLocations)
	}
	witnesses := packet.CallerContext.Files[0].Witnesses
	if len(witnesses) != 1 || witnesses[0].Resolution != "symbol_found" || witnesses[0].Executed {
		t.Fatal(witnesses)
	}
	seen := map[string]bool{}
	witnessSource := false
	for _, source := range packet.Sources {
		key := source.Version + ":" + source.Path
		if seen[key] {
			t.Fatal("duplicate source", key)
		}
		seen[key] = true
		if source.Path == "tests/witness.rs" && source.Version == "consumer" {
			witnessSource = true
		}
	}
	if !witnessSource {
		t.Fatal("caller witness source missing")
	}
	n, text, stderr = run(f, "inspect", "queue", "--contracts", entry, "--callers", artifact)
	if n != 0 || !strings.Contains(text, "Caller obligations at npm-packages/caller.ts:9:") || !strings.Contains(text, "Keep caller identity intact.") || !strings.Contains(text, "Caller witness tests/witness.rs::verify: symbol_found; executed=false") {
		t.Fatal(n, text, stderr)
	}
	scoped := contractReview(t, f, entry, "--callers", artifact, "--caller-path", "npm-packages/caller.ts")
	if scoped.CallerCoverage.ExcludedFiles != 0 || len(scoped.CallerCoverage.Scope) != 1 || scoped.SnapshotSHA256 == packet.SnapshotSHA256 {
		t.Fatal("scope missing from evidence", scoped.CallerCoverage)
	}
	same := contractReview(t, f, entry, "--callers", artifact, "--caller-path", "npm-packages/caller.ts", "--caller-path", "npm-packages/caller.ts")
	if same.SnapshotSHA256 != scoped.SnapshotSHA256 {
		t.Fatal("duplicate selector changes identity")
	}
	for _, args := range [][]string{
		{"inspect", "queue", "--contracts", entry, "--caller-path", "npm-packages/"},
		{"inspect", "queue", "--contracts", entry, "--callers", artifact, "--caller-path", "absent/"},
		{"inspect", "queue", "--contracts", entry, "--callers", artifact, "--caller-path", "../"},
	} {
		if code, _, _ := run(f, args...); code != 2 {
			t.Fatal(args, code)
		}
	}
	if strings.Index(text, "Caller obligations") > strings.Index(text, "Scanned ") {
		t.Fatal("raw coverage precedes obligations", text)
	}
	f.Write("tests/witness.rs", "fn verify() { assert!(false); }\n")
	changed := contractReview(t, f, entry, "--callers", artifact)
	if packet.SnapshotSHA256 == changed.SnapshotSHA256 {
		t.Fatal("changed witness absent from review identity")
	}
	f.Write("notes.md", "unrelated commit\n")
	f.Commit()
	current := contractReview(t, f, entry, "--callers", artifact)
	if current.ImportConsumers.Head != capture.Head || current.Candidate.Head == capture.Head {
		t.Fatal("capture provenance lost")
	}
	n, out, stderr = run(f, "surface", "callers", entry, "--module", "sdk="+entry, "--json")
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	f.Write("target/after.json", out)
	n, out, stderr = run(f, "surface", "compare", artifact, filepath.Join(f.Root, "target/after.json"), "--json")
	if n != 0 {
		t.Fatal(n, out, stderr)
	}
	var comparison surface.Comparison
	if err = json.Unmarshal([]byte(out), &comparison); err != nil {
		t.Fatal(err)
	}
	if !comparison.Comparable || len(comparison.Callers) != 0 || comparison.Kind != "concave-callers-comparison" {
		t.Fatal(comparison)
	}
	f.Write("npm-packages/caller.ts", "export const changed=1;\n")
	if n, _, _ = run(f, "inspect", "queue", "--contracts", entry, "--callers", artifact); n != 3 {
		t.Fatal("changed caller accepted", n)
	}
}
