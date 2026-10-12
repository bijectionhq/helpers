package analysis_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func inspect(t *testing.T, f *testrepo.Fixture, o analysis.Options) *analysis.Report {
	t.Helper()
	r, c := f.Open()
	report, err := analysis.Analyze(r, c, o)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestRepeatedEditsPreserveEveryLocationAndRule(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/other.rs", "fn first() {}\nfn second() {}\n")
	f.Config.Upstream = f.Commit()
	f.Save()
	f.Commit()
	for _, p := range []string{"crates/core/lib.rs", "crates/core/other.rs"} {
		f.Write(p, "fn replacement() {}\nfn second() {}\n")
	}
	r := inspect(t, f, analysis.Options{Paths: []string{"crates/"}})
	q := analysis.BuildFindingQueue(r)
	if q.Unresolved != 2 || q.FindingCount != 2 || q.UnresolvedHunks != 2 || len(q.Groups) != 1 || q.Complete {
		t.Fatal(q)
	}
	g := q.Groups[0]
	if len(g.Occurrences) != 2 || g.Before != "fn first() {}\n" || g.After != "fn replacement() {}\n" || g.Occurrences[0].Hunk == g.Occurrences[1].Hunk {
		t.Fatal(g)
	}
	for _, o := range g.Occurrences {
		if o.Hunk == "" || o.Line != 1 || o.Family != "engine" {
			t.Fatal(o)
		}
	}
	// Ordering and grouping are independent of report traversal order.
	r.Findings[0], r.Findings[1] = r.Findings[1], r.Findings[0]
	r.Files[0], r.Files[1] = r.Files[1], r.Files[0]
	a, _ := json.Marshal(q)
	b, _ := json.Marshal(analysis.BuildFindingQueue(r))
	if string(a) != string(b) {
		t.Fatal("nondeterministic queue")
	}
	r.Findings[0].Rule = "source/exact"
	if len(analysis.BuildFindingQueue(r).Groups) != 2 {
		t.Fatal("group collapsed different rules")
	}
}

func TestApprovalAndNonTextFailuresStayDistinct(t *testing.T) {
	f := testrepo.New(t)
	f.Config.Exceptions = []policy.Exception{{ID: "reviewed", Path: "crates/core/lib.rs", UpstreamSHA256: repository.Digest([]byte("fn first() {}\nfn second() {}\n")), Start: 0, End: 1,
		ReplacementSHA256: repository.Digest([]byte("fn extension() {}\n")), Reason: "fixture", Witnesses: []string{"crates/core/lib.rs"}}}
	f.Save()
	f.Write("crates/core/lib.rs", "fn extension() {}\nfn second() {}\n")
	f.Write("npm-packages/client/protocol.ts", "\x00binary")
	os.Remove(filepath.Join(f.Root, "npm-packages/convex/index.ts"))
	q := analysis.BuildFindingQueue(inspect(t, f, analysis.Options{}))
	if q.Approved != 1 || q.Unresolved != 2 || q.Complete {
		t.Fatal(q)
	}
	kinds := map[string]bool{}
	for _, g := range q.Groups {
		kinds[g.Kind] = true
	}
	if !kinds["missing_file"] || !kinds["unsupported_change"] {
		t.Fatal(q.Groups)
	}
	for _, g := range q.Groups {
		for _, o := range g.Occurrences {
			if o.Path == "crates/core/lib.rs" {
				t.Fatal("approved change queued")
			}
		}
	}
}

func TestOnlyFullCleanReviewIsComplete(t *testing.T) {
	f := testrepo.New(t)
	q := analysis.BuildFindingQueue(inspect(t, f, analysis.Options{}))
	if !q.Complete {
		t.Fatal(q)
	}
	q = analysis.BuildFindingQueue(inspect(t, f, analysis.Options{Paths: []string{"crates/core/lib.rs"}}))
	if q.Complete || q.Coverage.Uninspected == 0 {
		t.Fatal("scoped result claimed completion")
	}
}
