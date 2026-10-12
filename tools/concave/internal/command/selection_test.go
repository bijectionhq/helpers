package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"concave/internal/repository"
	"concave/internal/testrepo"
	"concave/internal/workflow"
)

func TestSelectionRequiresEveryExplicitDecision(t *testing.T) {
	hunks := []repository.Hunk{{ID: "mine", Path: "a.go", Before: "old\n", After: "new\n"}, {ID: "peer", Path: "b.go"}}
	var output bytes.Buffer
	selected, err := selectHunks(context.Background(), strings.NewReader("what\ny\nn\np\n"), &output, hunks)
	if err != nil || len(selected) != 1 || selected[0] != "mine" || !strings.Contains(output.String(), "+new") {
		t.Fatalf("selection %v, %v: %s", selected, err, output.String())
	}
	for _, answer := range []string{"y\n", "y\nq\n", "n\nn\n", "y\nn\n", "y\nn\nn\n"} {
		if got, err := selectHunks(context.Background(), strings.NewReader(answer), &output, hunks); err == nil || len(got) != 0 {
			t.Fatalf("partial/empty decision accepted: %q: %v, %v", answer, got, err)
		}
	}
}

func TestSelectionGroupsFilesAndConfirmsExactHunks(t *testing.T) {
	hunks := []repository.Hunk{{ID: "first", Path: "a.go", Before: "old\n", After: "new\n"}, {ID: "second", Path: "a.go", Start: 4, End: 5, Before: "old2\n", After: "new2\n"}, {ID: "peer", Path: "b.go"}}
	for _, scenario := range []struct {
		name, input string
		want        []string
	}{
		{"file", "y\nn\np\n", []string{"first", "second"}},
		{"hunks", "h\ny\nn\nn\np\n", []string{"first"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var output bytes.Buffer
			got, err := selectHunks(context.Background(), strings.NewReader(scenario.input), &output, hunks)
			if err != nil || strings.Join(got, ",") != strings.Join(scenario.want, ",") {
				t.Fatal(got, err, output.String())
			}
			summary := strings.Split(output.String(), "Selected:")[1]
			if strings.Contains(summary, "peer") || !strings.Contains(summary, "Prepare, edit file number, or cancel?") || !strings.Contains(summary, "[2] b.go (0/1 changes)") {
				t.Fatal(summary)
			}
			if strings.Count(output.String(), "Include this file?") != 2 {
				t.Fatal("file decisions were duplicated", output.String())
			}
		})
	}
}

func TestSelectionCorrectsDecisionsBeforePreparing(t *testing.T) {
	hunks := []repository.Hunk{{ID: "a1", Path: "a.go"}, {ID: "a2", Path: "a.go"}, {ID: "b1", Path: "b.go"}}
	for _, tc := range []struct {
		name, input string
		want        []string
		message     string
	}{
		{"include-excluded-file", "y\nn\n2\ny\np\n", []string{"a1", "a2", "b1"}, "[2] b.go (0/1 changes)"},
		{"exclude-included-file", "y\ny\n1\nn\np\n", []string{"b1"}, "[1] a.go (0/2 changes)"},
		{"correct-hunks", "y\ny\n1\nh\nn\ny\np\n", []string{"a2", "b1"}, "a1 (included)"},
		{"previous-file", "y\nb\nn\ny\np\n", []string{"b1"}, "a.go (2/2 changes selected)"},
		{"previous-hunk", "h\ny\nb\nn\ny\nn\np\n", []string{"a2"}, "a1 (included)"},
		{"back-to-file-menu", "h\nb\ny\nn\np\n", []string{"a1", "a2"}, "a.go (0/2 changes selected)"},
		{"summary-back", "y\ny\n2\nb\nn\np\n", []string{"b1"}, "[1] a.go (0/2 changes)"},
		{"first-file-back", "b\ny\nn\np\n", []string{"a1", "a2"}, "Already at the first file."},
		{"empty-selection", "n\nn\np\n2\ny\np\n", []string{"b1"}, "No changes selected."},
		{"invalid-decisions", "what\ny\nn\n0\n3\nword\n\np\n", []string{"a1", "a2"}, "Choose p to prepare, a file number from 1 to 2 to edit, or q to cancel."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			got, err := selectHunks(context.Background(), strings.NewReader(tc.input), &output, hunks)
			if err != nil || !slices.Equal(got, tc.want) || !strings.Contains(output.String(), tc.message) {
				t.Fatalf("selection %v, %v: %s", got, err, output.String())
			}
		})
	}
}

func TestSelectionCancellationNeverReturnsPartialDecisions(t *testing.T) {
	hunks := []repository.Hunk{{ID: "a1", Path: "a.go"}, {ID: "b1", Path: "b.go"}}
	for _, tc := range []struct{ name, input string }{
		{"file", "y\nq\n"},
		{"hunk", "y\nh\nq\n"},
		{"summary", "y\nn\nq\n"},
		{"editing", "y\nn\n2\nq\n"},
		{"eof-file", "y\n"},
		{"eof-summary", "y\nn\n"},
		{"eof-editing", "y\nn\n1\nh\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectHunks(context.Background(), strings.NewReader(tc.input), io.Discard, hunks)
			if err == nil || len(got) != 0 || !strings.Contains(err.Error(), "no candidate prepared") {
				t.Fatal(got, err)
			}
		})
	}
	t.Run("context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		got, err := selectHunks(ctx, &cancelSelectionReader{cancel: cancel}, io.Discard, hunks)
		if !errors.Is(err, context.Canceled) || len(got) != 0 {
			t.Fatal(got, err)
		}
	})
}

type cancelSelectionReader struct{ cancel context.CancelFunc }

func (r *cancelSelectionReader) Read(data []byte) (int, error) {
	r.cancel()
	return copy(data, "y\ny\np\n"), nil
}

func TestSelectionRevalidatesBeforePreparing(t *testing.T) {
	for _, change := range []string{"source", "policy", "scope"} {
		t.Run(change, func(t *testing.T) {
			f := testrepo.New(t)
			f.Write("crates/core/lib.rs", "fn changed() {}\n")
			repo, config := f.Open()
			service := workflow.Service{Repo: repo, Stderr: io.Discard}
			record, err := service.Capture(config, []string{"crates/core/"}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			record.Selected, err = selectHunks(context.Background(), strings.NewReader("y\np\n"), io.Discard, record.Hunks)
			if err != nil {
				t.Fatal(err)
			}
			record.Parent = record.ID
			if err := record.Seal(); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source":
				f.Write("crates/core/lib.rs", "fn peer_edit() {}\n")
			case "policy":
				f.Config.Families[0].Title += " (edited)"
				f.Save()
			case "scope":
				f.Write("crates/core/new.rs", "fn added() {}\n")
			}
			if _, err := service.Prepare(config, record); !repository.IsStale(err) {
				t.Fatalf("changed %s admitted after selection: %v", change, err)
			}
			if _, err := os.Stat(filepath.Join(f.Root, "target/concave/candidates")); !os.IsNotExist(err) {
				t.Fatal("stale selection created candidate content", err)
			}
		})
	}
}
