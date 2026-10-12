package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"concave/internal/command"
	"concave/internal/continuation"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/review"
)

func TestContributionLandsExactCandidateAndRetainsEvidence(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "reuse"}[reuse], func(t *testing.T) {
			f := contractFixture(t)
			path := "packaging/own.go"
			before := "package own\n\n// concave:invariant owned-work\n// Preserve the locally required operation.\nfunc own() {}\n\nfunc peer() {}\n"
			f.Write(path, before)
			witness := "packaging/own_test.go"
			f.Write(witness, "package own\n")
			f.Config.Checks = map[string]policy.CheckDefinition{"local": {
				Command:   []string{"sh", "-c", `printf '%s\n' '{"Action":"pass","Package":"pkg","Test":"TestOwn"}'`},
				Directory: ".", Inputs: []string{path, witness}, Paths: []string{path},
				Toolchain:      []policy.ToolCheck{{Command: []string{"sh", "-c", "printf v1"}, Expected: "v1"}},
				TimeoutSeconds: 5, Results: "go-test-json", Witnesses: map[string][]string{witness: {"pkg::TestOwn"}},
			}}
			if reuse {
				check := f.Config.Checks["local"]
				check.ReuseReason = "This fixture prints fixed test JSON from an exact command and shell, with no external behavior."
				f.Config.Checks["local"] = check
			}
			if !reuse {
				check := f.Config.Checks["local"]
				check.Command[2] = `if [ "$CONCAVE_TEST_HOLD" = 1 ]; then echo ready > "$CONCAVE_TEST_MARKER"; sleep 30; fi; ` + check.Command[2]
				f.Config.Checks["local"] = check
			}
			f.Save()
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
			f.Git("config", "user.name", "Concave fixture")
			f.Git("config", "user.email", "test@example.invalid")
			f.Commit()
			remote := filepath.Join(t.TempDir(), "origin.git")
			f.Git("init", "--bare", "--initial-branch=main", remote)
			f.Git("remote", "add", "origin", remote)
			f.Git("push", "origin", "main")
			head := f.Git("rev-parse", "HEAD")
			index, err := os.ReadFile(filepath.Join(f.Root, ".git/index"))
			if err != nil {
				t.Fatal(err)
			}
			after := strings.ReplaceAll(strings.ReplaceAll(before, "own() {}", "own() { local() }"), "peer() {}", "peer() { other() }")
			f.Write(path, after)
			capture := func(args ...string) review.Contribution {
				t.Helper()
				code, text, stderr := run(f, args...)
				if code != 0 {
					t.Fatal(code, text, stderr)
				}
				var r review.Contribution
				if args[0] == "verify" {
					var verdict review.Verdict
					if err := json.Unmarshal([]byte(text), &verdict); err != nil || !verdict.Ready() || verdict.Next.Kind != "publish" {
						t.Fatal(text, err)
					}
					repo, err := repository.Open(context.Background(), f.Root)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(repo.Close)
					record, err := review.Read(repo, verdict.Revision)
					if err != nil {
						t.Fatal(err)
					}
					return *record
				}
				if err := json.Unmarshal([]byte(text), &r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			r := capture("review", path, "--json")
			if len(r.Hunks) != 2 {
				t.Fatal(r.Hunks)
			}
			r = capture("review", r.ID, "--hunk", r.Hunks[0].ID, "--json")
			candidateID := r.ID
			candidateContext, candidateInputs := r.Context, r.Inputs
			// A saved candidate owns its policy even while a peer edits live configuration.
			f.Write(policy.File, "unfinished policy {\n")
			r = capture("verify", r.ID, "--json")
			if !reflect.DeepEqual(candidateContext.Candidate.Findings, r.Context.Candidate.Findings) || !reflect.DeepEqual(candidateContext.Blobs, r.Context.Blobs) || !reflect.DeepEqual(candidateInputs, r.Inputs) {
				t.Fatal("Git source view and materialized execution disagree")
			}
			if r.Verification == nil || r.Verification.Candidate != candidateID {
				t.Fatal("verification created a redundant candidate revision")
			}
			if r.Verification == nil || r.Verification.Candidate == "" || len(r.Evidence()) != 1 || r.Evidence()[0].Mode != "executed" {
				t.Fatal("initial execution missing", r)
			}
			if reuse {
				members := filepath.Join(f.Root, "target/concave/reviews/contributions", r.Origin[2:10], r.Origin)
				beforeRecords, err := os.ReadDir(members)
				if err != nil {
					t.Fatal(err)
				}
				r = capture("verify", r.ID, "--json")
				afterRecords, err := os.ReadDir(members)
				if err != nil {
					t.Fatal(err)
				}
				if len(beforeRecords) != 3 || len(afterRecords) != 4 || r.Verification.Candidate != candidateID || r.Evidence()[0].Mode != "reused" {
					t.Fatal("repeat verification must add one evidence revision and retain its original candidate", len(beforeRecords), len(afterRecords), r.Verification)
				}
			}
			if !reuse {
				marker := filepath.Join(f.Root, "target/cancel-check")
				t.Setenv("CONCAVE_TEST_HOLD", "1")
				t.Setenv("CONCAVE_TEST_MARKER", marker)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var out, stderr bytes.Buffer
				done := make(chan int, 1)
				go func() {
					done <- command.Run(ctx, []string{"--repo", f.Root, "verify", candidateID, "--json"}, &out, &stderr)
				}()
				// Materialization and evaluation precede the held check; a loaded
				// machine can take far longer than its execution.
				deadline := time.Now().Add(2 * time.Minute)
				for !regularTestFile(marker) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !regularTestFile(marker) {
					cancel()
					<-done
					t.Fatal("held check never started", stderr.String())
				}
				cancel()
				code := <-done
				var verdict review.Verdict
				if !regularTestFile(marker) || code != 1 || json.Unmarshal(out.Bytes(), &verdict) != nil || verdict.Ready() || len(verdict.Checks) != 1 || verdict.Checks[0].Problems[0].Code != "cancelled" || verdict.Next.Kind != "verify" {
					t.Fatal("cancellation lost its durable verdict", code, out.String(), stderr.String())
				}
				t.Setenv("CONCAVE_TEST_HOLD", "")
			}
			originalReceipt := r.Evidence()[0].Receipt.ReceiptSHA256
			// Fail before the test runner starts: the outer CLI must retain and expose
			// the execution-environment error, not merely land.py's exit status.
			if !reuse {
				broken := r
				meta := *r.Candidate
				broken.Candidate = &meta
				// A missing commit is an execution setup error independent of source acceptance.
				broken.Candidate.Commit = "0000000000000000000000000000000000000000"
				repo, err := repository.Open(context.Background(), f.Root)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(repo.Close)
				ref, err := review.Store(repo, &broken)
				if err != nil {
					t.Fatal(err)
				}
				code, _, stderr := run(f, "verify", ref, "--output", filepath.Join(f.Root, "target/failed-evaluation"))
				if code == 0 || !strings.Contains(stderr, "git rev-parse") {
					t.Fatal("lost setup diagnostic", code, stderr)
				}
				log, err := os.ReadFile(filepath.Join(f.Root, "target/failed-evaluation/evaluation.stderr.log"))
				if err != nil || !strings.Contains(string(log), "git rev-parse") {
					t.Fatal("lost setup log", err, string(log))
				}
			}

			if code, _, stderr := run(f, "review", "status", r.ID); code != 0 {
				t.Fatal(code, stderr)
			}
			// An unrelated publication changes the merged tree, not this check inputs.
			clone := filepath.Join(t.TempDir(), "peer")
			for _, args := range [][]string{{"clone", remote, clone}, {"-C", clone, "config", "user.name", "Peer"}, {"-C", clone, "config", "user.email", "peer@example.invalid"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatal(err, string(out))
				}
			}
			if err := os.WriteFile(filepath.Join(clone, "unrelated"), []byte("published by peer\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"-C", clone, "add", "unrelated"}, {"-C", clone, "commit", "-m", "peer"}, {"-C", clone, "push", "origin", "main"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatal(err, string(out))
				}
			}
			if candidateID == r.ID {
				t.Fatal("candidate overwritten")
			}

			savedArtifacts := artifactFiles(t, f.Root)
			code, text, stderr := run(f, "review", "export", r.ID, "--message", "Publish selected fixture", "--json")
			if code != 0 {
				t.Fatal(code, text, stderr)
			}
			var exported struct {
				Content  review.SelectedContent `json:"content"`
				Artifact string                 `json:"artifact"`
				Command  []string               `json:"command"`
			}
			if err := json.Unmarshal([]byte(text), &exported); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				code, again, stderr := run(f, "review", "export", r.ID, "--message", "Publish selected fixture", "--json")
				if code != 0 || again != text {
					t.Fatal("publication command is not stable", code, again, stderr)
				}
			}
			if !reflect.DeepEqual(savedArtifacts, artifactFiles(t, f.Root)) {
				t.Fatal("command preparation changed artifacts")
			}
			for _, args := range [][]string{{"review", r.ID, "--json"}, {"review", "status", r.ID, "--json"}} {
				if code, text, stderr := run(f, args...); code != 0 {
					t.Fatal("saved review read working policy", code, text, stderr)
				}
			}
			source := exported.Content.Files[0].Content.Artifact
			original, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("tampered\n"), 0600); err != nil {
				t.Fatal(err)
			}
			tampered := exec.Command(exported.Command[0], exported.Command[1:]...)
			tampered.Dir = f.Root
			if out, err := tampered.CombinedOutput(); err == nil || !strings.Contains(string(out), "reviewed SHA256") {
				t.Fatal("modified export accepted", err, string(out))
			}
			if err := os.WriteFile(source, original, 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(exported.Command[0], exported.Command[1:]...)
			command.Dir = f.Root
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "CONCAVE_AST_GREP=") {
					command.Env = append(command.Env, value)
				}
			}
			command.Env = append(command.Env, "GIT_CONFIG_GLOBAL="+os.DevNull)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatal(err, string(output))
			}
			// The gate inside publication offers no continuation: publishing is the
			// publisher's own step, and no command can point into its checkout.
			if !strings.Contains(string(output), "Publication gate passed; the publisher continues.") || strings.Contains(string(output), "concave --repo") {
				t.Fatal("publication gate offered a continuation", string(output))
			}
			want := strings.Replace(before, "own() {}", "own() { local() }", 1)
			if got := f.Git("--git-dir", remote, "show", "main:"+path); got != strings.TrimSpace(want) {
				t.Fatal("published peer edit", got)
			}
			working, _ := os.ReadFile(filepath.Join(f.Root, path))
			currentIndex, _ := os.ReadFile(filepath.Join(f.Root, ".git/index"))
			livePolicy, err := os.ReadFile(filepath.Join(f.Root, policy.File))
			if err != nil || string(livePolicy) != "unfinished policy {\n" {
				t.Fatal("publication changed peer policy", err)
			}
			if string(working) != after || string(index) != string(currentIndex) || f.Git("rev-parse", "HEAD") != head {
				t.Fatal("landing changed shared source, index or HEAD")
			}
			retained, err := filepath.Glob(filepath.Join(filepath.Dir(exported.Artifact), "runs/*/v-*.json"))
			if err != nil || len(retained) != 1 {
				t.Fatal("candidate evidence did not survive checkout removal", retained, err, string(output))
			}
			data, err := os.ReadFile(retained[0])
			if err != nil {
				t.Fatal(err)
			}
			var candidate review.Contribution
			if err := json.Unmarshal(data, &candidate); err != nil || len(candidate.Evidence()) != 1 {
				t.Fatal(err, string(data))
			}
			if _, err := os.Stat(candidate.Evidence()[0].Directory + "/stdout.log"); err != nil {
				t.Fatal("candidate log was deleted", err)
			}
			if reuse && (candidate.Evidence()[0].Mode != "reused" || candidate.Evidence()[0].Receipt.ReceiptSHA256 != originalReceipt) {
				t.Fatal("valid receipt was not reused", candidate.Evidence())
			}
			if !reuse && candidate.Evidence()[0].Mode != "executed" {
				t.Fatal("nonreusable check skipped", candidate.Evidence())
			}
		})
	}
}

func regularTestFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Exercise creation, retirement and mode changes through the real publication
// owner, including empty files and a peer's recreation after capture.
func TestContributionFileOperations(t *testing.T) {
	for _, operation := range []string{"delete", "delete-empty", "create-empty", "mode"} {
		t.Run(operation, func(t *testing.T) {
			f := contractFixture(t)
			path := "packaging/own.go"
			if operation != "create-empty" {
				content := "package own\n"
				if operation == "delete-empty" {
					content = ""
				}
				f.Write(path, content)
			}
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
			switch operation {
			case "delete", "delete-empty":
				if err := os.Remove(filepath.Join(f.Root, path)); err != nil {
					t.Fatal(err)
				}
			case "create-empty":
				f.Write(path, "")
			case "mode":
				if err := os.Chmod(filepath.Join(f.Root, path), 0755); err != nil {
					t.Fatal(err)
				}
			}
			code, text, stderr := run(f, "review", path, "--json")
			var captured review.Contribution
			if code != 0 || json.Unmarshal([]byte(text), &captured) != nil || len(captured.Hunks) != 1 {
				t.Fatal(code, text, stderr)
			}
			code, text, stderr = run(f, "review", captured.ID, "--hunk", captured.Hunks[0].ID, "--json")
			var candidate review.Contribution
			if code != 0 || json.Unmarshal([]byte(text), &candidate) != nil {
				t.Fatal(code, text, stderr)
			}
			// Later peer bytes must never replace the frozen selected operation.
			f.Write(path, "peer bytes stay private\n")
			code, text, stderr = run(f, "verify", candidate.ID, "--json")
			var verdict review.Verdict
			if code != 0 || json.Unmarshal([]byte(text), &verdict) != nil || !verdict.Ready() {
				t.Fatal(code, text, stderr)
			}
			code, text, stderr = run(f, "review", "export", verdict.Revision, "--message", "Selected file operation", "--json")
			var exported struct {
				Command []string `json:"command"`
			}
			if code != 0 || json.Unmarshal([]byte(text), &exported) != nil {
				t.Fatal(code, text, stderr)
			}
			cmd := exec.Command(exported.Command[0], exported.Command[1:]...)
			cmd.Dir = f.Root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			tree := f.Git("ls-tree", "origin/main", "--", path)
			if strings.HasPrefix(operation, "delete") {
				if tree != "" {
					t.Fatal(tree)
				}
			} else {
				mode := "100644"
				if operation == "mode" {
					mode = "100755"
				}
				if !strings.HasPrefix(tree, mode) {
					t.Fatal(tree)
				}
				expected := ""
				if operation == "mode" {
					expected = "package own"
				}
				if data := f.Git("show", "origin/main:"+path); data != expected {
					t.Fatal(data)
				}
			}
			data, err := os.ReadFile(filepath.Join(f.Root, path))
			if err != nil || string(data) != "peer bytes stay private\n" {
				t.Fatal(err, string(data))
			}
		})
	}
}

// A capture names no hunks; verify selects every change it shows, without a
// terminal, and prepares that candidate before running its checks.
func TestVerifySelectsEveryShownHunk(t *testing.T) {
	f := contractFixture(t)
	path := "packaging/own.go"
	f.Write(path, "package own\n\nfunc own() {}\n\nfunc peer() {}\n")
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
	f.Git("config", "user.name", "Concave fixture")
	f.Git("config", "user.email", "test@example.invalid")
	f.Commit()
	remote := filepath.Join(t.TempDir(), "origin.git")
	f.Git("init", "--bare", "--initial-branch=main", remote)
	f.Git("remote", "add", "origin", remote)
	f.Git("push", "origin", "main")
	f.Write(path, "package own\n\nfunc own() { local() }\n\nfunc peer() { other() }\n")
	code, text, stderr := run(f, "review", path, "--json")
	var captured review.Contribution
	if code != 0 || json.Unmarshal([]byte(text), &captured) != nil || len(captured.Hunks) != 2 {
		t.Fatal(code, text, stderr)
	}
	var verdict review.Verdict
	if code, text, stderr = run(f, "review", "status", captured.Reference(), "--json"); code != 0 || json.Unmarshal([]byte(text), &verdict) != nil || verdict.Outcome != review.NeedsVerification || verdict.Selection != review.SelectionAll || verdict.Next.Kind != continuation.Verify {
		t.Fatal(code, text, stderr)
	}
	if code, text, stderr = run(f, "verify", captured.Reference(), "--json"); code != 0 || json.Unmarshal([]byte(text), &verdict) != nil || !verdict.Ready() {
		t.Fatal(code, text, stderr)
	}
	repo, err := repository.Open(context.Background(), f.Root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repo.Close)
	prepared, err := review.Read(repo, verdict.Revision)
	if want := []string{captured.Hunks[0].ID, captured.Hunks[1].ID}; err != nil || !reflect.DeepEqual(prepared.Selected, want) || prepared.Candidate == nil {
		t.Fatal(prepared, want, err)
	}
	// A synchronization moves the selected file's baseline after review: the
	// reviewed delta merges from its recorded baseline and keeps the newer work.
	synchronized := filepath.Join(t.TempDir(), "own.go")
	if err := os.WriteFile(synchronized, []byte("// synchronized\npackage own\n\nfunc own() {}\n\nfunc peer() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.Git("update-index", "--cacheinfo", "100644,"+f.Git("hash-object", "-w", synchronized)+","+path)
	f.Git("commit", "--quiet", "-m", "synchronized peer work")
	f.Git("push", "--quiet", "origin", "main")
	// Another unfinished capture owning the path refuses publication first.
	blocking := exec.Command("python3", "scripts/land.py", "-m", "unfinished", "--check", "false", "--", path)
	blocking.Dir = f.Root
	if out, err := blocking.CombinedOutput(); err == nil {
		t.Fatal("blocking capture published", string(out))
	}
	code, text, stderr = run(f, "review", "publish", prepared.Reference(), "-m", "Select every hunk", "--json")
	var refused struct {
		State string              `json:"state"`
		Next  continuation.Action `json:"next"`
	}
	if code == 0 || json.Unmarshal([]byte(text), &refused) != nil || refused.State != "unpublished" || refused.Next.Kind != continuation.Publish || !strings.Contains(refused.Next.Message, "refused") || !strings.Contains(stderr, "unfinished capture owns overlapping paths") {
		t.Fatal(code, text, stderr)
	}
	receipts, err := filepath.Glob(filepath.Join(f.Root, ".git/land/receipts/*/state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		var state struct {
			Message   string `json:"message"`
			Published bool   `json:"published"`
		}
		if data, err := os.ReadFile(receipt); err == nil && json.Unmarshal(data, &state) == nil && state.Message == "unfinished" && !state.Published {
			abandon := exec.Command("python3", "scripts/land.py", "--abandon", filepath.Dir(receipt))
			abandon.Dir = f.Root
			if out, err := abandon.CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
		}
	}
	if code, text, stderr = run(f, "review", "publish", prepared.Reference(), "-m", "Select every hunk"); code != 0 {
		t.Fatal(code, text, stderr)
	}
	if got := f.Git("--git-dir", remote, "show", "main:"+path); got != "// synchronized\npackage own\n\nfunc own() { local() }\n\nfunc peer() { other() }" {
		t.Fatal("published tree lost the reviewed or the synchronized work", got)
	}
}
