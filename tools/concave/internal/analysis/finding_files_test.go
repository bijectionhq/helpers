package analysis_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestFileReviewCountsEachHunkOnceAcrossFindings(t *testing.T) {
	q := &analysis.FindingQueue{FindingSummary: analysis.FindingSummary{Kind: "concave-finding-queue", FindingCount: 4, UnresolvedHunks: 2, SnapshotSHA256: "snapshot"}, Groups: []analysis.FindingGroup{
		{ID: "b", Kind: "unexplained_change", TextHunk: true, Before: "α\n", After: "β\n", Occurrences: []analysis.FindingOccurrence{
			{Path: "a.rs", Family: "engine", Hunk: "first"}, {Path: "a.rs", Family: "engine", Hunk: "second"},
		}},
		{ID: "a", Kind: "stale_exception", TextHunk: true, Before: "α\n", After: "β\n", Occurrences: []analysis.FindingOccurrence{{Path: "a.rs", Family: "engine", Hunk: "first"}}},
		{ID: "c", Kind: "invalid_fence", Occurrences: []analysis.FindingOccurrence{{Path: "a.rs", Family: "engine"}}},
	}}
	before, _ := json.Marshal(q)
	v := analysis.FindingFiles(q)
	if v.Kind != "concave-finding-files" || v.SnapshotSHA256 != q.SnapshotSHA256 || len(v.Files) != 1 || v.FindingCount != 4 {
		t.Fatal(v)
	}
	f := v.Files[0]
	if f.Findings != 4 || f.TextHunks != 2 || f.RemovedLines != 2 || f.AddedLines != 2 || f.ChangedBytes != 12 || f.OtherFindings != 1 {
		t.Fatal(f)
	}
	if !reflect.DeepEqual(f.Groups, []string{"a", "b", "c"}) || !reflect.DeepEqual(f.Kinds, []string{"invalid_fence", "stale_exception", "unexplained_change"}) {
		t.Fatal(f)
	}
	after, _ := json.Marshal(q)
	if string(before) != string(after) {
		t.Fatal("file projection mutated the edit queue")
	}
	// Input traversal does not define the file view or its drill-down order.
	q.Groups[0], q.Groups[2] = q.Groups[2], q.Groups[0]
	a, _ := json.Marshal(v)
	b, _ := json.Marshal(analysis.FindingFiles(q))
	if string(a) != string(b) {
		t.Fatal("nondeterministic file view")
	}
}

func TestFileReviewOrdersKnownTextSizesBeforeOtherFindings(t *testing.T) {
	q := &analysis.FindingQueue{Groups: []analysis.FindingGroup{
		{ID: "large", TextHunk: true, Before: "a longer old line\n", After: "new\n", Occurrences: []analysis.FindingOccurrence{{Path: "a.rs", Hunk: "a"}}},
		{ID: "small", TextHunk: true, Before: "a", After: "b", Occurrences: []analysis.FindingOccurrence{{Path: "c.rs", Hunk: "c"}, {Path: "b.rs", Hunk: "b"}, {Path: "d.rs", Hunk: "d1"}, {Path: "d.rs", Hunk: "d2"}}},
		{ID: "missing", Kind: "missing_file", Occurrences: []analysis.FindingOccurrence{{Path: "0.rs"}}},
		{ID: "binary", Kind: "unsupported_change", Occurrences: []analysis.FindingOccurrence{{Path: "1.png"}}},
	}}
	v := analysis.FindingFiles(q)
	paths := []string{}
	for _, f := range v.Files {
		paths = append(paths, f.Path)
	}
	if !reflect.DeepEqual(paths, []string{"b.rs", "c.rs", "a.rs", "d.rs", "0.rs", "1.png"}) {
		t.Fatal(paths)
	}
	// Non-newline-terminated lines count, and unknown byte sizes stay marked.
	if v.Files[0].RemovedLines != 1 || v.Files[0].AddedLines != 1 || v.Files[4].ChangedBytes != 0 || v.Files[4].OtherFindings != 1 {
		t.Fatal(v.Files)
	}
}

func TestFileReviewOmitsApprovedHunksButKeepsUnrelatedDrift(t *testing.T) {
	f := testrepo.New(t)
	f.Config.Exceptions = []policy.Exception{{ID: "reviewed", Path: "crates/core/lib.rs", UpstreamSHA256: repository.Digest([]byte("fn first() {}\nfn second() {}\n")), Start: 0, End: 1,
		ReplacementSHA256: repository.Digest([]byte("fn extension() {}\n")), Reason: "fixture", Witnesses: []string{"crates/core/lib.rs"}}}
	f.Save()
	f.Write("crates/core/lib.rs", "fn extension() {}\nfn second() {}\nfn extra() {}\n")
	f.Write("npm-packages/client/protocol.ts", "\x00binary")
	if err := os.Remove(filepath.Join(f.Root, "npm-packages/convex/index.ts")); err != nil {
		t.Fatal(err)
	}
	q := analysis.BuildFindingQueue(inspect(t, f, analysis.Options{}))
	v := analysis.FindingFiles(q)
	if len(v.Files) != 3 || v.Complete || v.FindingCount != q.FindingCount || v.Coverage != q.Coverage {
		t.Fatal(v)
	}
	first := v.Files[0]
	if first.Path != "crates/core/lib.rs" || first.TextHunks != 1 || first.RemovedLines != 0 || first.AddedLines != 1 || first.ChangedBytes != len("fn extra() {}\n") {
		t.Fatal("approved hunk counted or unreviewed hunk lost", first)
	}
	for _, f := range v.Files[1:] {
		if f.OtherFindings != 1 || f.TextHunks != 0 {
			t.Fatal("non-text finding lost", f)
		}
	}
	for _, file := range v.Files {
		for _, id := range file.Groups {
			found := false
			for _, g := range q.Groups {
				found = found || g.ID == id
			}
			if !found {
				t.Fatal("drill-down group absent", id)
			}
		}
	}
}

func TestFileReviewIncludesFindingsOutsideInheritedRows(t *testing.T) {
	r := &analysis.Report{Findings: []policy.Finding{{Path: "concave.json", Kind: "policy_changed", Rule: "policy/reviewed"}}}
	v := analysis.FindingFiles(analysis.BuildFindingQueue(r))
	if len(v.Files) != 1 || v.Files[0].Path != "concave.json" || v.Files[0].OtherFindings != 1 || v.FindingCount != 1 || v.Unresolved != 0 || v.Complete {
		t.Fatal(v)
	}
}

func TestFileReviewCompletionRequiresFullCleanInspection(t *testing.T) {
	f := testrepo.New(t)
	for _, o := range []analysis.Options{{}, {Paths: []string{"crates/core/lib.rs"}}} {
		q := analysis.BuildFindingQueue(inspect(t, f, o))
		v := analysis.FindingFiles(q)
		if v.Complete != (len(o.Paths) == 0) || len(v.Files) != 0 || !reflect.DeepEqual(v.Scope, q.Scope) || v.Coverage != q.Coverage {
			t.Fatal(v)
		}
		data, _ := json.Marshal(v)
		var decoded map[string]any
		json.Unmarshal(data, &decoded)
		if _, ok := decoded["groups"]; ok || string(data) == "" {
			t.Fatal("file view includes full edit groups", string(data))
		}
		if _, ok := decoded["files"].([]any); !ok {
			t.Fatal("empty file list was not an array", string(data))
		}
	}
}
