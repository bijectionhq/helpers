package command_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/testrepo"
)

func TestCompareHistoryReportsKeepsRealSourceChanges(t *testing.T) {
	fixture := testrepo.New(t)
	original := "fn first() {}\nfn second() {}\n"
	report := func(history bool) analysis.Report {
		args := []string{"--family", "engine", "--rev", "HEAD"}
		if history {
			args = append(args, "--history")
		}
		return inspection(t, fixture, args...)
	}
	history, plain := report(true), report(false)
	if history.Files[0].Selected.Kind != "file" || plain.Files[0].Selected.Kind != "blob" {
		t.Fatal("fixture did not exercise both inspection paths")
	}
	compare := func(t *testing.T, before, after analysis.Report, files, added, resolved int) {
		t.Helper()
		paths := []string{"target/before.json", "target/after.json"}
		for i, value := range []analysis.Report{before, after} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			fixture.Write(paths[i], string(data))
			paths[i] = filepath.Join(fixture.Root, paths[i])
		}
		working, index := fixture.Git("diff", "--"), fixture.Git("ls-files", "--stage")
		code, text, stderr := run(fixture, "compare", paths[0], paths[1], "--json")
		var got analysis.Comparison
		if code != 0 || json.Unmarshal([]byte(text), &got) != nil || !got.Comparable || len(got.Files) != files || len(got.AddedFindings) != added || len(got.ResolvedFindings) != resolved {
			t.Fatal(code, text, stderr)
		}
		if !strings.Contains(strings.Join(got.BasisChanges, ","), "history") || got.BehavioralParity != "not_established" {
			t.Fatal(got)
		}
		code, text, stderr = run(fixture, "compare", paths[0], paths[1])
		if code != 0 || !strings.Contains(text, "Comparable scope: true") {
			t.Fatal(code, text, stderr)
		}
		if fixture.Git("diff", "--") != working || fixture.Git("ls-files", "--stage") != index {
			t.Fatal("comparison mutated source or index")
		}
	}
	t.Run("diagnostics-only", func(t *testing.T) { compare(t, history, plain, 0, 0, 0) })
	fixture.Write("crates/core/lib.rs", "fn changed() {}\nfn second() {}\n")
	fixture.Commit()
	changed := report(false)
	t.Run("new-drift", func(t *testing.T) { compare(t, history, changed, 1, 1, 0) })
	fixture.Write("crates/core/lib.rs", original)
	fixture.Commit()
	restored := report(true)
	t.Run("restored-source", func(t *testing.T) { compare(t, changed, restored, 1, 0, 1) })
}

func TestCompareDecidesCandidateParity(t *testing.T) {
	f := testrepo.New(t)
	path := "crates/core/lib.rs"
	f.Write(path, "fn first() { changed(); }\nfn second() {}\n")
	baseline := f.Commit()
	f.Write(path, "fn first() {}\nfn second() { changed(); }\n")
	candidate := f.Commit()
	code, text, stderr := run(f, "compare", "--baseline", baseline, "--candidate", candidate, "--json")
	var verdict analysis.Parity
	if code != 1 || json.Unmarshal([]byte(text), &verdict) != nil || verdict.Kind != "concave-parity" || verdict.Status != "failed" ||
		verdict.Baseline != baseline || verdict.Candidate != candidate || len(verdict.Regressions) != 1 || verdict.Resolved != 1 {
		t.Fatal(code, text, stderr)
	}
	code, text, stderr = run(f, "compare", "--baseline", baseline, "--candidate", candidate)
	if code != 1 || !strings.Contains(text, "Parity failed") || !strings.Contains(text, "regression "+path+":2: source/covered") || !strings.Contains(text, "Next: ") {
		t.Fatal(code, text, stderr)
	}
	f.Write(path, "fn first() { changed(); }\nfn second() {}\n")
	f.Write("packaging/other.ts", "export const other = 1;\n")
	unrelated := f.Commit()
	if code, text, stderr = run(f, "compare", "--baseline", baseline, "--candidate", unrelated); code != 0 || !strings.Contains(text, "Parity passed") {
		t.Fatal(code, text, stderr)
	}
	if code, _, _ = run(f, "compare", "--baseline", baseline, "--candidate", candidate, "a.json"); code != 2 {
		t.Fatal("saved inspections mixed with revisions", code)
	}
	if f.Git("diff", "--") != "" {
		t.Fatal("comparison edited source")
	}
}
