package repository_test

import (
	"testing"

	"concave/internal/testrepo"
)

// Git vouches only for unflagged stage-0 entries its stat check finds clean,
// whose stored bytes no conversion can change; everything else must be read.
func TestUnchangedIndexVouchesOnlyForUnconvertedCleanEntries(t *testing.T) {
	fixture := testrepo.New(t)
	fixture.Write(".gitattributes", "filtered.txt filter=custom\nbinary.dat -text\n")
	for _, name := range []string{"clean.txt", "racy.txt", "assumed.txt", "filtered.txt", "binary.dat"} {
		fixture.Write(name, "original\n")
	}
	fixture.Commit()
	// Same size, written immediately after indexing: only a content
	// comparison, which Git performs for racily clean entries, can tell.
	fixture.Write("racy.txt", "modified\n")
	fixture.Git("update-index", "--assume-unchanged", "assumed.txt")
	fixture.Write("assumed.txt", "changed!\n")
	repo, _ := fixture.Open()
	entries, err := repo.UnchangedIndex()
	if err != nil {
		t.Fatal(err)
	}
	clean, ok := entries["clean.txt"]
	if !ok || clean.OID != fixture.Git("hash-object", "clean.txt") || clean.Mode != "100644" {
		t.Fatal("clean entry not vouched for", clean, ok)
	}
	if _, ok := entries["binary.dat"]; !ok {
		t.Fatal("an unconverted binary entry was not vouched for")
	}
	for _, name := range []string{"racy.txt", "assumed.txt", "filtered.txt"} {
		if _, ok := entries[name]; ok {
			t.Fatal("vouched for", name)
		}
	}
	// A conversion setting for the whole checkout vouches for nothing.
	fixture.Git("config", "core.autocrlf", "input")
	if entries, err = repo.UnchangedIndex(); err != nil || len(entries) != 0 {
		t.Fatal(len(entries), err)
	}
}
