package recovery_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

type fixture struct {
	f      *testrepo.Fixture
	r      *repository.Repo
	c      *policy.Config
	report *analysis.Report
	raw    []byte
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := testrepo.New(t)
	f.Write("crates/core/lib.rs", "first fork\nfn second() {}\nthird fork\n")
	ref := f.Commit()
	r, c := f.Open()
	rep, e := analysis.Analyze(r, c, analysis.Options{Rev: ref, Case: "authentication", Diagnose: true})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(rep)
	return &fixture{f, r, c, rep, raw}
}
func (x *fixture) plan(t *testing.T, ids ...string) *recovery.Plan {
	t.Helper()
	if len(ids) == 0 {
		ids = []string{x.report.Hunks[0].ID}
	}
	p, e := recovery.Prepare(x.r, x.c, x.report, x.raw, ids, nil, "restore reviewed behavior")
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestRecoveryOneHunkPreservesOtherAndIndex(t *testing.T) {
	x := setup(t)
	index := x.f.Git("ls-files", "--stage")
	p := x.plan(t)
	if len(p.Findings) != 1 {
		t.Fatalf("preview lost remaining difference: %+v", p.Findings)
	}
	if e := recovery.Apply(x.r, x.c, p); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
	if !strings.Contains(string(b), "fn first() {}") || !strings.Contains(string(b), "third fork") {
		t.Fatal(string(b))
	}
	if x.f.Git("ls-files", "--stage") != index {
		t.Fatal("index changed")
	}
	if e := recovery.Apply(x.r, x.c, p); e == nil {
		t.Fatal("repeat accepted")
	}
}
func TestMultipleHunksRestoreExactOriginal(t *testing.T) {
	x := setup(t)
	ids := []string{}
	for _, h := range x.report.Hunks {
		ids = append(ids, h.ID)
	}
	p := x.plan(t, ids...)
	if e := recovery.Apply(x.r, x.c, p); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
	if string(b) != "fn first() {}\nfn second() {}\n" {
		t.Fatal(string(b))
	}
}
func TestManualPatchPreservesPeerChanges(t *testing.T) {
	x := setup(t)
	x.f.Write("packaging/facade.ts", "peer line\nexport const audience = 'bijection';\n")
	rep, e := analysis.Analyze(x.r, x.c, analysis.Options{Rev: x.report.Revision, Case: "authentication", Diagnose: true})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(rep)
	patch := recovery.SinglePatch("packaging/facade.ts", []byte("peer line\nexport const audience = 'bijection';\n"), []byte("peer line\nexport const audience = 'reviewed';\n"))
	p, e := recovery.Prepare(x.r, x.c, rep, raw, nil, patch, "boundary")
	if e != nil {
		t.Fatal(e)
	}
	if e = recovery.Apply(x.r, x.c, p); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(x.f.Root, "packaging/facade.ts"))
	if !bytes.HasPrefix(b, []byte("peer line\n")) {
		t.Fatal(string(b))
	}
}
func TestStalePreimageRefusesAllFiles(t *testing.T) {
	x := setup(t)
	first := recovery.SinglePatch("crates/core/lib.rs", []byte("first fork\nfn second() {}\nthird fork\n"), []byte("restored\n"))
	second := recovery.SinglePatch("packaging/facade.ts", []byte("export const audience = 'bijection';\n"), []byte("changed\n"))
	p, e := recovery.Prepare(x.r, x.c, x.report, x.raw, nil, append(first, second...), "two files")
	if e != nil {
		t.Fatal(e)
	}
	if err := recovery.Validate(x.r, x.c, p); err != nil {
		t.Fatal(err)
	}
	x.f.Write("packaging/facade.ts", "peer edit\n")
	if e = recovery.Apply(x.r, x.c, p); e == nil {
		t.Fatal("stale accepted")
	}
	b, _ := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
	if string(b) != "first fork\nfn second() {}\nthird fork\n" {
		t.Fatal("partial apply")
	}
}
func TestDirtyOrDifferentSelectedSourceRefused(t *testing.T) {
	for _, kind := range []string{"dirty", "advanced head", "selected differs"} {
		t.Run(kind, func(t *testing.T) {
			x := setup(t)
			x.f.Write("crates/core/lib.rs", "peer edit\n")
			if kind != "dirty" {
				x.f.Commit()
			}
			if kind == "selected differs" {
				x.f.Write("crates/core/lib.rs", "first fork\nfn second() {}\nthird fork\n")
				rep, e := analysis.Analyze(x.r, x.c, analysis.Options{Rev: x.report.Revision, Case: "authentication", Diagnose: true})
				if e != nil {
					t.Fatal(e)
				}
				x.report = rep
			}
			if _, e := recovery.Prepare(x.r, x.c, x.report, x.raw, []string{x.report.Hunks[0].ID}, nil, "reason"); e == nil {
				t.Fatal("unsafe selection accepted")
			}
		})
	}
}
func TestHeadAdvanceInvalidatesApplication(t *testing.T) {
	x := setup(t)
	p := x.plan(t)
	x.f.Write("other", "peer")
	x.f.Commit()
	if e := recovery.Apply(x.r, x.c, p); e == nil {
		t.Fatal("advanced HEAD accepted")
	}
}
func TestReportExcerptsCannotInjectCode(t *testing.T) {
	x := setup(t)
	x.report.Hunks[0].Before = "injected\n"
	p := x.plan(t)
	if strings.Contains(p.Patch, "injected") {
		t.Fatal("trusted report excerpts")
	}
}
func TestPatchTamperingRefused(t *testing.T) {
	for _, kind := range []string{"bytes", "effects", "policy"} {
		t.Run(kind, func(t *testing.T) {
			x := setup(t)
			p := x.plan(t)
			switch kind {
			case "bytes":
				p.Patch += "tampered"
			case "effects":
				for k, v := range p.Files {
					v.After.SHA256 = "wrong"
					p.Files[k] = v
				}
			case "policy":
				p.PolicySHA256 = "wrong"
			}
			if e := recovery.Apply(x.r, x.c, p); e == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
}
func TestExplicitUniqueHunksOnly(t *testing.T) {
	x := setup(t)
	id := x.report.Hunks[0].ID
	for _, ids := range [][]string{nil, {"unknown"}, {id, id}} {
		if _, e := recovery.Prepare(x.r, x.c, x.report, x.raw, ids, nil, "reason"); e == nil {
			t.Fatal("selection accepted", ids)
		}
	}
	if _, e := recovery.Prepare(x.r, x.c, x.report, x.raw, []string{id}, []byte("manual"), "reason"); e == nil {
		t.Fatal("mixed selection accepted")
	}
}
func TestManualPatchOutsideScopeRefused(t *testing.T) {
	x := setup(t)
	patch := recovery.SinglePatch("npm-packages/convex/index.ts", []byte("export const value = 1;\n"), []byte("changed\n"))
	if _, e := recovery.Prepare(x.r, x.c, x.report, x.raw, nil, patch, "outside"); e == nil {
		t.Fatal("scope escaped")
	}
}
func TestUnsupportedRecovery(t *testing.T) {
	for _, kind := range []string{"new", "delete", "mode", "binary", "oversize", "symlink", "parent symlink", "escape"} {
		t.Run(kind, func(t *testing.T) {
			x := setup(t)
			p := "packaging/facade.ts"
			before := []byte("export const audience = 'bijection';\n")
			patch := recovery.SinglePatch(p, before, []byte("changed\n"))
			switch kind {
			case "new":
				patch = []byte("--- /dev/null\n+++ b/new.ts\n@@ -0,0 +1 @@\n+new\n")
			case "delete":
				patch = []byte("--- a/packaging/facade.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n-export const audience = 'bijection';\n")
			case "mode":
				patch = []byte("diff --git a/packaging/facade.ts b/packaging/facade.ts\nold mode 100644\nnew mode 100755\n")
			case "binary":
				x.f.Write(p, "\x00binary")
			case "oversize":
				x.f.Write(p, strings.Repeat("x", repository.MaxBytes+1))
			case "symlink":
				os.Remove(filepath.Join(x.f.Root, p))
				os.Symlink(filepath.Join(x.f.Root, "crates/core/lib.rs"), filepath.Join(x.f.Root, p))
			case "parent symlink":
				os.Symlink("packaging", filepath.Join(x.f.Root, "linked"))
				patch = recovery.SinglePatch("linked/facade.ts", before, []byte("changed\n"))
			case "escape":
				patch = recovery.SinglePatch("../outside", before, []byte("changed\n"))
			}
			if _, e := recovery.CheckedPatch(x.r, patch, nil, nil); e == nil {
				t.Fatal("unsupported patch accepted")
			}
		})
	}
}
func TestUnicodeAndNoFinalNewline(t *testing.T) {
	x := setup(t)
	p := "crates/core/café file.rs"
	x.f.Write(p, "old")
	states, e := recovery.CheckedPatch(x.r, recovery.SinglePatch(p, []byte("old"), []byte("new")), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if states[p].After.SHA256 != repository.Digest([]byte("new")) {
		t.Fatal(states)
	}
}
func TestOutputLocation(t *testing.T) {
	x := setup(t)
	if _, e := x.r.OutputDirectory(filepath.Join(x.f.Root, "tracked-report")); e == nil {
		t.Fatal("unignored output accepted")
	}
	p := filepath.Join(x.f.Root, "target/report")
	if _, e := x.r.OutputDirectory(p); e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.OutputDirectory(p); e == nil {
		t.Fatal("output overwritten")
	}
}

func workingSetup(t *testing.T) *fixture {
	t.Helper()
	x := setup(t)
	// Both the first hunk and the unrelated tail differ from committed HEAD.
	x.f.Write("crates/core/lib.rs", "working first\nfn second() {}\npeer tail\n")
	report, err := analysis.Analyze(x.r, x.c, analysis.Options{Case: "authentication", Diagnose: true})
	if err != nil {
		t.Fatal(err)
	}
	x.report = report
	x.raw, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestWorkingRecoveryPreservesUnselectedChanges(t *testing.T) {
	x := workingSetup(t)
	index := x.f.Git("ls-files", "--stage")
	head := x.f.Git("rev-parse", "HEAD")
	// A report is a locator. Never apply code copied from its excerpts.
	x.report.Hunks[0].Before = "injected code\n"
	p := x.plan(t)
	if p.Revision != "" || len(p.SelectedHunks) != 1 || strings.Contains(p.Patch, "injected") {
		t.Fatal(p)
	}
	// Changes to other files need not block the selected, unchanged preimage.
	x.f.Write("packaging/facade.ts", "unrelated peer edit\n")
	if err := recovery.Apply(x.r, x.c, p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
	if string(got) != "fn first() {}\nfn second() {}\npeer tail\n" {
		t.Fatal(string(got))
	}
	other, _ := os.ReadFile(filepath.Join(x.f.Root, "packaging/facade.ts"))
	if string(other) != "unrelated peer edit\n" {
		t.Fatal(string(other))
	}
	if index != x.f.Git("ls-files", "--stage") || head != x.f.Git("rev-parse", "HEAD") {
		t.Fatal("recovery changed index or HEAD")
	}
	if err := recovery.Apply(x.r, x.c, p); err == nil {
		t.Fatal("repeated application accepted")
	}
}

func TestWorkingRecoveryRefusesStaleSelection(t *testing.T) {
	for _, kind := range []string{"selected hunk", "other hunk", "mode", "deleted", "head", "policy"} {
		t.Run(kind, func(t *testing.T) {
			x := workingSetup(t)
			id := x.report.Hunks[0].ID
			switch kind {
			case "selected hunk":
				x.f.Write("crates/core/lib.rs", "new first\nfn second() {}\npeer tail\n")
			case "other hunk":
				x.f.Write("crates/core/lib.rs", "working first\nfn second() {}\nchanged peer tail\n")
			case "mode":
				if err := os.Chmod(filepath.Join(x.f.Root, "crates/core/lib.rs"), 0700); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := os.Remove(filepath.Join(x.f.Root, "crates/core/lib.rs")); err != nil {
					t.Fatal(err)
				}
			case "head":
				x.f.Commit()
			case "policy":
				x.c.SHA256 = "changed"
			}
			if _, err := recovery.Prepare(x.r, x.c, x.report, x.raw, []string{id}, nil, "restore first"); err == nil {
				t.Fatal("stale working report accepted")
			}
		})
	}
}

func TestWorkingRecoveryValidatesEveryPreimageBeforeApplying(t *testing.T) {
	x := workingSetup(t)
	x.f.Write("npm-packages/convex/index.ts", "export const value = 2;\n")
	report, err := analysis.Analyze(x.r, x.c, analysis.Options{Diagnose: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	ids := []string{}
	for _, h := range report.Hunks {
		if h.Path == "npm-packages/convex/index.ts" || h.Path == "crates/core/lib.rs" && h.Start == 0 {
			ids = append(ids, h.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatal(ids)
	}
	p, err := recovery.Prepare(x.r, x.c, report, raw, ids, nil, "recover two selected hunks")
	if err != nil {
		t.Fatal(err)
	}
	x.f.Write("npm-packages/convex/index.ts", "peer edit after plan\n")
	if err := recovery.Apply(x.r, x.c, p); err == nil {
		t.Fatal("stale second file accepted")
	}
	got, _ := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
	if string(got) != "working first\nfn second() {}\npeer tail\n" {
		t.Fatal("partially applied", string(got))
	}
}

func TestWorkingRecoveryInsertionDeletionAndNoFinalNewline(t *testing.T) {
	for _, source := range []string{
		"header\nfn first() {}\nfn second() {}\n",
		"fn first() {}\n",
		"replacement\nfn second() {}\nfooter",
	} {
		t.Run(source, func(t *testing.T) {
			x := workingSetup(t)
			x.f.Write("crates/core/lib.rs", source)
			report, err := analysis.Analyze(x.r, x.c, analysis.Options{Paths: []string{"crates/core/lib.rs"}})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(report)
			ids := []string{}
			for _, h := range report.Hunks {
				ids = append(ids, h.ID)
			}
			p, err := recovery.Prepare(x.r, x.c, report, raw, ids, nil, "recover explicitly selected differences")
			if err != nil {
				t.Fatal(err)
			}
			if err = recovery.Apply(x.r, x.c, p); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
			if string(got) != "fn first() {}\nfn second() {}\n" {
				t.Fatal(string(got))
			}
		})
	}
}

func TestRecoveryGitProcessCountDoesNotGrowPerSelectedHunk(t *testing.T) {
	counts := []int{}
	for _, all := range []bool{false, true} {
		x := workingSetup(t)
		ids := []string{x.report.Hunks[0].ID}
		if all {
			if len(x.report.Hunks) != 2 {
				t.Fatal(x.report.Hunks)
			}
			ids = append(ids, x.report.Hunks[1].ID)
		}
		before := x.r.Processes
		x.plan(t, ids...)
		counts = append(counts, x.r.Processes-before)
	}
	if counts[1] > counts[0] {
		t.Fatal("Git process count grows with hunks", counts)
	}
}

func TestNewGitConversionRefusesPreparedRecovery(t *testing.T) {
	for _, attribute := range []string{"filter=late", "ident", "working-tree-encoding=UTF-16"} {
		t.Run(attribute, func(t *testing.T) {
			x := setup(t)
			p := x.plan(t)
			x.f.Write(".gitattributes", "*.rs "+attribute+"\n")
			before, err := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
			if err != nil {
				t.Fatal(err)
			}
			index := x.f.Git("ls-files", "--stage")
			err = recovery.Apply(x.r, x.c, p)
			if err == nil || !strings.Contains(err.Error(), "recovery refuses Git content conversion") {
				t.Fatal("late conversion was not refused", err)
			}
			after, err := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
			if err != nil || !bytes.Equal(before, after) || x.f.Git("ls-files", "--stage") != index {
				t.Fatal("refused recovery edited source or index", err)
			}
		})
	}
}

// Only a declared deletion may remove a file; its plan applies exactly.
func TestDeclaredDeletionApplies(t *testing.T) {
	x := setup(t)
	p := "packaging/facade.ts"
	before := []byte("export const audience = 'bijection';\n")
	patch := recovery.FilePatch(p, before, true, nil, false)
	if _, e := recovery.CheckedPatch(x.r, patch, nil, nil); e == nil {
		t.Fatal("undeclared deletion accepted")
	}
	if _, e := recovery.CheckedPatch(x.r, recovery.SinglePatch(p, before, []byte("changed\n")), nil, []string{p}); e == nil {
		t.Fatal("declared deletion that the patch does not perform accepted")
	}
	effects, e := recovery.CheckedPatch(x.r, patch, nil, []string{p})
	if e != nil || effects[p].Before == nil || effects[p].After != nil {
		t.Fatal(effects, e)
	}
	head, e := x.r.Resolve("HEAD")
	if e != nil {
		t.Fatal(e)
	}
	plan := &recovery.Plan{Format: 1, Kind: "concave-recovery-plan", Head: head, Upstream: x.c.Upstream, PolicySHA256: x.c.SHA256, Reason: "retire", DeletedPaths: []string{p}, Files: effects, Patch: string(patch), PatchSHA256: repository.Digest(patch)}
	if e := recovery.Apply(x.r, x.c, plan); e != nil {
		t.Fatal(e)
	}
	if _, err := os.Stat(filepath.Join(x.f.Root, p)); !os.IsNotExist(err) {
		t.Fatal("declared deletion kept the file", err)
	}
	if e := recovery.ValidateApplied(x.r, x.c, plan); e != nil {
		t.Fatal("applied deletion does not validate:", e)
	}
	fresh := "packaging/new.ts"
	created := recovery.FilePatch(fresh, nil, false, before, true)
	if effects, e := recovery.CheckedPatch(x.r, created, []string{fresh}, nil); e != nil || effects[fresh].After == nil {
		t.Fatal("creation patch", effects, e)
	}
}
