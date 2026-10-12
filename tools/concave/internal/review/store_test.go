package review_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestReadDistinguishesUnsupportedFormatFromCorruptedReview(t *testing.T) {
	fixture := testrepo.New(t)
	repo, _ := fixture.Open()
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(`{"format":2,"obsolete_field":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := review.Read(repo, path); err == nil || !strings.Contains(err.Error(), "unsupported review format 2") || !strings.Contains(err.Error(), "recapture") || strings.Contains(err.Error(), "digest") {
		t.Fatal(err)
	}
	record := &review.Contribution{Format: review.ContributionFormat}
	if err := record.Seal(); err != nil {
		t.Fatal(err)
	}
	record.Parent = "tampered"
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := review.Read(repo, path); err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatal(err)
	}
}

func TestStorePublishesCompleteImmutableRecord(t *testing.T) {
	f := testrepo.New(t)
	repo, _ := f.Open()
	record := review.Contribution{Format: review.ContributionFormat}
	if err := record.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := review.Store(repo, &record); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		copy := record
		go func() {
			_, err := review.Write(repo, &copy, filepath.Join(repo.Root, "target/concave/reviews"))
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	got, err := review.Read(repo, record.ID)
	if err != nil || got.ID != record.ID {
		t.Fatal(got, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(repo.Root, "target/concave/reviews/.review-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal(leftovers, err)
	}
}
