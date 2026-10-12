package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/repository"
)

func TestReceiptSourceChangesNameContentModeAndInventory(t *testing.T) {
	fixture := verificationFixture(t)
	fixture.Write("crates/core/gone.rs", "remove me\n")
	fixture.Write("crates/core/mode.rs", "mode only\n")
	fixture.Commit()
	repo, config := fixture.Open()
	receipt, err := Run(repo, config, "local", filepath.Join(fixture.Root, "target", "run"))
	if err != nil || receipt.Status != Passed {
		t.Fatal(receipt, err)
	}
	fixture.Write("crates/core/lib.rs", "changed content\n")
	fixture.Write("crates/core/added.rs", "new input\n")
	if err := os.Remove(filepath.Join(fixture.Root, "crates/core/gone.rs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(fixture.Root, "crates/core/mode.rs"), 0700); err != nil {
		t.Fatal(err)
	}
	working, index := fixture.Git("diff", "--"), fixture.Git("ls-files", "--stage")
	err = Validate(repo, config, receipt)
	want := "receipt source inputs changed (4):\n  added \"crates/core/added.rs\"\n  removed \"crates/core/gone.rs\"\n  changed \"crates/core/lib.rs\"\n  changed \"crates/core/mode.rs\""
	if !repository.IsStale(err) || err.Error() != want {
		t.Fatal(err)
	}
	if fixture.Git("diff", "--") != working || fixture.Git("ls-files", "--stage") != index {
		t.Fatal("validation edited source or index")
	}
}

func TestInputChangeDiagnosticsAreBoundedAndPreserveMissingInputs(t *testing.T) {
	before, after := repository.Inputs{}, repository.Inputs{}
	state := &repository.State{Kind: "file", Mode: "100644", OID: "identity", SHA256: "bytes", Size: 5}
	for i := 21; i >= 0; i-- {
		after[fmt.Sprintf("input-%02d", i)] = state
	}
	got := inputChanges(before, after)
	if !strings.HasPrefix(got, " (22):\n  added \"input-00\"") || !strings.HasSuffix(got, "\n  … 2 more changed inputs") || strings.Contains(got, "input-20") || strings.Count(got, "\n  added ") != 20 || inputChanges(before, after) != got {
		t.Fatal(got)
	}
	absent := repository.Inputs{"missing": nil}
	present := repository.Inputs{"missing": state}
	for _, tc := range []struct {
		before, after repository.Inputs
		want          string
	}{
		{absent, present, "added"}, {present, absent, "removed"},
		{before, absent, "added"}, {absent, before, "removed"},
	} {
		if got := inputChanges(tc.before, tc.after); got != " (1):\n  "+tc.want+" \"missing\"" {
			t.Fatal(got)
		}
	}
	if inputChanges(after, after) != "" {
		t.Fatal("unchanged evidence became stale")
	}
}
