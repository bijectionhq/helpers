package repository_test

import (
	"os"
	"path/filepath"
	"testing"

	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestVerificationInputsBindLargeFixtureWithoutRetainingText(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	filename := filepath.Join(f.Root, "fixture.bin")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(repository.MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	inputs, err := repo.CaptureInputs([]string{"fixture.bin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inputs["fixture.bin"].Kind != "oversize" || inputs["fixture.bin"].OID == "" {
		t.Fatal(inputs)
	}
	if err = inputs.Verify(repo, repository.SourceInputs); err != nil {
		t.Fatal(err)
	}
	file, err = os.OpenFile(filename, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte{1}, 0); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err = inputs.Verify(repo, repository.SourceInputs); !repository.IsStale(err) {
		t.Fatal("changed large input accepted", err)
	}
}

// An oversized file read from a revision and the same bytes materialized in a
// checkout have one state: a candidate checkout's inputs verify against it.
func TestOversizeRevisionStateEqualsItsWorkingFile(t *testing.T) {
	f := testrepo.New(t)
	filename := filepath.Join(f.Root, "fixture.bin")
	if err := os.WriteFile(filename, make([]byte, repository.MaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	head := f.Commit()
	repo, _ := f.Open()
	working, _, err := repo.Snapshot("fixture.bin")
	if err != nil {
		t.Fatal(err)
	}
	view, err := repo.At(head)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	revision, _, err := view.Snapshot("fixture.bin")
	if err != nil || revision.Kind != "oversize" || revision.Size != repository.MaxBytes+1 || !repository.Same(revision, working) {
		t.Fatal(revision, working, err)
	}
	// Without a preceding read, the size still comes from Git.
	entries, err := repo.Tree(head)
	if err != nil {
		t.Fatal(err)
	}
	unread, err := repo.BlobState(entries["fixture.bin"], nil)
	if err != nil || !repository.Same(unread, working) {
		t.Fatal(unread, working, err)
	}
}
