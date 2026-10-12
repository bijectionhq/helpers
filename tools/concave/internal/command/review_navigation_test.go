package command_test

import (
	"concave/internal/analysis"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"concave/internal/testrepo"
)

func replayCommand(t *testing.T, text, label string) []byte {
	t.Helper()
	prefix := label + ": concave "
	var suggestion string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			suggestion = strings.TrimPrefix(line, prefix)
			break
		}
	}
	if suggestion == "" {
		t.Fatal("missing command continuation", label, text)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", `cli=$1; set -- `+suggestion+`; repo_flag=$1; repo=$2; shift 2; exec "$cli" "$repo_flag" "$repo" --json "$@"`, "command-replay", executable)
	command.Dir = t.TempDir()
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("replay: %v: %s", err, data)
	}
	return data
}

func TestReviewCommandsKeepPinnedScopeAcrossNavigation(t *testing.T) {
	fixture := testrepo.New(t)
	paths := []string{"crates/core/owner's $(printf expanded).rs", "crates/core/second.rs", "crates/core/third.rs"}
	outside := "npm-packages/convex/other.ts"
	for _, path := range append(paths, outside) {
		fixture.Write(path, "original\n")
	}
	fixture.Config.Upstream = fixture.Commit()
	fixture.Save()
	for _, path := range append(paths, outside) {
		fixture.Write(path, "changed\n")
	}
	fixture.Write(paths[2], "a separate longer change\n")
	revision := fixture.Commit()
	args := []string{"inspect", "queue", "--rev", "HEAD", "--family", "engine", "--top", "1", "crates/core/"}
	code, filesText, stderr := run(fixture, append(args, "--files")...)
	if code != 0 {
		t.Fatal(code, filesText, stderr)
	}
	code, groupsText, stderr := run(fixture, args...)
	if code != 0 {
		t.Fatal(code, groupsText, stderr)
	}
	// Moving HEAD and editing the current source must not retarget printed commands.
	fixture.Write(paths[0], "later committed bytes\n")
	fixture.Commit()
	fixture.Write(paths[1], "uncommitted bytes\n")
	index, working := fixture.Git("ls-files", "--stage"), fixture.Git("diff", "--")
	var file analysis.FindingQueue
	if err := json.Unmarshal(replayCommand(t, filesText, "Open first file"), &file); err != nil {
		t.Fatal(err)
	}
	if file.Revision != revision || file.Scope.Family != "engine" || !reflect.DeepEqual(file.Scope.Paths, paths[:1]) || file.Groups[0].After != "changed\n" {
		t.Fatal("file navigation changed source or scope", file)
	}
	var allFiles analysis.FindingFileView
	if err := json.Unmarshal(replayCommand(t, filesText, "Show all files"), &allFiles); err != nil {
		t.Fatal(err)
	}
	if allFiles.Revision != revision || len(allFiles.Files) != 3 || allFiles.Scope.Family != "engine" || !reflect.DeepEqual(allFiles.Scope.Paths, []string{"crates/core/"}) {
		t.Fatal("file expansion changed scope", allFiles)
	}
	var group analysis.FindingQueue
	if err := json.Unmarshal(replayCommand(t, groupsText, "Open group"), &group); err != nil {
		t.Fatal(err)
	}
	if group.Revision != revision || group.Scope.Family != "engine" || len(group.Groups) != 1 || len(group.Groups[0].Occurrences) != 2 || group.SelectedGroup == "" || !reflect.DeepEqual(group.Scope.Paths, []string{"crates/core/"}) {
		t.Fatal("group navigation changed scope", group)
	}
	var allGroups analysis.FindingQueue
	if err := json.Unmarshal(replayCommand(t, groupsText, "Show all groups"), &allGroups); err != nil {
		t.Fatal(err)
	}
	if allGroups.Revision != revision || len(allGroups.Groups) != 2 || allGroups.Scope.Family != "engine" || !reflect.DeepEqual(allGroups.Scope.Paths, []string{"crates/core/"}) {
		t.Fatal("group expansion changed scope", allGroups)
	}
	code, text, stderr := run(fixture, "inspect", "queue", "--rev", revision, "--family", "engine", "--group", group.SelectedGroup, "--top", "1", "crates/core/")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var expanded analysis.FindingQueue
	if err := json.Unmarshal(replayCommand(t, text, "Open group"), &expanded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(group, expanded) {
		t.Fatal("location expansion changed the group")
	}
	var repositoryGroup analysis.FindingQueue
	if err := json.Unmarshal(replayCommand(t, text, "Find same edit across repository"), &repositoryGroup); err != nil {
		t.Fatal(err)
	}
	if repositoryGroup.Revision != revision || repositoryGroup.Scope.Family != "" || len(repositoryGroup.Scope.Paths) != 0 || repositoryGroup.Coverage.Uninspected != 0 || len(repositoryGroup.Groups) != 1 || repositoryGroup.SelectedGroup != group.SelectedGroup || len(repositoryGroup.Groups[0].Occurrences) != 3 {
		t.Fatal("repository expansion changed the revision or edit, or retained a scope filter", repositoryGroup)
	}
	code, text, stderr = run(fixture, "inspect", "queue", "--rev", revision, "--group", group.SelectedGroup)
	if code != 0 || strings.Contains(text, "Find same edit across repository:") {
		t.Fatal("repository view offers redundant expansion", code, text, stderr)
	}
	if fixture.Git("ls-files", "--stage") != index || fixture.Git("diff", "--") != working {
		t.Fatal("navigation mutated source or index")
	}
}

func TestWorkingReviewNavigationStaysWorkingAndEmptyQueuesHaveNoNextStep(t *testing.T) {
	fixture := testrepo.New(t)
	code, text, stderr := run(fixture, "inspect", "queue", "--files")
	if code != 0 || strings.Contains(text, "Open first file:") || strings.Contains(text, "Show all files:") {
		t.Fatal(code, text, stderr)
	}
	fixture.Write("crates/core/lib.rs", "working change\n")
	code, text, stderr = run(fixture, "inspect", "queue", "--files")
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	var queue analysis.FindingQueue
	if err := json.Unmarshal(replayCommand(t, text, "Open first file"), &queue); err != nil {
		t.Fatal(err)
	}
	if queue.Revision != "" || queue.Groups[0].After != "working change\n" {
		t.Fatal(queue)
	}
}
