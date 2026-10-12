package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

// Exercise the actual inspect -> plan -> writer -> apply path with Git settings
// that used to hide working bytes from both hunk IDs and diagnostic patches.
func TestCLIRecoveryUsesRawWorkingBytes(t *testing.T) {
	for _, kind := range []string{"crlf", "clean-filter"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			path := "crates/core/lib.rs"
			original := "fn first() {}\nfn second() {}\n"
			land, err := os.ReadFile("../../../../scripts/land.py")
			if err != nil {
				t.Fatal(err)
			}
			f.Write("scripts/land.py", string(land))
			f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
			f.Config.Cases["raw"] = policy.Case{Surfaces: []policy.Surface{{ID: "owner", Purpose: "raw bytes", Paths: []string{path}}}}
			f.Save()
			f.Commit()
			changed := strings.ReplaceAll(original, "\n", "\r\n")
			if kind == "crlf" {
				f.Git("config", "core.autocrlf", "input")
			} else {
				changed = strings.Replace(original, "first", "changed", 1)
				f.Write(".gitattributes", "*.rs filter=dogfood\n")
				f.Git("config", "filter.dogfood.clean", "touch target/filter-ran; sed 's/changed/first/g'")
			}
			f.Write("target/keep", "owned fixture output\n")
			f.Write(path, changed)
			index := f.Git("ls-files", "--stage")
			reportDir := filepath.Join(f.Root, "target/raw-inspection")
			n, out, stderr := run(f, "inspect", "--case", "raw", "--output", reportDir, "--json")
			if n != 0 {
				t.Fatal(n, out, stderr)
			}
			var report analysis.Report
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Hunks) != 1 || report.Totals.Removed == 0 || report.Totals.Added == 0 {
				t.Fatal("working bytes disappeared", report.Hunks, report.Totals)
			}
			needle := "+fn changed() {}\n"
			if kind == "crlf" {
				needle = "+fn first() {}\r\n"
			}
			if !strings.Contains(report.ReviewPatches["working"], needle) {
				t.Fatal("diagnostic patch filtered source", report.ReviewPatches["working"])
			}
			planDir := filepath.Join(f.Root, "target/raw-plan")
			n, out, stderr = run(f, "recover", "plan", filepath.Join(reportDir, "inspection.json"), "--hunk", report.Hunks[0].ID, "--reason", "Restore raw upstream bytes.", "--output", planDir)
			if kind == "clean-filter" {
				if n != 2 || !strings.Contains(stderr, "recovery refuses Git content conversion filter=dogfood") {
					t.Fatal("filtered recovery was not refused clearly", n, out, stderr)
				}
				got, err := os.ReadFile(filepath.Join(f.Root, path))
				if err != nil || string(got) != changed || f.Git("ls-files", "--stage") != index {
					t.Fatal("refusal changed source or index", err)
				}
				if _, err := os.Stat(filepath.Join(f.Root, "target/filter-ran")); !os.IsNotExist(err) {
					t.Fatal("inspection or refused recovery executed a filter", err)
				}
				return
			}
			if n != 0 {
				t.Fatal(n, out, stderr)
			}
			n, out, stderr = run(f, "recover", "apply", filepath.Join(planDir, "plan.json"))
			if n != 0 {
				t.Fatal(n, out, stderr)
			}
			got, err := os.ReadFile(filepath.Join(f.Root, path))
			if err != nil || string(got) != original {
				t.Fatal("recovery did not restore exact bytes", string(got), err)
			}
			if f.Git("ls-files", "--stage") != index {
				t.Fatal("recovery changed index")
			}
			if _, err := os.Stat(filepath.Join(f.Root, "target/filter-ran")); !os.IsNotExist(err) {
				t.Fatal("inspection/recovery executed a clean filter", err)
			}
			n, out, stderr = run(f, "check", path, "--json")
			if n != 0 {
				t.Fatal(n, out, stderr)
			}
		})
	}
}
