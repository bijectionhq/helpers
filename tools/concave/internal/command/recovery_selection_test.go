package command_test

import (
	"concave/internal/analysis"
	"concave/internal/testrepo"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverySeparatesUnknownSelectionFromStaleReview(t *testing.T) {
	for _, scenario := range []string{"unknown", "changed", "restored", "shifted"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := testrepo.New(t)
			path := "crates/core/lib.rs"
			fixture.Write(path, "fn first() { changed(); }\nfn second() {}\n")
			inspection := filepath.Join(fixture.Root, "target/inspection")
			code, text, stderr := run(fixture, "inspect", path, "--json", "--output", inspection)
			if code != 0 {
				t.Fatal(code, text, stderr)
			}
			var report analysis.Report
			if err := json.Unmarshal([]byte(text), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Hunks) != 1 {
				t.Fatal(report.Hunks)
			}
			id := report.Hunks[0].ID
			expected := 3
			message := "selected source hunks changed; inspect again"
			switch scenario {
			case "unknown":
				id = "not-in-inspection"
				expected, message = 2, "hunk ID not-in-inspection is absent from inspection"
			case "changed":
				fixture.Write(path, "fn first() { concurrent(); }\nfn second() {}\n")
			case "restored":
				fixture.Write(path, "fn first() {}\nfn second() {}\n")
			case "shifted":
				fixture.Write(path, "// concurrent insertion\nfn first() { changed(); }\nfn second() {}\n")
			}
			before, err := os.ReadFile(filepath.Join(fixture.Root, path))
			if err != nil {
				t.Fatal(err)
			}
			index := fixture.Git("ls-files", "--stage")
			destination := filepath.Join(fixture.Root, "target/plan")
			code, text, stderr = run(fixture, "recover", "plan", filepath.Join(inspection, "inspection.json"), "--hunk", id, "--reason", "Restore the reviewed source.", "--output", destination)
			if code != expected || !strings.Contains(stderr, message) {
				t.Fatal(code, text, stderr)
			}
			after, err := os.ReadFile(filepath.Join(fixture.Root, path))
			if err != nil || string(before) != string(after) || fixture.Git("ls-files", "--stage") != index {
				t.Fatal("refusal changed source or index", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("refusal wrote a plan", err)
			}
		})
	}
}
