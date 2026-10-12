package command_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuleCommandsRetainRepositoryAndPolicyBasis(t *testing.T) {
	fixture := structuralFixture(t)
	// Suggestions must survive both shell-sensitive repository names and being
	// copied into a shell outside that repository.
	root := filepath.Join(t.TempDir(), "recipe owner's $(printf expanded)")
	if err := os.Rename(fixture.Root, root); err != nil {
		t.Fatal(err)
	}
	fixture.Root = root
	revision := fixture.Git("rev-parse", "HEAD")
	code, pinned, stderr := run(fixture, "rule", "explain", "public-import", "--rev", "HEAD")
	if code != 0 || strings.Contains(pinned, "--rev HEAD") || !strings.Contains(pinned, "--rev "+revision) {
		t.Fatal(code, pinned, stderr)
	}
	code, working, stderr := run(fixture, "rule", "explain", "public-import")
	if code != 0 || strings.Contains(working, "--rev") {
		t.Fatal(code, working, stderr)
	}
	if !strings.Contains(working, "ast-grep recipe:") || !strings.Contains(working, "Counterexample") || !strings.Contains(working, "Fixture") {
		t.Fatal("explanation omitted recipe or positive/negative examples", working)
	}
	operations := []struct{ label, name string }{{"Inspect recipe", "inspect"}, {"Test fixtures", "test"}}
	expected := map[string][]byte{}
	for _, operation := range operations {
		code, data, stderr := run(fixture, "rule", operation.name, "public-import", "--rev", revision, "--json")
		if code != 0 {
			t.Fatal(operation.name, code, data, stderr)
		}
		expected[operation.name] = []byte(data)
	}
	change := func(suffix string) {
		rule := &fixture.Config.Transforms[0]
		fix := `"@bijectionhq/bijection/` + suffix + `"`
		rule.ASTGrep.Fix = &fix
		rule.Tests[0].After = strings.Replace(rule.Tests[0].Before, `"convex/server"`, fix, 1)
		rule.Explanation.Why = "Changed " + suffix + " policy."
		fixture.Save()
	}
	change("committed")
	fixture.Commit()
	change("working")
	index, source := fixture.Git("ls-files", "--stage"), fixture.Git("diff", "--")
	for _, operation := range operations {
		t.Run("pinned-"+operation.name, func(t *testing.T) {
			if got := replayCommand(t, pinned, operation.label); !bytes.Equal(got, expected[operation.name]) {
				t.Fatal("continuation changed committed recipe or evidence", string(got), string(expected[operation.name]))
			}
		})
		t.Run("working-"+operation.name, func(t *testing.T) {
			code, current, stderr := run(fixture, "rule", operation.name, "public-import", "--json")
			if code != 0 {
				t.Fatal(code, current, stderr)
			}
			if bytes.Equal([]byte(current), expected[operation.name]) {
				t.Fatal("fixture did not change its working policy")
			}
			if got := replayCommand(t, working, operation.label); string(got) != current {
				t.Fatal("working continuation froze or retargeted its policy", string(got), current)
			}
		})
	}
	if fixture.Git("ls-files", "--stage") != index || fixture.Git("diff", "--") != source {
		t.Fatal("rule commands mutated source or index")
	}
}
