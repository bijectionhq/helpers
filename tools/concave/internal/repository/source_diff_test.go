package repository_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestSourceDiffBatchesExactBytesWithoutMutatingRepository(t *testing.T) {
	f := testrepo.New(t)
	r, _ := f.Open()
	index := f.Git("write-tree")
	head := f.Git("rev-parse", "HEAD")
	status := f.Git("status", "--porcelain")
	pairs := map[string]repository.SourcePair{
		"one.rs":            {Before: []byte("pub struct One;\n\nend\n"), After: []byte("#[derive(Test)]\npub struct One;\n\nend\n")},
		"empty.rs":          {Before: []byte{}, After: []byte("added\n")},
		"removed.rs":        {Before: []byte("old\n"), After: []byte{}},
		"mixed ' \t\n é.rs": {Before: []byte("before\r\nno newline"), After: []byte("after\r\nno newline\n")},
		"ONE.rs":            {Before: []byte("ONE\n"), After: []byte("changed\n")},
		"same.rs":           {Before: []byte("same\n"), After: []byte("same\n")},
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	processes := r.Processes
	hs, err := r.DiffSources(pairs)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 5 || r.Processes-processes != 1 {
		t.Fatal(hs, r.Processes-processes)
	}
	for path, spans := range hs {
		for _, h := range spans {
			if h.Path != path {
				t.Fatal(h)
			}
		}
	}
	if len(hs["one.rs"]) != 1 || hs["one.rs"][0].Before != "" || hs["one.rs"][0].After != "#[derive(Test)]\n" {
		t.Fatal(hs)
	}
	if f.Git("write-tree") != index || f.Git("rev-parse", "HEAD") != head || f.Git("status", "--porcelain") != status {
		t.Fatal("repository mutated")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary source residue", entries, err)
	}
	processes = r.Processes
	if _, err = r.DiffSources(map[string]repository.SourcePair{"same": pairs["same.rs"]}); err != nil || r.Processes != processes {
		t.Fatal("unchanged source launched Git", err)
	}
}

func TestSourceDiffRefusesInvalidInputsAndCancellation(t *testing.T) {
	for _, kind := range []string{"unsafe", "directory", "binary", "unavailable", "oversize", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			r, _ := f.Open()
			path := "file"
			pair := repository.SourcePair{Before: []byte("before\n"), After: []byte("after\n")}
			switch kind {
			case "unsafe":
				path = "../outside"
			case "directory":
				path = "dir/"
			case "binary":
				pair.After = []byte{0}
			case "unavailable":
				pair.Before = nil
			case "oversize":
				pair.After = []byte(strings.Repeat("x", repository.MaxBytes+1))
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				r.Context = ctx
			}
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			if _, err := r.DiffSources(map[string]repository.SourcePair{path: pair}); err == nil {
				t.Fatal("accepted invalid input")
			}
			entries, err := filepath.Glob(filepath.Join(tmp, "concave-source-diff-*"))
			if err != nil || len(entries) != 0 {
				t.Fatal("temporary source residue", entries, err)
			}
		})
	}
}

func TestWorkingPatchPreservesTextModesAndBinarySummaries(t *testing.T) {
	f := testrepo.New(t)
	f.Write("crates/core/image.png", "\x00original")
	base := f.Commit()
	added := "crates/core/new 雪.rs"
	f.Write(added, "new source without final newline")
	f.Write("crates/core/empty.rs", "")
	if err := os.Remove(filepath.Join(f.Root, "crates/core/lib.rs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(f.Root, "npm-packages/convex/index.ts"), 0700); err != nil {
		t.Fatal(err)
	}
	r, _ := f.Open()
	paths := []string{added, "crates/core/lib.rs", "crates/core/empty.rs", "npm-packages/convex/index.ts"}
	patch, err := r.DiffPatch(base, "", paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"new file mode 100644", "deleted file mode 100644", "new mode 100755", "No newline at end of file"} {
		if !strings.Contains(string(patch), expected) {
			t.Fatal("missing patch metadata", expected, string(patch))
		}
	}
	// The diagnostic patch must describe the exact working state, including new,
	// empty, removed, executable and Unicode paths, without applying any edits.
	if _, err := r.Git(patch, "apply", "--reverse", "--check", "--unidiff-zero", "--whitespace=nowarn", "-"); err != nil {
		t.Fatal(err, string(patch))
	}
	f.Write("crates/core/image.png", "\x00private binary bytes")
	patch, err = r.DiffPatch(base, "", []string{"crates/core/image.png"})
	if err != nil || !strings.Contains(string(patch), "Binary files") || strings.Contains(string(patch), "private binary bytes") {
		t.Fatal("binary patch must be an identity-only diagnostic", err, string(patch))
	}
}
