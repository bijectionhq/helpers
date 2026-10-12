package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestCLIHistoryIsReadOnlyCompleteAndSaved(t *testing.T) {
	f := testrepo.New(t)
	const before = "// top\n// anchor\n#[cfg(test)]\nfn old_test() {}\nfn first() {}\nfn second() {}\n"
	const after = "// top\n// anchor\nfn first() {}\nfn second() {}\n"
	for _, p := range []string{"crates/core/lib.rs", "crates/core/other.rs"} {
		f.Write(p, before)
	}
	f.Commit()
	for _, p := range []string{"crates/core/lib.rs", "crates/core/other.rs"} {
		f.Write(p, after)
	}
	removed := f.Commit()
	f.Config.Upstream = removed
	f.Config.PreserveExactly = nil
	f.Config.Restorations = []policy.Restoration{{ID: "retained", RemovedBy: removed, Paths: []string{"crates/core/"}, Explanation: policy.Explanation{Why: "Retain the historical tests.", Witnesses: []string{"crates/core/lib.rs"}}}}
	f.Save()
	f.Commit()
	f.Write("crates/core/lib.rs", strings.Replace(before, "old_test", "updated_test", 1))
	f.Write("crates/core/other.rs", before)
	index := f.Git("ls-files", "--stage")
	status := f.Git("status", "--porcelain")
	n, out, err := run(f, "inspect", "--history", "--top", "1", "crates/core/")
	if n != 0 || !strings.Contains(out, "diagnostic only") || !strings.Contains(out, "updated_test") || !strings.Contains(out, "1 more historical blocks") {
		t.Fatal(n, out, err)
	}
	dir := filepath.Join(f.Root, "target/history")
	n, out, err = run(f, "inspect", "--history", "--top", "1", "--json", "--output", dir, "crates/core/")
	if n != 0 {
		t.Fatal(n, out, err)
	}
	var r analysis.Report
	if e := json.Unmarshal([]byte(out), &r); e != nil {
		t.Fatal(e)
	}
	if len(r.History) != 2 || len(r.Findings) != 1 || !r.Scope.History {
		t.Fatal(r)
	}
	raw, e := os.ReadFile(filepath.Join(dir, "inspection.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &r); e != nil || len(r.History) != 2 {
		t.Fatal(e, r.History)
	}
	pairA := r.History[0].ID + "=" + r.History[0].Selected.Hunk
	pairB := r.History[1].ID + "=" + r.History[1].Selected.Hunk
	n, text, err := run(f, "inspect", "--history", "--history-pair", pairA, "crates/core/")
	if n != 0 || !strings.Contains(text, "explicit_selection") || !strings.Contains(text, r.History[0].ID) {
		t.Fatal(n, text, err)
	}
	var selected analysis.Report
	for _, pairs := range [][]string{{pairA, pairB}, {pairB, pairA}} {
		n, text, err = run(f, "inspect", "--history", "--history-pair", pairs[0], "--history-pair", pairs[1], "--json", "crates/core/")
		if n != 0 {
			t.Fatal(n, text, err)
		}
		var next analysis.Report
		if err := json.Unmarshal([]byte(text), &next); err != nil {
			t.Fatal(err)
		}
		if len(next.Scope.HistoryPairs) != 2 || !reflect.DeepEqual(next.Findings, r.Findings) {
			t.Fatal(next.Scope, next.Findings)
		}
		if selected.SnapshotSHA256 != "" && next.SnapshotSHA256 != selected.SnapshotSHA256 {
			t.Fatal("flag order changes identity")
		}
		selected = next
	}
	for _, args := range [][]string{
		{"inspect", "--history-pair", pairA},
		{"inspect", "--history", "--history-pair", "malformed"},
		{"inspect", "--history", "--history-pair", pairA, "--history-pair", pairA},
		{"inspect", "--history", "--history-pair", r.History[0].ID + "=" + r.History[1].Selected.Hunk},
		{"check", "--history-pair", pairA},
	} {
		if n, _, _ := run(f, args...); n != 2 {
			t.Fatal(args, n)
		}
	}
	n, _, _ = run(f, "check", "--history")
	if n != 2 {
		t.Fatal("history exposed as enforcement")
	}
	n, _, _ = run(f, "check")
	if n != 1 {
		t.Fatal("adaptation was accepted")
	}
	if f.Git("ls-files", "--stage") != index || f.Git("status", "--porcelain") != status {
		t.Fatal("history changed source or index")
	}
}

func TestCLIHistoryNamesEmptyScope(t *testing.T) {
	f := testrepo.New(t)
	n, out, err := run(f, "inspect", "--history", "crates/core/lib.rs")
	if n != 0 || !strings.Contains(out, "No pure historical deletion blocks") {
		t.Fatal(n, out, err)
	}
}
