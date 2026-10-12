package command_test

import (
	"concave/internal/analysis"
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/testrepo"
)

func TestCLIFileReviewScopeAndDrillDown(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "changed\nfn second() {}\n")
	f.Write("npm-packages/convex/index.ts", "export const value = 2;\n")
	rev := f.Commit()
	f.Write("crates/core/lib.rs", "unrelated working change\n")
	index := f.Git("ls-files", "--stage")
	working := f.Git("diff", "--")
	n, out, stderr := run(f, "inspect", "queue", "--rev", rev, "--files", "--top", "1", "--json")
	var v analysis.FindingFileView
	if n != 0 || json.Unmarshal([]byte(out), &v) != nil || v.Kind != "concave-finding-files" || v.Revision != rev || len(v.Files) != 2 || v.Complete {
		t.Fatal(n, out, stderr)
	}
	if strings.Contains(out, `"before"`) || strings.Contains(out, `"after"`) {
		t.Fatal("summary includes full edit bytes", out)
	}
	for _, file := range v.Files {
		for _, group := range file.Groups {
			n, out, stderr = run(f, "inspect", "queue", "--rev", rev, "--group", group, "--json")
			var q analysis.FindingQueue
			if n != 0 || json.Unmarshal([]byte(out), &q) != nil || len(q.Groups) != 1 || q.SelectedGroup != group || q.Groups[0].Occurrences[0].Path != file.Path {
				t.Fatal("group drill-down failed", n, out, stderr)
			}
		}
	}
	n, out, stderr = run(f, "inspect", "queue", "--rev", rev, "--files", "--top", "1")
	if n != 0 || !strings.Contains(out, "1 more files") || !strings.Contains(out, v.Files[0].Path) || strings.Contains(out, v.Files[1].Path) {
		t.Fatal(n, out, stderr)
	}
	n, out, stderr = run(f, "inspect", "queue", "--rev", rev, "--files", "--top", "0")
	if n != 0 || !strings.Contains(out, v.Files[1].Path) || strings.Contains(out, "more files") {
		t.Fatal(n, out, stderr)
	}
	for _, scope := range [][]string{{"--family", "engine"}, {"crates/core/lib.rs"}} {
		n, out, stderr = run(f, append([]string{"inspect", "queue", "--rev", rev, "--files", "--json"}, scope...)...)
		var selected analysis.FindingFileView
		if n != 0 || json.Unmarshal([]byte(out), &selected) != nil || len(selected.Files) != 1 || selected.Files[0].Path != "crates/core/lib.rs" || selected.Coverage.Uninspected == 0 || selected.Complete {
			t.Fatal(n, out, stderr)
		}
	}
	for _, args := range [][]string{{"inspect", "queue", "--files", "--group", v.Files[0].Groups[0]}, {"inspect", "queue", "--files", "--top", "-1"}} {
		if n, out, stderr := run(f, args...); n != 2 {
			t.Fatal(args, n, out, stderr)
		}
	}
	if f.Git("ls-files", "--stage") != index || f.Git("diff", "--") != working {
		t.Fatal("review changed source or index")
	}
}
