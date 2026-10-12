package command_test

import (
	"concave/internal/analysis"
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/testrepo"
)

func TestRepositoryEditNavigationKeepsWorkingSourceAndRequiresText(t *testing.T) {
	for _, scenario := range []string{"text", "binary"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := testrepo.New(t)
			paths := []string{"crates/core/lib.rs", "npm-packages/convex/index.ts"}
			for _, path := range paths {
				fixture.Write(path, "original\n")
			}
			fixture.Config.Upstream = fixture.Commit()
			fixture.Save()
			fixture.Commit()
			source := "changed\n"
			if scenario == "binary" {
				source = "changed\x00\n"
			}
			for _, path := range paths {
				fixture.Write(path, source)
			}
			index, working := fixture.Git("ls-files", "--stage"), fixture.Git("diff", "--")
			code, text, stderr := run(fixture, "inspect", "queue", paths[0], "--json")
			var scoped analysis.FindingQueue
			if code != 0 || json.Unmarshal([]byte(text), &scoped) != nil || len(scoped.Groups) != 1 {
				t.Fatal(code, text, stderr)
			}
			code, text, stderr = run(fixture, "inspect", "queue", paths[0], "--group", scoped.Groups[0].ID)
			if code != 0 {
				t.Fatal(code, text, stderr)
			}
			if scenario == "binary" {
				if strings.Contains(text, "Find same edit across repository:") {
					t.Fatal("non-text findings are not byte-identical edit groups", text)
				}
			} else {
				var expanded analysis.FindingQueue
				if err := json.Unmarshal(replayCommand(t, text, "Find same edit across repository"), &expanded); err != nil {
					t.Fatal(err)
				}
				if expanded.Revision != "" || expanded.Scope.Family != "" || len(expanded.Scope.Paths) != 0 || expanded.Coverage.Uninspected != 0 || len(expanded.Groups) != 1 || expanded.Groups[0].ID != scoped.Groups[0].ID || len(expanded.Groups[0].Occurrences) != 2 {
					t.Fatal("working repository expansion changed scope or edit", expanded)
				}
			}
			if fixture.Git("ls-files", "--stage") != index || fixture.Git("diff", "--") != working {
				t.Fatal("navigation mutated source or index")
			}
		})
	}
}
