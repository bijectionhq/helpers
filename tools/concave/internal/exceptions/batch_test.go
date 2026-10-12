package exceptions_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func namedFence(id, code string) string {
	s := strings.Replace(fenced(code), "concave:begin read", "concave:begin "+id, 1)
	return strings.Replace(s, "concave:end read", "concave:end "+id, 1)
}

func batchFixture(t *testing.T, split bool) (*testrepo.Fixture, map[string]string) {
	t.Helper()
	f := testrepo.New(t)
	f.Write(path, "fn first() {}\nfn middle() {}\nfn second() {}\nfn last() {}\n")
	f.Config.Upstream = f.Commit()
	f.Write("contract.md", "# Read\n")
	f.Save()
	f.Commit()
	sources := map[string]string{path: namedFence("alpha", "fn first() { first(); }\n") + "fn middle() {}\n"}
	if split {
		sources[path] += "fn second() {}\n"
		sources["npm-packages/convex/index.ts"] = namedFence("beta", "export const value = 2;\n")
	} else {
		sources[path] += namedFence("beta", "fn second() { second(); }\n")
	}
	sources[path] += "fn last() {}\n\nfn unrelated() {}\n"
	for p, s := range sources {
		f.Write(p, s)
	}
	return f, sources
}

// policyBytes is the policy file and every binding file, by path.
func policyBytes(t *testing.T, f *testrepo.Fixture) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Root, policy.File))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(filepath.Join(f.Root, policy.BindingsDirectory), func(name string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) || err == nil && entry.IsDir() {
			return nil
		}
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		b = append(append(append(b, 0), name...), data...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBatchRecordsShareAnalysisAndPreserveUnselectedDrift(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-file", true: "multiple-files"}[split], func(t *testing.T) {
			f, sources := batchFixture(t, split)
			r, c := f.Open()
			before := policyBytes(t, f)
			index := f.Git("ls-files", "--stage")
			o := exceptions.Options{}
			if !split {
				o.Path = path
			}
			start := r.Processes
			p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"beta", "alpha"}, o)
			batchProcesses := r.Processes - start
			if err != nil || len(p.Changes) != 2 || p.Changes[0].Next.ID != "alpha" || p.Kind != "concave-exception-proposal" {
				t.Fatal(p, err)
			}
			start = r.Processes
			for _, id := range []string{"alpha", "beta"} {
				if _, err := exceptions.Prepare(r, c, "record", []string{id}, o); err != nil {
					t.Fatal(err)
				}
			}
			if singleProcesses := r.Processes - start; batchProcesses >= singleProcesses {
				t.Fatalf("batch repeated analysis: batch=%d separate=%d", batchProcesses, singleProcesses)
			}
			q, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha", "beta"}, o)
			if err != nil || q.ProposalSHA256 != p.ProposalSHA256 || !bytes.Equal(before, policyBytes(t, f)) {
				t.Fatal("preview is not stable and read-only", q, err)
			}
			for _, one := range p.Changes {
				if !one.Next.Fence || strings.Contains(one.SourceDiff, "unrelated") {
					t.Fatal("batch absorbed unselected drift", one)
				}
			}
			if err := exceptions.Write(r, c, p); err != nil {
				t.Fatal(err)
			}
			r, c = f.Open()
			if len(c.Exceptions) != 2 {
				t.Fatal(c.Exceptions)
			}
			for _, id := range []string{"alpha", "beta"} {
				e, err := explain(r, c, id, "")
				if err != nil || e.Status != "valid" {
					t.Fatal(e, err)
				}
			}
			report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
			if err != nil || len(report.Findings) != 1 || report.Findings[0].Kind != "unexplained_change" {
				t.Fatal("unselected change lost", report, err)
			}
			for p, expected := range sources {
				actual, err := os.ReadFile(filepath.Join(f.Root, p))
				if err != nil || string(actual) != expected {
					t.Fatal("batch edited source", p, err)
				}
			}
			if index != f.Git("ls-files", "--stage") {
				t.Fatal("batch edited shared index")
			}
		})
	}
}

func TestBatchRecordsRefuseIncompleteOrAmbiguousSelections(t *testing.T) {
	for _, kind := range []string{"duplicate", "missing-last", "invalid-last", "path-mismatch", "existing", "too-many", "empty", "file", "hunk", "replacement", "metadata", "overlapping-transform"} {
		t.Run(kind, func(t *testing.T) {
			f, sources := batchFixture(t, true)
			ids, o := []string{"alpha", "beta"}, exceptions.Options{}
			switch kind {
			case "duplicate":
				ids = []string{"alpha", "alpha"}
			case "missing-last":
				ids[1] = "missing"
			case "invalid-last":
				f.Write("npm-packages/convex/index.ts", strings.Replace(sources["npm-packages/convex/index.ts"], "tests::read", "missing.rs::read", 1))
			case "path-mismatch":
				o.Path = path
			case "existing":
				r, c := f.Open()
				p, err := exceptions.Prepare(r, c, "record", []string{"alpha"}, exceptions.Options{Path: path})
				if err != nil {
					t.Fatal(err)
				}
				if err := exceptions.Write(r, c, p); err != nil {
					t.Fatal(err)
				}
			case "too-many":
				ids = make([]string, exceptions.MaxChanges+1)
			case "empty":
				ids = nil
			case "file":
				o.File = true
			case "hunk":
				o.Hunks = []string{"hunk"}
			case "replacement":
				o.Replace = []string{"old"}
			case "metadata":
				o.Metadata.Why = "not in source"
			case "overlapping-transform":
				f.Config.Transforms = []policy.Transform{{ID: "beta-rule", Paths: []string{"npm-packages/convex/index.ts"}, Replacements: []policy.Replacement{{From: "value = 1", To: "value = 2"}}, Explanation: policy.Explanation{Why: "native value", Witnesses: []string{path}}}}
				f.Save()
			}
			before := policyBytes(t, f)
			r, c := f.Open()
			if p, err := exceptions.Prepare(r, c, exceptions.Record, ids, o); err == nil || p != nil {
				t.Fatal("invalid batch accepted", p, err)
			}
			if !bytes.Equal(before, policyBytes(t, f)) {
				t.Fatal("invalid batch wrote part of its policy")
			}
		})
	}
}

func TestBatchWriteRefusesAnyStaleMemberOrTamperedReview(t *testing.T) {
	for _, kind := range []string{"last-source", "reference", "policy", "proposal"} {
		t.Run(kind, func(t *testing.T) {
			f, sources := batchFixture(t, true)
			f.Write("witness.rs", "fn read() {}\n")
			for path, source := range sources {
				sources[path] = strings.ReplaceAll(source, "// witness: tests::read", "// witness: witness.rs::read")
				f.Write(path, sources[path])
			}
			r, c := f.Open()
			p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha", "beta"}, exceptions.Options{})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "last-source":
				f.Write("npm-packages/convex/index.ts", strings.Replace(sources["npm-packages/convex/index.ts"], "value = 2", "value = 3", 1))
			case "reference":
				f.Write("witness.rs", "fn read() { revised(); }\n")
			case "policy":
				f.Config.Families[0].Title += " (edited)"
				f.Save()
			case "proposal":
				p.Changes[1].Next.End++
			}
			before := policyBytes(t, f)
			if err := exceptions.Write(r, c, p); err == nil {
				t.Fatal("stale or tampered batch was written")
			}
			if !bytes.Equal(before, policyBytes(t, f)) {
				t.Fatal("failed batch recorded an earlier member")
			}
		})
	}
}

func TestBatchKeepsExistingBindings(t *testing.T) {
	f, _ := batchFixture(t, false)
	f.Write("npm-packages/convex/index.ts", namedFence("gamma", "export const value = 3;\n"))
	r, c := f.Open()
	old, err := exceptions.Prepare(r, c, "record", []string{"gamma"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, old); err != nil {
		t.Fatal(err)
	}
	r, c = f.Open()
	previous := c.Exceptions[0]
	p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha", "beta"}, exceptions.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	_, c = f.Open()
	if len(c.Exceptions) != 3 || !reflect.DeepEqual(c.Exceptions[2], previous) {
		t.Fatal("batch changed an existing binding", c.Exceptions)
	}
}
