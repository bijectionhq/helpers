package workflow

import (
	"context"
	"io"

	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/review"
	"concave/internal/testrepo"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandPreparesOnlySelectedChecks(t *testing.T) {
	configured := os.Getenv("CONCAVE_AST_GREP")
	if configured == "" {
		configured = "../../../../target/concave/engine/0.45.3/ast-grep"
	}
	engine, err := filepath.Abs(configured)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCAVE_AST_GREP", engine)
	fixture := testrepo.New(t)
	repo, config := fixture.Open()
	fixture.Write("target/.keep", "")
	marker := filepath.Join(fixture.Root, "target/node-probed")
	fake := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho probed > "+marker+"\necho 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	fake, err = filepath.EvalSymlinks(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCAVE_NODE", fake)
	fixture.Write(".nvmrc", "v0\n")
	config.Environments = map[string]policy.Environment{"sdk": {}}
	config.Checks = map[string]policy.CheckDefinition{"native": {}, "rust": {Results: "rust-test-json"}, "sdk": {Environment: "sdk"}, "node": {Toolchain: []policy.ToolCheck{{Command: []string{"node", "-p", "process.versions.node"}, VersionFile: ".nvmrc"}}}}
	args, err := Command(repo, config, []string{"native"}, "candidate", Options{Execute: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range args {
		if strings.HasPrefix(value, "CONCAVE_NODE=") || strings.HasPrefix(value, "CARGO_TARGET_DIR=") {
			t.Fatal(args)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unselected Node was probed", err)
	}
	fixture.Commit()
	view, err := repo.At("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	fixture.Write(".nvmrc", "v999\n")
	repo = view
	args, err = Command(repo, config, []string{"node"}, "candidate", Options{Execute: true})
	if err != nil || !strings.Contains(strings.Join(args, "\n"), "CONCAVE_NODE="+fake) || strings.Contains(strings.Join(args, "\n"), "CARGO_TARGET_DIR=") {
		t.Fatal(args, err)
	}
	args, err = Command(repo, config, []string{"sdk", "rust"}, "candidate", Options{Execute: true})
	if err != nil || !strings.Contains(strings.Join(args, "\n"), "CONCAVE_NODE="+fake) || !strings.Contains(strings.Join(args, "\n"), "CARGO_TARGET_DIR=") {
		t.Fatal(args, err)
	}
}

func TestFinalizationSurvivesCancellationAfterExecution(t *testing.T) {
	fixture := testrepo.New(t)
	repo, config := fixture.Open()
	service := Service{Repo: repo, Stderr: io.Discard}
	record, err := service.Capture(config, []string{"crates/core/lib.rs"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(repo.Context)
	defer cancel()
	ctx := process.WithProgress(parent, func(event process.Observation) {
		if event.Phase == "finalize" && !event.Finished {
			cancel()
		}
	})
	repo = repo.WithContext(ctx)
	defer repo.Close()
	directory, err := repo.OutputDirectory(filepath.Join(fixture.Root, "target/finalized"))
	if err != nil {
		t.Fatal(err)
	}
	retained, verdict, err := finalizeEvaluation(repo, repo, config, record, directory, "")
	if parent.Err() != context.Canceled || err != nil || retained == nil || verdict == nil || !verdict.Passed() {
		t.Fatal("late cancellation lost completed evidence", retained, verdict, err)
	}
	for _, name := range []string{"result.json", "verdict.json", retained.ID + ".json"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	reader := repo.WithContext(context.Background())
	defer reader.Close()
	if saved, err := review.Read(reader, filepath.Join(directory, retained.ID+".json")); err != nil || saved.ID != retained.ID {
		t.Fatal("finalized record is not intact", saved, err)
	}
}
