package repository_test

import (
	"reflect"
	"testing"

	"concave/internal/testrepo"
)

func TestRevisionViewIgnoresWorkingEditsAndHeadAdvancement(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	head, err := repo.Resolve("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	view, err := repo.At(head)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	want, data, err := repo.Snapshot("crates/core/lib.rs")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := view.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	f.Write("crates/core/lib.rs", "peer changes\n")
	f.Write("crates/core/new.rs", "peer file\n")
	f.Commit()
	f.Write("another.rs", "untracked peer file\n")
	state, actual, err := view.Snapshot("crates/core/lib.rs")
	if err != nil || !reflect.DeepEqual(want, state) || string(data) != string(actual) {
		t.Fatal(state, string(actual), err)
	}
	current, err := view.Resolve("HEAD")
	if err != nil || current != head {
		t.Fatal(current, err)
	}
	next, err := view.Inventory()
	if err != nil || !reflect.DeepEqual(paths, next) {
		t.Fatal(next, err)
	}
	if untracked, err := view.Untracked(); err != nil || len(untracked) != 0 {
		t.Fatal(untracked, err)
	}
	if state, _, err := view.Snapshot("crates/core/new.rs"); err != nil || state != nil {
		t.Fatal(state, err)
	}
}
