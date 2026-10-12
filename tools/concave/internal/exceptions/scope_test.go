package exceptions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/repository"
)

const beta = "npm-packages/convex/index.ts"

func bindingIDs(t *testing.T, c *policy.Config) []string {
	t.Helper()
	ids := []string{}
	for _, binding := range c.Exceptions {
		ids = append(ids, binding.ID)
	}
	slices.Sort(ids)
	return ids
}

// Approvals of different paths share no file, so they cannot invalidate or
// overwrite each other, even when written at the same time.
func TestConcurrentRecordsOnDifferentPathsNeverConflict(t *testing.T) {
	f, _ := batchFixture(t, true)
	r, c := f.Open()
	proposals := []*exceptions.Proposal{}
	for _, id := range []string{"alpha", "beta"} {
		p, err := exceptions.Prepare(r, c, exceptions.Record, []string{id}, exceptions.Options{})
		if err != nil {
			t.Fatal(err)
		}
		proposals = append(proposals, p)
	}
	if !slices.Equal(proposals[0].BindingPaths, []string{path}) || !slices.Equal(proposals[1].BindingPaths, []string{beta}) {
		t.Fatal("proposal scope is not its own binding files", proposals[0].BindingPaths, proposals[1].BindingPaths)
	}
	var wait sync.WaitGroup
	failures := make([]error, len(proposals))
	for index, proposal := range proposals {
		wait.Add(1)
		go func() {
			defer wait.Done()
			reader, err := repository.Open(context.Background(), f.Root)
			if err != nil {
				failures[index] = err
				return
			}
			defer reader.Close()
			failures[index] = exceptions.Write(reader, c, proposal)
		}()
	}
	wait.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal("concurrent approval of another path refused:", err)
		}
	}
	_, after := f.Open()
	if ids := bindingIDs(t, after); !slices.Equal(ids, []string{"alpha", "beta"}) {
		t.Fatal("concurrent approvals lost a binding", ids)
	}
	for _, source := range []string{path, beta} {
		if _, err := os.Stat(filepath.Join(f.Root, policy.BindingPath(source))); err != nil {
			t.Fatal("binding file missing", err)
		}
	}
}

// A proposal's fingerprint and guarded write cover exactly its own binding
// files: another path's approval leaves both intact, a peer edit of its own
// binding file refuses the write without touching any file.
func TestProposalFingerprintCoversOnlyItsBindingFiles(t *testing.T) {
	f, _ := batchFixture(t, true)
	r, c := f.Open()
	alpha, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := exceptions.Prepare(r, c, exceptions.Record, []string{"beta"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, other); err != nil {
		t.Fatal(err)
	}
	r, c = f.Open()
	again, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha"}, exceptions.Options{})
	if err != nil || again.ProposalSHA256 != alpha.ProposalSHA256 || again.PolicySHA256 != alpha.PolicySHA256 {
		t.Fatal("another path's approval changed this proposal", err)
	}
	// A peer approval of the same path changes this proposal's own input.
	peer := policy.Exception{ID: "peer", Path: path, UpstreamSHA256: alpha.Changes[0].Next.UpstreamSHA256, ReplacementSHA256: alpha.Changes[0].Next.ReplacementSHA256, Start: 3, End: 4, Reason: "peer", Witnesses: []string{"contract.md"}}
	data, err := policy.EncodeBindings(path, []policy.Exception{peer})
	if err != nil {
		t.Fatal(err)
	}
	f.Write(policy.BindingPath(path), string(data))
	before := policyBytes(t, f)
	if err := exceptions.Write(r, c, alpha); !repository.IsStale(err) {
		t.Fatal("peer edit of this path's bindings was not stale:", err)
	}
	if !slices.Equal(before, policyBytes(t, f)) {
		t.Fatal("refused write changed policy")
	}
	if err := os.Remove(filepath.Join(f.Root, policy.BindingPath(path))); err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, alpha); err != nil {
		t.Fatal("restored input still refused:", err)
	}
}

// Neither a moved HEAD nor an unchanged proposal makes a write stale: HEAD is
// not an input of the proposal.
func TestWriteIgnoresHead(t *testing.T) {
	f, _ := batchFixture(t, true)
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.Write("peer.txt", "peer\n")
	f.Commit()
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal("HEAD movement refused an unaffected proposal:", err)
	}
}

func retiredFile(t *testing.T, bindings []policy.Exception) string {
	t.Helper()
	slices.SortFunc(bindings, func(a, b policy.Exception) int {
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	data, err := json.MarshalIndent(map[string]any{"format": 1, "exceptions": bindings}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

// Migration reproduces exactly the bindings and classifications of the
// retired file, and a later migration on top of published binding files
// converts only the paths whose bindings changed locally.
func TestMigrationIsEquivalentAndAppliesOnlyLocalEdits(t *testing.T) {
	f, _ := batchFixture(t, true)
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha", "beta"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	r, recorded := f.Open()
	want, err := analysis.Analyze(r, recorded, analysis.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The pre-migration state: one retired file and no binding files.
	if err := os.RemoveAll(filepath.Join(f.Root, policy.BindingsDirectory)); err != nil {
		t.Fatal(err)
	}
	f.Write(exceptions.RetiredBindings, retiredFile(t, recorded.Exceptions))
	f.Commit()
	r, c = f.Open()
	if len(c.Exceptions) != 0 {
		t.Fatal("policy loading read the retired file")
	}
	migration, err := exceptions.PrepareMigration(r, c)
	if err != nil || len(migration.Written) != 2 || migration.Base != "" {
		t.Fatal(migration, err)
	}
	if err := migration.Write(r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.Root, exceptions.RetiredBindings)); !os.IsNotExist(err) {
		t.Fatal("retired file kept", err)
	}
	r, migrated := f.Open()
	if !reflect.DeepEqual(bindingIDs(t, migrated), bindingIDs(t, recorded)) || migrated.SHA256 != recorded.SHA256 {
		t.Fatal("migration changed the bindings")
	}
	got, err := analysis.Analyze(r, migrated, analysis.Options{})
	if err != nil {
		t.Fatal(err)
	}
	classes := func(report *analysis.Report) map[string]string {
		rows := map[string]string{}
		for _, row := range report.Files {
			rows[row.Path] = row.Classification
		}
		return rows
	}
	if !reflect.DeepEqual(classes(want), classes(got)) || len(want.Findings) != len(got.Findings) {
		t.Fatal("migration changed classifications", classes(want), classes(got))
	}
	if _, err := exceptions.PrepareMigration(r, migrated); err == nil {
		t.Fatal("migration without a retired file accepted")
	}
	// Another checkout publishes the migration while this one still edits the
	// retired file: only its locally edited path is converted.
	retired := f.Commit()
	_ = retired
	local := []policy.Exception{}
	for _, binding := range recorded.Exceptions {
		if binding.ID != "beta" {
			local = append(local, binding)
		}
	}
	f.Write(exceptions.RetiredBindings, retiredFile(t, local))
	r, c = f.Open()
	migration, err = exceptions.PrepareMigration(r, c)
	if err != nil || migration.Base == "" || len(migration.Written) != 0 || !slices.Equal(migration.Deleted, []string{policy.BindingPath(beta)}) || migration.Unchanged != 0 {
		t.Fatalf("%+v %v", migration, err)
	}
	if err := migration.Write(r); err != nil {
		t.Fatal(err)
	}
	_, c = f.Open()
	if ids := bindingIDs(t, c); !slices.Equal(ids, []string{"alpha"}) {
		t.Fatal("local deletion not migrated", ids)
	}
	if _, err := os.Stat(filepath.Join(f.Root, policy.BindingsDirectory, "npm-packages")); !os.IsNotExist(err) {
		t.Fatal("empty binding directories kept", err)
	}
}
