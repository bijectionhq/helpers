package review_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestContributionReferenceRefusesConcurrentRevisions(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	root := &review.Contribution{Format: review.ContributionFormat}
	if _, err := review.Store(repo, root); err != nil {
		t.Fatal(err)
	}
	first := *root
	first.Parent, first.Origin, first.Selected = root.ID, root.ID, []string{"first"}
	if _, err := review.Store(repo, &first); err != nil {
		t.Fatal(err)
	}
	got, err := review.Read(repo, root.Reference())
	if err != nil || got.ID != first.ID {
		t.Fatal(got, err)
	}
	second := *root
	second.Parent, second.Origin, second.Selected = root.ID, root.ID, []string{"second"}
	if _, err := review.Store(repo, &second); err != nil {
		t.Fatal(err)
	}
	if _, err := review.Read(repo, root.Reference()); err == nil || !strings.Contains(err.Error(), "concurrent revisions") {
		t.Fatal(err)
	}
	exact, err := review.Read(repo, first.ID)
	if err != nil || exact.ID != first.ID {
		t.Fatal(exact, err)
	}
}

func TestContributionLookupIgnoresUnrelatedArtifacts(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	root := &review.Contribution{Format: review.ContributionFormat}
	if _, err := review.Store(repo, root); err != nil {
		t.Fatal(err)
	}
	other := &review.Contribution{Format: review.ContributionFormat, Selected: []string{"unrelated"}}
	path, err := review.Store(repo, other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted unrelated record"), 0600); err != nil {
		t.Fatal(err)
	}
	// Old flat archives are not part of the current store's lookup boundary.
	if err := os.WriteFile(filepath.Join(f.Root, "target/concave/reviews", "r-"+strings.Repeat("e", 64)+".json"), []byte("old unrelated archive"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{root.ID, root.ID[:10], root.Reference(), "@" + root.ID[:10]} {
		got, err := review.Read(repo, ref)
		if err != nil || got.ID != root.ID {
			t.Fatal(ref, got, err)
		}
	}
	if _, err := review.Read(repo, other.Reference()); err == nil {
		t.Fatal("related corruption accepted")
	}
}
