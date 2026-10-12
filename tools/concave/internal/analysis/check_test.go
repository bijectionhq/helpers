package analysis_test

import (
	"os"
	"path/filepath"
	"testing"

	"concave/internal/analysis"
	"concave/internal/testrepo"
)

func TestCheckEmptyScopeRetainsIdentityAndCoverage(t *testing.T) {
	fixture := testrepo.New(t)
	// Existing divergence outside the changed scope is neither inspected nor hidden as exact.
	fixture.Write("crates/core/lib.rs", "fn changed() {}\n")
	fixture.Commit()
	repo, config := fixture.Open()
	report, err := analysis.Check(repo, config, analysis.CheckOptions{Changed: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Kind != "concave-inspection" || report.Format != 1 || report.Head == "" || report.SnapshotSHA256 == "" || report.PolicySHA256 != config.SHA256 {
		t.Fatalf("lost identity: %+v", report)
	}
	if report.Status != "passed" || report.Coverage.Scope != "changed" || report.Coverage.Inspected != 0 || report.Coverage.Uninspected != report.Coverage.UpstreamFiles || report.Coverage.UpstreamFiles == 0 || len(report.Files) != 0 || len(report.Findings) != 0 || report.Metric != nil || report.Next == nil || len(report.Next.Argv) != 0 {
		t.Fatalf("empty scope expanded or lost accounting: %+v", report)
	}
	if _, err := analysis.Check(repo, config, analysis.CheckOptions{Changed: true, Paths: []string{"crates/core/lib.rs"}}); err == nil {
		t.Fatal("mixed selectors accepted")
	}
}

func TestChangedScopeTracksLinkIdentity(t *testing.T) {
	for _, change := range []string{"unchanged", "target", "regular", "deleted"} {
		t.Run(change, func(t *testing.T) {
			f := testrepo.New(t)
			p := "link"
			file := filepath.Join(f.Root, p)
			if err := os.Symlink("absent", file); err != nil {
				t.Fatal(err)
			}
			f.Commit()
			if change != "unchanged" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "target":
					if err := os.Symlink("another-absent", file); err != nil {
						t.Fatal(err)
					}
				case "regular":
					f.Write(p, "absent")
				}
			}
			r, c := f.Open()
			paths, full, err := analysis.ChangedPaths(r, c, "", "")
			if err != nil || full {
				t.Fatal(paths, full, err)
			}
			if change == "unchanged" && len(paths) != 0 || change != "unchanged" && (len(paths) != 1 || paths[0] != p) {
				t.Fatalf("%s: %v", change, paths)
			}
		})
	}
}

func TestCheckEnforcesReferenceWithoutCLI(t *testing.T) {
	for _, operation := range []string{"edit", "delete", "mode"} {
		t.Run(operation, func(t *testing.T) {
			fixture := testrepo.New(t)
			path := "packaging/facade.ts"
			fixture.Config.Boundaries = []string{path}
			fixture.Save()
			fixture.Commit()
			switch operation {
			case "edit":
				fixture.Write(path, "export const changed = true;\n")
			case "delete":
				if err := os.Remove(filepath.Join(fixture.Root, path)); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(filepath.Join(fixture.Root, path), 0700); err != nil {
					t.Fatal(err)
				}
			}
			repo, config := fixture.Open()
			report, err := analysis.Check(repo, config, analysis.CheckOptions{Changed: true})
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != "failed" || len(report.Findings) != 1 || report.Findings[0].Kind != "boundary_changed" || report.Findings[0].Rule != "policy/changed" {
				t.Fatalf("boundary accepted: %+v", report)
			}
		})
	}
	fixture := testrepo.New(t)
	fixture.Write("crates/core/lib.rs", "fn changed() {}\n")
	fixture.Config.Families[0].Title += " (edited)"
	fixture.Save()
	repo, config := fixture.Open()
	report, err := analysis.Check(repo, config, analysis.CheckOptions{Changed: true})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, finding := range report.Findings {
		found[finding.Kind] = true
	}
	if !found["policy_changed"] || !found["unexplained_change"] || len(found) != 2 || report.Metric != nil {
		t.Fatal(report.Findings)
	}
}
