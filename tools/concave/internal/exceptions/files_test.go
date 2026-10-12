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

const asset = "crates/core/logo.png"
const oldAsset = "\x00original asset"
const newAsset = "\x00Bijection asset"

func fileFixture(t *testing.T, original string) *testrepo.Fixture {
	f := testrepo.New(t)
	f.Write(asset, original)
	f.Config.Upstream = f.Commit()
	f.Write("contract.md", "# Branding\n")
	f.Save()
	f.Commit()
	return f
}
func fileOptions() exceptions.Options {
	return exceptions.Options{Path: asset, File: true, Metadata: policy.Explanation{Why: "owned branding", Witnesses: []string{path}, RemoveWhen: "original branding satisfies the contract"}}
}
func prepareFile(t *testing.T, f *testrepo.Fixture, action string, o exceptions.Options) *exceptions.Proposal {
	t.Helper()
	r, c := f.Open()
	p, e := exceptions.Prepare(r, c, exceptions.Action(action), []string{"brand"}, o)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func writeFile(t *testing.T, f *testrepo.Fixture, p *exceptions.Proposal) {
	t.Helper()
	r, c := f.Open()
	if e := exceptions.Write(r, c, p); e != nil {
		t.Fatal(e)
	}
}
func requireFileStatus(t *testing.T, f *testrepo.Fixture, status string) {
	t.Helper()
	r, c := f.Open()
	v, e := explain(r, c, "brand", "")
	if e != nil || v.Status != status {
		t.Fatal(v, e)
	}
	rep, e := analysis.Analyze(r, c, analysis.Options{Paths: []string{asset}})
	if e != nil {
		t.Fatal(e)
	}
	if status == "valid" && (len(rep.Findings) != 0 || rep.Files[0].Classification != "excepted") {
		t.Fatal(rep.Findings, rep.Files)
	}
	if status != "valid" && len(rep.Findings) == 0 {
		t.Fatal("invalid exception hid findings")
	}
}
func removeAsset(t *testing.T, f *testrepo.Fixture) {
	t.Helper()
	if e := os.Remove(filepath.Join(f.Root, asset)); e != nil {
		t.Fatal(e)
	}
}

func TestFileExceptionLifecycle(t *testing.T) {
	f := fileFixture(t, oldAsset)
	f.Write(asset, newAsset)
	index := f.Git("ls-files", "--stage")
	before, _ := os.ReadFile(filepath.Join(f.Root, policy.File))
	p := prepareFile(t, f, "record", fileOptions())
	after, _ := os.ReadFile(filepath.Join(f.Root, policy.File))
	if !bytes.Equal(before, after) || p.Status != "preview" || p.Changes[0].Next.File == nil || strings.Contains(p.Changes[0].SourceDiff, "\x00") {
		t.Fatal("bad preview", p)
	}
	writeFile(t, f, p)
	requireFileStatus(t, f, "valid")
	f.Write(asset, "\x00changed asset")
	requireFileStatus(t, f, "stale")
	p = prepareFile(t, f, "refresh", exceptions.Options{})
	if p.Changes[0].Next.File.Replacement.SHA256 == p.Changes[0].Previous[0].File.Replacement.SHA256 {
		t.Fatal(p)
	}
	writeFile(t, f, p)
	requireFileStatus(t, f, "valid")
	f.Write(asset, oldAsset)
	requireFileStatus(t, f, "obsolete")
	p = prepareFile(t, f, "refresh", exceptions.Options{})
	if p.Changes[0].Action != "remove" || p.Changes[0].Next != nil {
		t.Fatal(p)
	}
	writeFile(t, f, p)
	_, c := f.Open()
	if len(c.Exceptions) != 0 || f.Git("ls-files", "--stage") != index {
		t.Fatal("exception or index remains", c.Exceptions)
	}
}
func TestFileRetirementAndRecreation(t *testing.T) {
	for _, original := range []string{oldAsset, "export const retired = true;\n", ""} {
		t.Run(repository.Digest([]byte(original))[:8], func(t *testing.T) {
			f := fileFixture(t, original)
			removeAsset(t, f)
			p := prepareFile(t, f, "record", fileOptions())
			if p.Changes[0].Next.File.Replacement != nil || !strings.Contains(p.Changes[0].SourceDiff, "absent (retirement)") {
				t.Fatal(p)
			}
			if original != "" && repository.Text([]byte(original)) && !strings.Contains(p.Changes[0].SourceDiff, "-export const retired") {
				t.Fatal("missing retirement diff", p.Changes[0].SourceDiff)
			}
			writeFile(t, f, p)
			requireFileStatus(t, f, "valid")
			f.Write(asset, "\x00new file")
			requireFileStatus(t, f, "stale")
			r, c := f.Open()
			if _, e := exceptions.Prepare(r, c, "refresh", []string{"brand"}, exceptions.Options{}); e == nil {
				t.Fatal("retirement silently changed")
			}
			f.Write(asset, original)
			requireFileStatus(t, f, "obsolete")
			p = prepareFile(t, f, "refresh", exceptions.Options{})
			if p.Changes[0].Action != "remove" {
				t.Fatal(p)
			}
			writeFile(t, f, p)
		})
	}
}
func TestFileExceptionScopeAndInputRejections(t *testing.T) {
	for _, kind := range []string{"text", "binary to text", "text to binary", "mode", "symlink", "directory", "oversize", "upstream oversize", "exact", "unknown", "unsafe", "missing path", "hunks", "replace", "span conversion", "no change", "missing reference"} {
		t.Run(kind, func(t *testing.T) {
			original := oldAsset
			if kind == "text" || kind == "text to binary" {
				original = "text\n"
			}
			if kind == "upstream oversize" {
				original = strings.Repeat("x", repository.MaxBytes+1)
			}
			f := fileFixture(t, original)
			f.Write(asset, newAsset)
			o := fileOptions()
			switch kind {
			case "text", "binary to text":
				f.Write(asset, "changed text\n")
			case "mode":
				if e := os.Chmod(filepath.Join(f.Root, asset), 0755); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				removeAsset(t, f)
				if e := os.Symlink("lib.rs", filepath.Join(f.Root, asset)); e != nil {
					t.Fatal(e)
				}
			case "directory":
				removeAsset(t, f)
				if e := os.Mkdir(filepath.Join(f.Root, asset), 0755); e != nil {
					t.Fatal(e)
				}
			case "oversize":
				f.Write(asset, strings.Repeat("x", repository.MaxBytes+1))
			case "exact":
				f.Config.PreserveExactly = append(f.Config.PreserveExactly, asset)
				f.Save()
			case "unknown":
				o.Path = "crates/core/unknown.png"
			case "unsafe":
				o.Path = "../unknown.png"
			case "missing path":
				o.Path = ""
			case "hunks":
				o.Hunks = []string{"unselected"}
			case "replace":
				o.Replace = []string{"span"}
			case "span conversion":
				f.Config.Exceptions = []policy.Exception{{ID: "brand", Path: asset, Start: 0, End: 1, UpstreamSHA256: repository.Digest([]byte(original)), ReplacementSHA256: repository.Digest([]byte(newAsset)), Reason: o.Metadata.Why, Witnesses: o.Metadata.Witnesses}}
				f.Save()
			case "no change":
				f.Write(asset, original)
			case "missing reference":
				o.Metadata.Witnesses = []string{"missing.rs"}
			}
			r, c := f.Open()
			action := "record"
			if kind == "span conversion" {
				action = "refresh"
			}
			if _, e := exceptions.Prepare(r, c, exceptions.Action(action), []string{"brand"}, o); e == nil {
				t.Fatal("invalid file binding accepted", kind)
			}
		})
	}
}
func TestFileWriteBindsAllObservedInputs(t *testing.T) {
	for _, kind := range []string{"source", "retired recreated", "reference", "policy", "proposal"} {
		t.Run(kind, func(t *testing.T) {
			f := fileFixture(t, oldAsset)
			f.Write(asset, newAsset)
			if kind == "retired recreated" {
				removeAsset(t, f)
			}
			r, c := f.Open()
			p := prepareFile(t, f, "record", fileOptions())
			switch kind {
			case "source", "retired recreated":
				f.Write(asset, "\x00peer asset")
			case "reference":
				f.Write(path, "fn changed_witness() {}\n")
			case "policy":
				f.Config.Families[0].Title += " (edited)"
				f.Save()
			case "proposal":
				p.Changes[0].Next.File.Upstream.SHA256 = repository.Digest([]byte("tampered"))
			}
			before := policyBytes(t, f)
			if e := exceptions.Write(r, c, p); e == nil {
				t.Fatal("stale file proposal applied")
			}
			if after := policyBytes(t, f); !bytes.Equal(before, after) {
				t.Fatal("policy changed on refusal")
			}
		})
	}
}
func TestFileRefreshRequiresExplicitUpstreamAndRetirementReview(t *testing.T) {
	for _, kind := range []string{"upstream", "retirement"} {
		t.Run(kind, func(t *testing.T) {
			f := fileFixture(t, oldAsset)
			f.Write(asset, newAsset)
			writeFile(t, f, prepareFile(t, f, "record", fileOptions()))
			_, f.Config = f.Open()
			if kind == "upstream" {
				f.Write(asset, "\x00new upstream")
				f.Config.Upstream = f.Commit()
				f.Save()
				f.Commit()
				f.Write(asset, newAsset)
			} else {
				removeAsset(t, f)
			}
			r, c := f.Open()
			if _, e := exceptions.Prepare(r, c, "refresh", []string{"brand"}, exceptions.Options{}); e == nil {
				t.Fatal("changed meaning silently accepted")
			}
			p := prepareFile(t, f, "refresh", exceptions.Options{File: true})
			writeFile(t, f, p)
			requireFileStatus(t, f, "valid")
		})
	}
}
func TestCommittedFileExceptionAndReferenceSnapshot(t *testing.T) {
	f := fileFixture(t, oldAsset)
	f.Write(asset, newAsset)
	writeFile(t, f, prepareFile(t, f, "record", fileOptions()))
	rev := f.Commit()
	f.Write(asset, "\x00dirty asset")
	if err := os.Remove(filepath.Join(f.Root, path)); err != nil {
		t.Fatal(err)
	}
	r, _ := f.Open()
	c, e := policy.Load(r, rev)
	if e != nil {
		t.Fatal(e)
	}
	v, e := explain(r, c, "brand", rev)
	if e != nil || v.Status != "valid" {
		t.Fatal(v, e)
	}
	// Restoring the reviewed asset cannot hide an invalid reference.
	f.Write(asset, newAsset)
	requireFileStatus(t, f, "stale")
}
