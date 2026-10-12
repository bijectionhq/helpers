package review_test

import (
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/repository"
	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestSelectedContentRejectsAmbiguousFileChanges(t *testing.T) {
	for _, scenario := range []string{"empty", "duplicate", "unsorted", "unsafe-path", "missing-kind", "missing-content", "delete-with-content", "delete-absent", "bad-baseline", "bad-mode", "bad-hash", "relative-artifact"} {
		t.Run(scenario, func(t *testing.T) {
			f := testrepo.New(t)
			repo, _ := f.Open()
			content := &review.SelectedContent{Files: []review.SelectedFile{{Path: "a.go", Baseline: "100644:" + strings.Repeat("1", 40), Change: review.WriteFile, Content: &review.FileContent{Artifact: filepath.Join(f.Root, "target/source"), Mode: "100644", SHA256: repository.Digest(nil)}}}}
			file := &content.Files[0]
			switch scenario {
			case "empty":
				content.Files = nil
			case "duplicate":
				content.Files = append(content.Files, *file)
			case "unsorted":
				second := *file
				file.Path = "z.go"
				content.Files = append(content.Files, second)
			case "unsafe-path":
				file.Path = "../a.go"
			case "missing-kind":
				file.Change = ""
			case "missing-content":
				file.Content = nil
			case "delete-with-content":
				file.Change = review.DeleteFile
			case "delete-absent":
				file.Change = review.DeleteFile
				file.Baseline = "absent"
				file.Content = nil
			case "bad-baseline":
				file.Baseline = "100644:invalid"
			case "bad-mode":
				file.Content.Mode = "120000"
			case "bad-hash":
				file.Content.SHA256 = strings.Repeat("g", 64)
			case "relative-artifact":
				file.Content.Artifact = "source"
			}
			record := &review.Contribution{Format: review.ContributionFormat, Candidate: &review.Candidate{Commit: "commit", Tree: "tree", Parent: "parent", Content: content}}
			path, err := review.Store(repo, record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = review.Read(repo, path); err == nil {
				t.Fatal("malformed selected content was admitted")
			}
		})
	}
}

func TestRevisionKindsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		record review.Contribution
	}{
		{"concave-capture", review.Contribution{}},
		{"concave-candidate", review.Contribution{Candidate: &review.Candidate{}}},
		{"concave-verification", review.Contribution{Candidate: &review.Candidate{}, Verification: &review.Verification{}}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if err := tc.record.Seal(); err != nil {
				t.Fatal(err)
			}
			if tc.record.Kind != tc.kind || tc.record.Kind == "concave-receipt" {
				t.Fatal("contribution revision must differ from a check receipt", tc.record.Kind)
			}
		})
	}
}
