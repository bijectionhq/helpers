package coverage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Go witness that calls the fenced function executes it; one that never
// does is not_executed. Neither claim depends on names in the test body.
func TestGoWitnessCoverageDecidesExecution(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) []byte {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return []byte(text)
	}
	write("mod/go.mod", "module example.com/m\n\ngo 1.22\n")
	source := write("mod/pkg/a.go", "package pkg\n\nfunc Fenced(x int) int {\n\tif x > 0 {\n\t\treturn x * 2\n\t}\n\treturn 0\n}\n\nfunc Other() int { return 1 }\n")
	tests := write("mod/pkg/a_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestReaches(t *testing.T) { helper(t) }\nfunc helper(t *testing.T) { if Fenced(3) != 6 { t.Fatal() } }\nfunc TestMisses(t *testing.T) { if Other() != 1 { t.Fatal() } }\n")
	run := func(symbol string) *Receipt {
		receipt, err := Run(context.Background(), Options{Root: root, Scratch: t.TempDir(), Timeout: 2 * time.Minute}, Request{Fence: "fenced", Path: "mod/pkg/a.go", Source: source, CodeStart: 3, CodeEnd: 7, Witness: "mod/pkg/a_test.go::" + symbol, WitnessPath: "mod/pkg/a_test.go", Symbol: symbol, WitnessSource: tests})
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	if r := run("TestReaches"); r.Outcome != Executed || len(r.Lines) == 0 {
		t.Fatalf("%+v", r)
	}
	if r := run("TestMisses"); r.Outcome != NotExecuted {
		t.Fatalf("%+v", r)
	}
	if r := run("TestAbsent"); r.Outcome != Failed || !strings.Contains(r.Problem, "did not run") {
		t.Fatalf("%+v", r)
	}
	request := Request{Fence: "fenced", Path: "mod/pkg/a.go", Source: source, CodeStart: 3, CodeEnd: 7, Witness: "mod/pkg/a_test.go::TestMisses", WitnessPath: "mod/pkg/a_test.go", Symbol: "TestMisses", WitnessSource: tests}
	if r := Load(root, request); r == nil || r.Outcome != NotExecuted {
		t.Fatal("receipt not found for its exact inputs", r)
	}
	request.Source = append([]byte{}, source...)
	request.Source[0] = 'P'
	if Load(root, request) != nil {
		t.Fatal("receipt matched changed fence bytes")
	}
	request = Request{Fence: "fenced", Path: "x.ts", Source: source, Witness: "x.test.ts::case", WitnessPath: "x.test.ts", Symbol: "case", WitnessSource: tests}
	r, err := Run(context.Background(), Options{Root: root, Scratch: t.TempDir(), Timeout: time.Minute}, request)
	if err != nil || r.Outcome != Unsupported {
		t.Fatal(r, err)
	}
}

// A mutation replaces the fence with its original in a private copy. A
// witness that passes before and fails after is witnessed; one that passes
// both times only ran the code without detecting the divergence.
func TestMutationSeparatesWitnessesFromExecution(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) []byte {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return []byte(text)
	}
	git := func(args ...string) {
		if out, err := command(context.Background(), root, nil, append([]string{"git"}, args...)...); err != nil {
			t.Fatal(string(out), err)
		}
	}
	git("init", "-q")
	write("mod/go.mod", "module example.com/m\n\ngo 1.22\n")
	source := write("mod/pkg/a.go", "package pkg\n\nfunc Fenced(x int) int {\n\treturn x * 2\n}\n")
	tests := write("mod/pkg/a_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestDetects(t *testing.T) { if Fenced(3) != 6 { t.Fatal() } }\nfunc TestRunsOnly(t *testing.T) { _ = Fenced(3) }\n")
	mutate := func(symbol, original string) *Receipt {
		mutated := strings.Replace(string(source), "\treturn x * 2\n", original, 1)
		receipt, err := Mutate(context.Background(), Options{Root: root, Scratch: t.TempDir(), Timeout: 2 * time.Minute}, Mutation{Request: Request{Fence: "fenced", Path: "mod/pkg/a.go", Source: source, CodeStart: 3, CodeEnd: 4, Witness: "mod/pkg/a_test.go::" + symbol, WitnessPath: "mod/pkg/a_test.go", Symbol: symbol, WitnessSource: tests}, Anchor: "a:3:4", Mutated: []byte(mutated)}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	if r := mutate("TestDetects", "\treturn x\n"); r.Outcome != Witnessed {
		t.Fatalf("%+v", r)
	}
	if r := mutate("TestRunsOnly", "\treturn x\n"); r.Outcome != NotWitnessing {
		t.Fatalf("%+v", r)
	}
	if r := mutate("TestDetects", "\treturn missing\n"); r.Outcome != MutantDoesNotCompile {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "mod/pkg/a.go")); string(b) != string(source) {
		t.Fatal("mutation touched the working tree")
	}
	request := Request{Fence: "fenced", Path: "mod/pkg/a.go", Source: source, CodeStart: 3, CodeEnd: 4, Witness: "mod/pkg/a_test.go::TestDetects", WitnessPath: "mod/pkg/a_test.go", Symbol: "TestDetects", WitnessSource: tests}
	if r := LoadMutation(root, request, "a:3:4"); r == nil || r.Outcome != MutantDoesNotCompile {
		t.Fatal("receipt not found for its exact inputs", r)
	}
	if LoadMutation(root, request, "b:3:4") != nil {
		t.Fatal("receipt matched another binding anchor")
	}
}
