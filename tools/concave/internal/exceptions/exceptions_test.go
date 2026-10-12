package exceptions_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

const path = "crates/core/lib.rs"

func fenced(code string) string {
	return "// concave:begin read\n// why: preserve read ordering\n// witness: tests::read\n" + code + "// concave:end read\n"
}
func fixture(t *testing.T) *testrepo.Fixture {
	f := testrepo.New(t)
	f.Write("contract.md", "# Read\n")
	f.Commit()
	f.Write(path, fenced("fn first() { read(); }\n")+"fn second() {}\n")
	return f
}
func options(t *testing.T, f *testrepo.Fixture) exceptions.Options {
	t.Helper()
	r, c := f.Open()
	rep, e := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
	if e != nil {
		t.Fatal(e)
	}
	o := exceptions.Options{Path: path}
	for _, h := range rep.Hunks {
		o.Hunks = append(o.Hunks, h.ID)
	}
	return o
}
func record(t *testing.T, f *testrepo.Fixture) *exceptions.Proposal {
	t.Helper()
	o := options(t, f)
	r, c := f.Open()
	p, e := exceptions.Prepare(r, c, "record", []string{"read"}, o)
	if e != nil {
		t.Fatal(e)
	}
	if e = exceptions.Write(r, c, p); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestRecordExplainRefreshAndRestore(t *testing.T) {
	f := fixture(t)
	index := f.Git("ls-files", "--stage")
	before, _ := os.ReadFile(filepath.Join(f.Root, path))
	p := record(t, f)
	if p.Changes[0].Next.Reason != "" || len(p.Changes[0].Next.Witnesses) != 0 || !p.Changes[0].Next.Fence || p.Changes[0].Explanation.Why != "preserve read ordering" {
		t.Fatal(p)
	}
	if b, _ := os.ReadFile(filepath.Join(f.Root, path)); !bytes.Equal(b, before) || index != f.Git("ls-files", "--stage") {
		t.Fatal("source or index edited")
	}
	r, c := f.Open()
	v, e := explain(r, c, "read", "")
	if e != nil || v.Status != "valid" {
		t.Fatal(v, e)
	}
	f.Write(path, strings.Replace(string(before), "read();", "read_more();", 1))
	r, c = f.Open()
	v, e = explain(r, c, "read", "")
	if e != nil || v.Status != "stale" {
		t.Fatal(v, e)
	}
	p, e = exceptions.Prepare(r, c, "refresh", []string{"read"}, exceptions.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if p.Changes[0].Next.ReplacementSHA256 == p.Changes[0].Previous[0].ReplacementSHA256 || p.PolicyPatch == "" {
		t.Fatal(p)
	}
	if e = exceptions.Write(r, c, p); e != nil {
		t.Fatal(e)
	}
	f.Write(path, "fn first() {}\nfn second() {}\n")
	r, c = f.Open()
	v, e = explain(r, c, "read", "")
	if e != nil || v.Status != "obsolete" {
		t.Fatal(v, e)
	}
	p, e = exceptions.Prepare(r, c, "refresh", []string{"read"}, exceptions.Options{})
	if e != nil || p.Changes[0].Action != "remove" || p.Changes[0].Next != nil {
		t.Fatal(p, e)
	}
	if e = exceptions.Write(r, c, p); e != nil {
		t.Fatal(e)
	}
	_, c = f.Open()
	if len(c.Exceptions) != 0 {
		t.Fatal(c.Exceptions)
	}
}
func TestRecordRequiresExactSelectionAndKeepsOtherDrift(t *testing.T) {
	for _, kind := range []string{"outside", "duplicate", "stale"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t)
			o := options(t, f)
			switch kind {
			case "duplicate":
				o.Hunks = append(o.Hunks, o.Hunks[0])
			case "outside":
				f.Write("npm-packages/convex/index.ts", "other\n")
				o.Hunks = append(o.Hunks, "other")
			case "stale":
				f.Write(path, fenced("fn first() { changed(); }\n")+"fn second() {}\n")
			}
			r, c := f.Open()
			if _, e := exceptions.Prepare(r, c, "record", []string{"read"}, o); e == nil {
				t.Fatal("selection accepted", kind)
			}
		})
	}
	f := fixture(t)
	record(t, f)
	f.Write(path, fenced("fn first() { read(); }\n")+"fn second() {}\n\nfn unrelated() {}\n")
	r, c := f.Open()
	rep, e := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
	if e != nil {
		t.Fatal(e)
	}
	uncovered := 0
	for _, finding := range rep.Findings {
		if finding.Rule == "source/covered" {
			uncovered++
			if finding.Line != 7 || finding.EndLine != 8 || finding.Upstream.Start != 2 || finding.Upstream.End != 2 {
				t.Fatal("highlighted an approved span", finding)
			}
		}
	}
	if uncovered == 0 {
		t.Fatal("unrelated change hidden")
	}
}
func TestWriteRefusesChangedInputsAndTampering(t *testing.T) {
	for _, kind := range []string{"source", "reference", "policy", "proposal"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t)
			f.Write("witness.rs", "fn read() {}\n")
			f.Write(path, strings.Replace(fenced("fn first() { read(); }\n")+"fn second() {}\n", "// witness: tests::read", "// witness: witness.rs::read", 1))
			r, c := f.Open()
			p, e := exceptions.Prepare(r, c, "record", []string{"read"}, options(t, f))
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "source":
				f.Write(path, "peer\n")
			case "reference":
				f.Write("witness.rs", "fn read() { revised(); }\n")
			case "policy":
				f.Config.Families[0].Title += " (edited)"
				f.Save()
			case "proposal":
				p.Changes[0].Next.End++
			}
			before := policyBytes(t, f)
			if e := exceptions.Write(r, c, p); e == nil {
				t.Fatal("stale proposal applied")
			}
			if after := policyBytes(t, f); !bytes.Equal(before, after) {
				t.Fatal("policy changed on failure")
			}
		})
	}
}
func TestRefreshRequiresSelectionForWidenedScope(t *testing.T) {
	f := fixture(t)
	record(t, f)
	f.Write(path, fenced("fn first() { read(); }\nfn second() { changed(); }\n"))
	r, c := f.Open()
	if _, e := exceptions.Prepare(r, c, "refresh", []string{"read"}, exceptions.Options{}); e == nil {
		t.Fatal("silently widened exception")
	}
	p, e := exceptions.Prepare(r, c, "refresh", []string{"read"}, options(t, f))
	if e != nil || p.Changes[0].Next.End != 2 {
		t.Fatal(p, e)
	}
}
func TestRecordRejectsInvalidReferencesAndMetadata(t *testing.T) {
	for _, kind := range []string{"missing witness", "invalid metadata", "duplicated authority"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t)
			switch kind {
			case "missing witness":
				f.Write(path, strings.Replace(fenced("fn first() { read(); }\n"), "tests::read", "missing.rs::test", 1)+"fn second() {}\n")
			case "invalid metadata":
				f.Write(path, strings.Replace(fenced("fn first() { read(); }\n"), "// why: preserve read ordering\n", "", 1)+"fn second() {}\n")
			}
			o := options(t, f)
			if kind == "duplicated authority" {
				o.Metadata.Why = "policy reason"
			}
			r, c := f.Open()
			if _, e := exceptions.Prepare(r, c, "record", []string{"read"}, o); e == nil {
				t.Fatal("invalid explanation accepted")
			}
		})
	}
}
func TestUncommentableRecord(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/data.json", "{\"value\":1}\n")
	f.Config.Upstream = f.Commit()
	f.Write("contract.md", "# Data\n")
	f.Save()
	f.Commit()
	f.Write("crates/core/data.json", "{\"value\":2}\n")
	r, c := f.Open()
	rep, e := analysis.Analyze(r, c, analysis.Options{Paths: []string{"crates/core/data.json"}})
	if e != nil {
		t.Fatal(e)
	}
	o := exceptions.Options{Path: "crates/core/data.json", Hunks: []string{rep.Hunks[0].ID}, Metadata: policy.Explanation{Why: "required data", Witnesses: []string{path}}}
	p, e := exceptions.Prepare(r, c, "record", []string{"data"}, o)
	if e != nil || p.Changes[0].Next.Fence || p.Changes[0].Next.Reason != "required data" {
		t.Fatal(p, e)
	}
	if e = exceptions.Write(r, c, p); e != nil {
		t.Fatal(e)
	}
}
func TestConsolidateOnlyContainedBindings(t *testing.T) {
	f := fixture(t)
	before := []byte("fn first() {}\nfn second() {}\n")
	f.Config.Exceptions = []policy.Exception{{ID: "previous", Path: path, Start: 0, End: 1, UpstreamSHA256: repository.Digest(before), ReplacementSHA256: repository.Digest([]byte("old\n")), Reason: "old reason", Witnesses: []string{path}}}
	f.Save()
	o := options(t, f)
	o.Replace = []string{"previous"}
	r, c := f.Open()
	p, e := exceptions.Prepare(r, c, "record", []string{"read"}, o)
	if e != nil || len(p.Changes[0].Previous) != 1 {
		t.Fatal(p, e)
	}
	if e = exceptions.Write(r, c, p); e != nil {
		t.Fatal(e)
	}
	_, c = f.Open()
	if len(c.Exceptions) != 1 || c.Exceptions[0].ID != "read" {
		t.Fatal(c.Exceptions)
	}
}
func TestExplainCommittedSourceIgnoresDirtyExplanation(t *testing.T) {
	f := fixture(t)
	record(t, f)
	rev := f.Commit()
	f.Write(path, fenced("fn first() { changed(); }\n")+"fn second() {}\n")
	r, _ := f.Open()
	c, e := policy.Load(r, rev)
	if e != nil {
		t.Fatal(e)
	}
	v, e := explain(r, c, "read", rev)
	if e != nil || v.Status != "valid" {
		t.Fatal(v, e)
	}
}

func TestExplainPinnedStaleSourceUsesOnlySelectedHistory(t *testing.T) {
	for _, test := range []struct{ name, current string }{
		{"changed", fenced("fn first() { changed(); }\n") + "fn second() {}\n"},
		{"removed-fence", "fn first() {}\nfn second() {}\n"},
		{"deleted-file", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := fixture(t)
			record(t, f)
			approved := f.Commit()
			if test.name == "deleted-file" {
				if err := os.Remove(filepath.Join(f.Root, path)); err != nil {
					t.Fatal(err)
				}
			} else {
				f.Write(path, test.current)
			}
			selected := f.Commit()
			// A later matching body must never become evidence for an earlier revision.
			f.Write(path, fenced("fn first() { read(); }\n")+"fn second() {}\n")
			f.Commit()
			f.Write(path, fenced("fn first() { dirty(); }\n")+"fn second() {}\n")
			repo, _ := f.Open()
			config, err := policy.Load(repo, selected)
			if err != nil {
				t.Fatal(err)
			}
			explanation, err := explain(repo, config, "read", selected)
			if err != nil {
				t.Fatal(err)
			}
			previous := explanation.PreviousSource
			if previous == nil || previous.Status != "recovered" || previous.Commit != approved || explanation.Source != selected || explanation.Status == "valid" {
				t.Fatalf("incorrect pinned history: %+v, %+v", explanation, previous)
			}
			if !strings.Contains(previous.Diff, "-fn first() { read(); }") || strings.Contains(previous.Diff, "dirty();") || previous.CurrentFence != (test.name == "changed") {
				t.Fatal(previous)
			}
			if test.name == "changed" && !strings.Contains(previous.Diff, "+fn first() { changed(); }") {
				t.Fatal(previous)
			}
		})
	}
}

func TestExplainPinnedHistoryDoesNotBorrowFutureApprovalBytes(t *testing.T) {
	f := fixture(t)
	record(t, f)
	f.Write(path, fenced("fn first() { changed(); }\n")+"fn second() {}\n")
	selected := f.Commit()
	f.Write(path, fenced("fn first() { read(); }\n")+"fn second() {}\n")
	f.Commit()
	repo, _ := f.Open()
	config, err := policy.Load(repo, selected)
	if err != nil {
		t.Fatal(err)
	}
	explanation, err := explain(repo, config, "read", selected)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.PreviousSource == nil || explanation.PreviousSource.Status != "unavailable" || explanation.PreviousSource.Diff != "" {
		t.Fatal("future bytes supplied an unavailable approval", explanation.PreviousSource)
	}
}
