package repository_test

import (
	"concave/internal/testrepo"
	"context"
	"testing"
)

func TestScopedContextPreservesParentAndImmutableBasis(t *testing.T) {
	fixture := testrepo.New(t)
	parent, _ := fixture.Open()
	view, err := parent.At(fixture.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	ctx, cancel := context.WithCancel(parent.Context)
	child := view.WithContext(ctx)
	_, data, err := child.Snapshot("crates/core/lib.rs")
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
	cancel()
	child.Close()
	child.Close()
	fixture.Write("crates/core/lib.rs", "peer changes\n")
	_, again, err := view.Snapshot("crates/core/lib.rs")
	if err != nil || string(again) != string(data) || parent.Context.Err() != nil {
		t.Fatal("scope changed its caller", err, string(again))
	}
	if view.Reads != 2 {
		t.Fatal("scoped observations lost or counted twice", view.Reads)
	}
}
