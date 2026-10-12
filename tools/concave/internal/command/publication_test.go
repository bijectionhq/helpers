package command_test

import (
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/review"
	"concave/internal/workflow"
)

func TestContributionFlowPublishesAndResumesByReference(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "conflict"}[conflict], func(t *testing.T) {
			f := contractFixture(t)
			path := "packaging/own.go"
			f.Write(path, "package own\nfunc own() {}\n")
			for _, name := range []string{"land.py", "conflict-marker-check.sh"} {
				data, err := os.ReadFile(filepath.Join("../../../../scripts", name))
				if err != nil {
					t.Fatal(err)
				}
				f.Write("scripts/"+name, string(data))
				if err := os.Chmod(filepath.Join(f.Root, "scripts", name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			f.Commit()
			remote := filepath.Join(t.TempDir(), "origin.git")
			f.Git("init", "--bare", "--initial-branch=main", remote)
			f.Git("remote", "add", "origin", remote)
			f.Git("push", "origin", "main")
			head := f.Git("rev-parse", "HEAD")
			index, _ := os.ReadFile(filepath.Join(f.Root, ".git/index"))
			f.Write(path, "package own\nfunc own() { selected() }\n")
			var capture review.Contribution
			code, text, stderr := run(f, "review", path, "--json")
			if code != 0 || json.Unmarshal([]byte(text), &capture) != nil || len(capture.Hunks) != 1 {
				t.Fatal(code, text, stderr)
			}
			var candidate review.Contribution
			// Capture plus an explicit hunk selection is one invocation.
			code, text, stderr = run(f, "review", path, "--hunk", capture.Hunks[0].ID, "--json")
			if code != 0 || json.Unmarshal([]byte(text), &candidate) != nil {
				t.Fatal(code, text, stderr)
			}
			ref := candidate.Reference()
			beforeVerification := artifactFiles(t, f.Root)
			code, text, stderr = run(f, "review", "status", ref, "--json")
			var unverified review.Verdict
			if code != 1 || json.Unmarshal([]byte(text), &unverified) != nil || unverified.Outcome != review.NeedsVerification {
				t.Fatal("unverified candidate became ready", code, text, stderr)
			}
			if !reflect.DeepEqual(beforeVerification, artifactFiles(t, f.Root)) {
				t.Fatal("unverified status wrote artifacts")
			}
			code, text, stderr = run(f, "verify", ref, "--json")
			var verdict review.Verdict
			if code != 0 || json.Unmarshal([]byte(text), &verdict) != nil || !verdict.Ready() || verdict.Outcome != review.Ready {
				t.Fatal(code, text, stderr)
			}
			saved := artifactFiles(t, f.Root)
			f.Write(path, "package own\nfunc own() { unfinished() }\n")
			for i := 0; i < 2; i++ {
				code, text, stderr = run(f, "review", "status", ref, "--json")
				if code != 0 || json.Unmarshal([]byte(text), &verdict) != nil || !verdict.Ready() || verdict.Outcome != review.Ready {
					t.Fatal(code, text, stderr)
				}
			}
			if after := artifactFiles(t, f.Root); !reflect.DeepEqual(saved, after) {
				t.Fatal("status created or modified artifacts")
			}
			if conflict {
				peer := filepath.Join(t.TempDir(), "peer")
				f.Git("clone", remote, peer)
				f.Git("-C", peer, "config", "user.name", "Peer")
				f.Git("-C", peer, "config", "user.email", "peer@example.invalid")
				if err := os.WriteFile(filepath.Join(peer, path), []byte("package own\nfunc own() { remote() }\n"), 0600); err != nil {
					t.Fatal(err)
				}
				f.Git("-C", peer, "add", path)
				f.Git("-C", peer, "commit", "-m", "peer")
				f.Git("-C", peer, "push", "origin", "main")
			}
			code, text, stderr = run(f, "review", "publish", ref)
			if code != 2 || !strings.Contains(text, "Supply: --message <what changed and why>") {
				t.Fatal("missing publication input hidden", code, text, stderr)
			}
			var publication workflow.Publication
			code, text, stderr = run(f, "review", "publish", ref, "--message", "Publish selected review", "--json")
			if err := json.Unmarshal([]byte(text), &publication); err != nil {
				t.Fatal(code, text, stderr, err)
			}
			if conflict {
				if code != 3 || publication.State != "conflict" || publication.Conflict == "" {
					t.Fatal(code, text, stderr)
				}
				wrong := t.TempDir()
				if err := os.WriteFile(filepath.Join(wrong, "state.json"), []byte(`{"kind":"publication"}`), 0600); err != nil {
					t.Fatal(err)
				}
				code, text, stderr = run(f, "review", "publish", ref, "--resolve", wrong, "--json")
				if code != 4 || !strings.Contains(stderr, "another publication") {
					t.Fatal(code, text, stderr)
				}
				// The shared source remains untouched; resolve the owned private file.
				file := filepath.Join(publication.Conflict, "files", path)
				if err := os.WriteFile(file, []byte("package own\nfunc own() { remote(); selected() }\n"), 0600); err != nil {
					t.Fatal(err)
				}
				code, text, stderr = run(f, "review", "publish", ref, "--resolve", publication.Conflict, "--json")
				if json.Unmarshal([]byte(text), &publication) != nil {
					t.Fatal(text)
				}
			}
			if code != 0 || publication.State != "published" {
				t.Fatal(code, text, stderr)
			}
			published := f.Git("--git-dir", remote, "rev-parse", "main")
			if !strings.Contains(f.Git("--git-dir", remote, "show", "main:"+path), "selected()") {
				t.Fatal("selected bytes absent")
			}
			// A retry reconciles the same receipt and never captures later source.
			f.Write(path, "package own\nfunc own() { unfinished() }\n")
			code, text, stderr = run(f, "review", "publish", ref, "--json")
			if code != 0 || json.Unmarshal([]byte(text), &publication) != nil || publication.State != "published" {
				t.Fatal(code, text, stderr)
			}
			if f.Git("--git-dir", remote, "rev-parse", "main") != published || f.Git("rev-parse", "HEAD") != head {
				t.Fatal("retry moved history")
			}
			current, _ := os.ReadFile(filepath.Join(f.Root, ".git/index"))
			if string(index) != string(current) {
				t.Fatal("shared index changed")
			}
		})
	}
}

func artifactFiles(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	files := map[string][32]byte{}
	for _, dir := range []string{"target/concave", ".git/land"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				files[path] = [32]byte{}
				return nil
			}
			data, err := os.ReadFile(path)
			if err == nil {
				files[path] = sha256.Sum256(data)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}
