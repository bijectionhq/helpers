package command_test

import (
	"concave/internal/exceptions"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrintedExceptionCommandReplaysThroughWriter(t *testing.T) {
	fixture := contractFixture(t)
	path := "crates/core/art'work.bin"
	fixture.Write(path, "\x00original")
	fixture.Config.Upstream = fixture.Commit()
	land, err := os.ReadFile("../../../../scripts/land.py")
	if err != nil {
		t.Fatal(err)
	}
	fixture.Write("scripts/land.py", string(land))
	fixture.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	fixture.Save()
	fixture.Commit()
	fixture.Write(path, "\x00replacement")
	index := fixture.Git("ls-files", "--stage")
	reason := "Keep the owner's literal `name`, $HOME and $(printf expanded).\nPreserve both lines."
	removal := "Remove when upstream's asset matches; preserve $ and quotes."
	args := []string{"exception", "record", "artwork", "--file", "--path", path, "--why", reason, "--remove-when", removal,
		"--witness", "crates/core/lib.rs", "--witness", "crates/core/adapter.rs::authenticate",
		"--preserves", "crates/core/adapter.rs#audience-binding", "--preserves", "contracts/auth.rs#authentication"}
	code, text, stderr := run(fixture, args...)
	if code != 0 {
		t.Fatal(code, text, stderr)
	}
	start := strings.LastIndex(text, "Write this proposal: concave ")
	end := strings.LastIndex(text, "\nWitnesses were not executed;")
	if start < 0 || end <= start {
		t.Fatal("missing complete replay command", text)
	}
	suggestion := text[start+len("Write this proposal: concave ") : end]
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Run exactly the rendered arguments from a different directory. TestMain
	// supplies the CLI executable; the configured real land.py owns writer entry.
	command := exec.Command("sh", "-c", `exec "$1" `+suggestion+" --json", "concave-replay", executable)
	command.Dir = t.TempDir()
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("replay: %v: %s", err, data)
	}
	var proposal exceptions.Proposal
	if err := json.Unmarshal(data, &proposal); err != nil || proposal.Status != exceptions.Recorded {
		t.Fatal(string(data), err)
	}
	next := proposal.Changes[0].Next
	if next.Path != path || next.Reason != reason || next.RemoveWhen != removal || !reflect.DeepEqual(next.Witnesses, []string{"crates/core/lib.rs", "crates/core/adapter.rs::authenticate"}) || len(next.Preserves) != 2 {
		t.Fatalf("argument changed in replay: %+v", next)
	}
	content, err := os.ReadFile(filepath.Join(fixture.Root, path))
	if err != nil || string(content) != "\x00replacement" || index != fixture.Git("ls-files", "--stage") {
		t.Fatal("replay changed source or index", err)
	}
	code, text, stderr = run(fixture, "exception", "explain", "artwork")
	if code != 0 || !strings.Contains(text, "artwork · valid") || !strings.Contains(text, reason) {
		t.Fatal(code, text, stderr)
	}
}
