package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/recovery"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func recoveryCheckFixture(t *testing.T) (*testrepo.Fixture, string, recovery.Plan) {
	t.Helper()
	fixture := testrepo.New(t)
	// A read-only check must not require even an executable writer command.
	fixture.Config.Writer = []string{"concave-test-writer-must-not-run"}
	fixture.Save()
	fixture.Commit()
	fixture.Write("crates/core/lib.rs", "fn first() { changed(); }\nfn second() {}\n")
	reportDir := filepath.Join(fixture.Root, "target/inspection")
	code, text, stderr := run(fixture, "inspect", "crates/core/lib.rs", "--output", reportDir, "--json")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var report analysis.Report
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	planDir := filepath.Join(fixture.Root, "target/plan")
	code, text, stderr = run(fixture, "recover", "plan", filepath.Join(reportDir, "inspection.json"), "--hunk", report.Hunks[0].ID, "--reason", "Restore reviewed source.", "--output", planDir)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	planPath := filepath.Join(planDir, "plan.json")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan recovery.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	return fixture, planPath, plan
}

func TestRecoveryCheckIsRepeatableAndReadOnly(t *testing.T) {
	fixture, path, _ := recoveryCheckFixture(t)
	before, index := fixture.Git("diff", "--"), fixture.Git("ls-files", "--stage")
	for attempt := 0; attempt < 2; attempt++ {
		code, text, stderr := run(fixture, "recover", "check", path)
		if code != 0 {
			t.Fatal(code, text, stderr)
		}
		var result struct {
			Status       string                     `json:"status"`
			Applied      bool                       `json:"applied"`
			Published    bool                       `json:"published"`
			Verification string                     `json:"verification"`
			Files        map[string]recovery.Effect `json:"files"`
		}
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "applicable" || result.Applied || result.Published || result.Verification != "pending" || len(result.Files) != 1 {
			t.Fatal(result)
		}
	}
	if fixture.Git("diff", "--") != before || fixture.Git("ls-files", "--stage") != index {
		t.Fatal("check edited source or index")
	}
}

func TestRecoveryCheckRefusesChangedInputsAndInvalidEffects(t *testing.T) {
	for _, scenario := range []string{"source", "deleted", "head", "policy", "patch", "effects", "engine", "filter", "ident", "encoding"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, path, plan := recoveryCheckFixture(t)
			want, message := 3, "stale preimage"
			switch scenario {
			case "source":
				fixture.Write("crates/core/lib.rs", "peer change\n")
			case "deleted":
				if err := os.Remove(filepath.Join(fixture.Root, "crates/core/lib.rs")); err != nil {
					t.Fatal(err)
				}
			case "head":
				fixture.Write("peer.txt", "peer commit")
				fixture.Commit()
				message = "HEAD changed"
			case "policy":
				fixture.Config.Families[0].Title += " (edited)"
				fixture.Save()
				message = "policy changed"
			case "patch":
				plan.Patch += "tampered"
				want, message = 2, "patch digest mismatch"
			case "effects":
				plan.Files["crates/core/lib.rs"].After.SHA256 = "wrong"
				want, message = 2, "patch paths or effects differ"
			case "engine":
				plan.SyntaxEngine = "wrong"
				message = "syntax engine requirement changed"
			case "filter", "ident", "encoding":
				attribute := map[string]string{"filter": "filter=late", "ident": "ident", "encoding": "working-tree-encoding=UTF-16"}[scenario]
				fixture.Write(".gitattributes", "*.rs "+attribute+"\n")
				fixture.Git("config", "filter.late.clean", "touch target/filter-ran; cat")
				fixture.Git("config", "filter.late.smudge", "touch target/filter-ran; cat")
				want, message = 2, "recovery refuses Git content conversion"
			}
			data, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			fixture.Write("target/plan/plan.json", string(data))
			// Raw snapshots avoid running a configured Git content filter during assertions.
			repo, _ := fixture.Open()
			before, content, err := repo.Snapshot("crates/core/lib.rs")
			if err != nil {
				t.Fatal(err)
			}
			index := fixture.Git("ls-files", "--stage")
			code, text, stderr := run(fixture, "recover", "check", path)
			if code != want || !strings.Contains(stderr, message) || strings.Contains(text, `"applicable"`) {
				t.Fatal(code, text, stderr)
			}
			after, current, err := repo.Snapshot("crates/core/lib.rs")
			if err != nil || !repository.Same(before, after) || string(content) != string(current) || fixture.Git("ls-files", "--stage") != index {
				t.Fatal("refused check changed source or index", err)
			}
			if _, err := os.Stat(filepath.Join(fixture.Root, "target/filter-ran")); !os.IsNotExist(err) {
				t.Fatal("check ran a content filter", err)
			}
		})
	}
}

func TestRecoveryCheckLeavesCreationAbsentAndRejectsAnOccupiedDestination(t *testing.T) {
	fixture, _, _ := recoveryCheckFixture(t)
	patch := "diff --git a/packaging/native.ts b/packaging/native.ts\nnew file mode 100644\n--- /dev/null\n+++ b/packaging/native.ts\n@@ -0,0 +1 @@\n+export const value = 1;\n"
	fixture.Write("target/creation.patch", patch)
	dir := filepath.Join(fixture.Root, "target/creation")
	code, text, stderr := run(fixture, "recover", "plan", filepath.Join(fixture.Root, "target/inspection/inspection.json"), "--patch", filepath.Join(fixture.Root, "target/creation.patch"), "--create", "packaging/native.ts", "--reason", "Create a reviewed native owner.", "--output", dir)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	planPath := filepath.Join(dir, "plan.json")
	code, text, stderr = run(fixture, "recover", "check", planPath)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	if _, err := os.Stat(filepath.Join(fixture.Root, "packaging/native.ts")); !os.IsNotExist(err) {
		t.Fatal("check created a destination", err)
	}
	fixture.Write("packaging/native.ts", "peer creation\n")
	code, text, stderr = run(fixture, "recover", "check", planPath)
	if code != 3 || !strings.Contains(stderr, "stale preimage") {
		t.Fatal(code, text, stderr)
	}
	data, err := os.ReadFile(filepath.Join(fixture.Root, "packaging/native.ts"))
	if err != nil || string(data) != "peer creation\n" {
		t.Fatal("check changed peer creation", err)
	}
}
