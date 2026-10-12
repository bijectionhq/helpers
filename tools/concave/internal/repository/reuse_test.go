package repository_test

import (
	"concave/internal/repository"
	"concave/internal/testrepo"
	"testing"
)

func TestImmutableTreeReuseKeepsRefsLiveAndMapsIndependent(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	head, err := repo.Resolve("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.Tree(head)
	if err != nil {
		t.Fatal(err)
	}
	processes := repo.Processes
	delete(tree, "crates/core/lib.rs")
	again, err := repo.Tree(head)
	if err != nil || again["crates/core/lib.rs"].OID == "" || repo.Processes != processes {
		t.Fatal("immutable tree was reread or mutated", err)
	}
	f.Write("new.txt", "new")
	f.Commit()
	next, err := repo.Tree("HEAD")
	if err != nil || next["new.txt"].OID == "" {
		t.Fatal("mutable ref was cached", err)
	}
	old, err := repo.Tree(head)
	if err != nil || old["new.txt"].OID != "" {
		t.Fatal("old tree changed", err)
	}
}

func TestImmutableBlobReuseDoesNotShareMutableBytes(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	tree, err := repo.Tree("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	oid := tree["crates/core/lib.rs"].OID
	first, err := repo.Blob(oid)
	if err != nil {
		t.Fatal(err)
	}
	want, reads := string(first), repo.BlobReads
	first[0] = 'X'
	second, err := repo.Blob(oid)
	if err != nil || string(second) != want || repo.BlobReads != reads {
		t.Fatal("immutable blob reread or caller mutation escaped", err)
	}
	f.Write("crates/core/lib.rs", "fn new_source() {}\n")
	f.Commit()
	next, err := repo.Tree("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := repo.Blob(next["crates/core/lib.rs"].OID)
	if err != nil || string(changed) == want || repo.BlobReads != reads+1 {
		t.Fatal("different blob reused stale content", err)
	}
}

func TestSnapshotReusesBytesButFreshnessReadsLiveSource(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	snapshot, err := repository.OpenSnapshot(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	original, err := snapshot.Read("crates/core/lib.rs")
	if err != nil {
		t.Fatal(err)
	}
	reads := repo.Reads
	want := string(original)
	original[0] = 'X'
	again, err := snapshot.Read("crates/core/lib.rs")
	if err != nil || string(again) != want || repo.Reads != reads {
		t.Fatal("source reread or mutable cache", err)
	}
	f.Write("crates/core/lib.rs", "changed")
	captured, err := snapshot.Read("crates/core/lib.rs")
	if err != nil || string(captured) != want {
		t.Fatal("snapshot bytes drifted", err)
	}
	if _, err = snapshot.Verify(); err == nil {
		t.Fatal("cached read hid live change")
	}
}
