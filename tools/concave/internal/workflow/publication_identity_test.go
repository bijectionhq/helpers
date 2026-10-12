package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/repository"
	"concave/internal/review"
)

// Golden identities were derived from the publisher encoding before the file
// entry cutover. New representations must not create a second publication.
func TestPublicationIdentitySurvivesContentRepresentation(t *testing.T) {
	for _, tc := range []struct{ name, baseline, mode, hash, want string }{
		{"empty", "absent", "100644", repository.Digest(nil), "448b0f04e9a6898973a1e87299e98e9ab0e727196a4c0069f6c0bdbafa2fb080"},
		{"delete", "100644:" + strings.Repeat("1", 40), "absent", "", "659365e9f29d18dcad3b6a8127cac4eb7290e4af4c2dfea9e020401460ed1bd9"},
		{"executable", "100644:" + strings.Repeat("1", 40), "100755", strings.Repeat("2", 64), "7d43c2ffce213af762182597fb17523c4dd6fae16e85829a85e03d3eedad5927"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := review.SelectedFile{Path: "a.go", Baseline: tc.baseline, Change: review.DeleteFile}
			if tc.mode != "absent" {
				file.Change = review.WriteFile
				file.Content = &review.FileContent{Artifact: filepath.Join(t.TempDir(), "source"), Mode: tc.mode, SHA256: tc.hash}
			}
			candidate := &review.Contribution{Candidate: &review.Candidate{Content: &review.SelectedContent{Files: []review.SelectedFile{file}}}}
			got, err := publicationID(candidate)
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
			if file.Content != nil {
				file.Content.Artifact = filepath.Join(t.TempDir(), "relocated-source")
			}
			got, err = publicationID(candidate)
			if err != nil || got != tc.want {
				t.Fatal("artifact location changed publication identity", got, err)
			}
		})
	}
}
