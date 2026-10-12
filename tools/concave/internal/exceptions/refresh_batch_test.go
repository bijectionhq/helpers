package exceptions_test

import (
	"bytes"
	"concave/internal/exceptions"
	"concave/internal/repository"
	"strings"
	"testing"
)

func TestRefreshBatchRemovesRestoredAndRefreshesChangedAtomically(t *testing.T) {
	f, sources := batchFixture(t, true)
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha", "beta"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	f.Commit()
	f.Write(path, "fn first() {}\nfn middle() {}\nfn second() {}\nfn last() { unrelated(); }\n")
	f.Write("npm-packages/convex/index.ts", strings.Replace(sources["npm-packages/convex/index.ts"], "= 2", "= 3", 1))
	r, c = f.Open()
	p, err = exceptions.Prepare(r, c, "refresh", []string{"beta", "alpha"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 2 || p.Changes[0].Action != "remove" || p.Changes[1].PreviousSource.Status != "recovered" || !strings.Contains(p.Changes[1].PreviousSource.Diff, "= 2") {
		t.Fatalf("%+v", p)
	}
	before := policyBytes(t, f)
	f.Write("npm-packages/convex/index.ts", sources["npm-packages/convex/index.ts"])
	if err = exceptions.Write(r, c, p); err == nil || !bytes.Equal(before, policyBytes(t, f)) {
		t.Fatal("partial/stale batch applied", err)
	}
	f.Write("npm-packages/convex/index.ts", strings.Replace(sources["npm-packages/convex/index.ts"], "= 2", "= 3", 1))
	if err = exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	_, c = f.Open()
	if len(c.Exceptions) != 1 || c.Exceptions[0].ID != "beta" {
		t.Fatal(c.Exceptions)
	}
}

func TestPreviousApprovedBytesUnavailableWithoutHistory(t *testing.T) {
	f := fixture(t)
	record(t, f)
	f.Write(path, fenced("fn first() { changed(); }\n")+"fn second() {}\n")
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, "refresh", []string{"read"}, exceptions.Options{})
	if err != nil || p.Changes[0].PreviousSource.Status != "unavailable" {
		t.Fatal(p, err)
	}
}

// An unchanged proposal still verifies its inputs, yet HEAD is not one of them.
func TestUnchangedProposalIgnoresHead(t *testing.T) {
	fixture := fixture(t)
	record(t, fixture)
	repo, config := fixture.Open()
	proposal, err := exceptions.Prepare(repo, config, exceptions.Refresh, []string{"read"}, exceptions.Options{})
	if err != nil || proposal.Status != exceptions.Unchanged {
		t.Fatal(proposal, err)
	}
	fixture.Write("peer.txt", "advance")
	fixture.Commit()
	if err := exceptions.Write(repo, config, proposal); err != nil {
		t.Fatal("HEAD movement refused an unchanged proposal:", err)
	}
	fixture.Write(path, "peer\n")
	if err := exceptions.Write(repo, config, proposal); !repository.IsStale(err) {
		t.Fatal("unchanged proposal ignored a changed source:", err)
	}
}
