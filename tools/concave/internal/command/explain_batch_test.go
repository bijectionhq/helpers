package command_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func explainedFixture(t *testing.T) *testrepo.Fixture {
	t.Helper()
	fixture := testrepo.New(t)
	for _, entry := range []struct{ id, path, before, after string }{
		{"read", "crates/core/lib.rs", "fn first() {}\nfn second() {}\n", "fn first() { read(); }\n"},
		{"display", "npm-packages/convex/index.ts", "export const value = 1;\n", "export const value = 2;\n"},
	} {
		source := "// concave:begin " + entry.id + "\n// why: Keep " + entry.id + " ordering.\n// witness: " + entry.path + "\n" + entry.after + "// concave:end " + entry.id + "\n"
		fixture.Write(entry.path, source)
		fixture.Config.Exceptions = append(fixture.Config.Exceptions, policy.Exception{ID: entry.id, Path: entry.path, Fence: true, End: len(repository.Lines(entry.before)), UpstreamSHA256: repository.Digest([]byte(entry.before)), ReplacementSHA256: repository.Digest([]byte(entry.after))})
	}
	fixture.Save()
	fixture.Commit()
	return fixture
}

func TestCLIExplainUsesOneBatchShapeAndSelectedRevision(t *testing.T) {
	fixture := explainedFixture(t)
	fixture.Write("crates/core/lib.rs", "fn changed() {}\n")
	selected := fixture.Commit()
	fixture.Write("crates/core/lib.rs", "fn dirty() {}\n")
	read := func(ids ...string) []*exceptions.Explanation {
		t.Helper()
		args := append([]string{"exception", "explain"}, ids...)
		code, text, stderr := run(fixture, append(args, "--rev", selected, "--json")...)
		var explanations []*exceptions.Explanation
		if code != 0 || json.Unmarshal([]byte(text), &explanations) != nil {
			t.Fatal(code, text, stderr)
		}
		return explanations
	}
	batch := read("read", "display")
	if len(batch) != 2 || batch[0].ID != "display" || batch[0].Status != "valid" || batch[1].Status != "stale" || batch[1].Source != selected || batch[1].PreviousSource.CurrentFence {
		t.Fatal(batch)
	}
	for i, id := range []string{"display", "read"} {
		if !reflect.DeepEqual(batch[i], read(id)[0]) {
			t.Fatal("single-selection schema or evidence differs")
		}
	}
	code, text, stderr := run(fixture, "exception", "explain", "read", "display", "--rev", selected)
	if code != 0 || !strings.Contains(text, "read · stale") || !strings.Contains(text, "display · valid") || !strings.Contains(text, "current fence absent") || strings.Contains(text, "dirty()") {
		t.Fatal(code, text, stderr)
	}
	for _, ids := range [][]string{{"read", "missing"}, {"read", "read"}, {}, make([]string, exceptions.MaxChanges+1)} {
		code, text, stderr := run(fixture, append([]string{"exception", "explain"}, ids...)...)
		if code != 2 || text != "" {
			t.Fatal("invalid selection leaked a partial result", code, text, stderr)
		}
	}
}

func TestCLIExplainRefusesSourceAndPolicyChangesDuringHistory(t *testing.T) {
	for _, target := range []string{"crates/core/lib.rs", policy.File} {
		t.Run(target, func(t *testing.T) {
			fixture := explainedFixture(t)
			fixture.Write("crates/core/lib.rs", "fn changed() {}\n")
			git, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(fixture.Root, "target", "bin", "git")
			script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = log ]; then printf ' ' >> " + process.ShellCommand([]string{filepath.Join(fixture.Root, target)}) + "; fi; done\nexec " + process.ShellCommand([]string{git}) + " \"$@\"\n"
			fixture.Write("target/bin/git", script)
			if err := os.Chmod(wrapper, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(wrapper)+string(os.PathListSeparator)+os.Getenv("PATH"))
			code, text, stderr := run(fixture, "exception", "explain", "display", "read", "--json")
			if code != 3 || text != "" || !strings.Contains(stderr, target) {
				t.Fatal("mixed source versions escaped as a diagnosis", code, text, stderr)
			}
		})
	}
}
