package command_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/exceptions"
	"concave/internal/testrepo"
)

func TestCLIBatchPreviewAndAtomicRealWriter(t *testing.T) {
	f := testrepo.New(t)
	land, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
	if err != nil {
		t.Fatal(err)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Save()
	f.Write("contract.md", "# Read\n")
	f.Commit()
	header := "// why: retain the native contract\n// witness: tests::read\n"
	sources := map[string]string{
		"crates/core/lib.rs":           "// concave:begin read\n" + header + "fn first() { read(); }\n// concave:end read\nfn second() {}\n\nfn unrelated() {}\n",
		"npm-packages/convex/index.ts": "// concave:begin display\n" + header + "export const value = 2;\n// concave:end display\n",
	}
	for p, s := range sources {
		f.Write(p, s)
	}
	index := f.Git("ls-files", "--stage")
	before, err := os.ReadFile(filepath.Join(f.Root, "concave.json"))
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func() {
		t.Helper()
		after, err := os.ReadFile(filepath.Join(f.Root, "concave.json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("partial batch write", err)
		}
	}
	preview := func() exceptions.Proposal {
		t.Helper()
		n, out, stderr := run(f, "exception", "record", "read", "display", "--json")
		var p exceptions.Proposal
		if n != 0 || json.Unmarshal([]byte(out), &p) != nil || p.Status != "preview" || len(p.Changes) != 2 || p.Changes[0].Next.ID != "display" {
			t.Fatal(n, out, stderr)
		}
		unchanged()
		return p
	}
	p := preview()
	if n, out, stderr := run(f, "exception", "record", "read", "display"); n != 0 || !strings.Contains(out, "exception record display read --write --expect "+p.ProposalSHA256) {
		t.Fatal(n, out, stderr)
	}
	for _, args := range [][]string{
		{"exception", "record", "read", "display", "--write"},
		{"exception", "record", "read", "display", "--expect", p.ProposalSHA256},
		{"exception", "record", "read", "read"},
		{"exception", "record", "display", "missing"},
		{"exception", "record", "read", "display", "--file"},
		{"exception", "record", "read", "display", "--path", "crates/core/lib.rs"},
		{"exception", "record", "read", "display", "--hunk", "unsupported"},
		{"exception", "record", "read", "display", "--why", "outside source"},
		{"exception", "refresh", "read", "display"},
	} {
		if n, out, stderr := run(f, args...); n != 2 {
			t.Fatal(args, n, out, stderr)
		}
		unchanged()
	}
	write := func(p exceptions.Proposal) (int, string, string) {
		return run(f, "exception", "record", "read", "display", "--write", "--expect", p.ProposalSHA256, "--json")
	}
	sources["crates/core/lib.rs"] = strings.Replace(sources["crates/core/lib.rs"], "read();", "read_more();", 1)
	f.Write("crates/core/lib.rs", sources["crates/core/lib.rs"])
	if n, out, stderr := write(p); n != 3 {
		t.Fatal("stale batch accepted", n, out, stderr)
	}
	unchanged()
	p = preview()
	f.Write(".git/land/sync-fixture/state.json", `{"phase":"applying"}`)
	if n, _, _ := write(p); n == 0 {
		t.Fatal("batch bypassed real writer guard")
	}
	unchanged()
	if err := os.Remove(filepath.Join(f.Root, ".git/land/sync-fixture/state.json")); err != nil {
		t.Fatal(err)
	}
	if n, out, stderr := write(p); n != 0 || !strings.Contains(out, `"status": "recorded"`) {
		t.Fatal(n, out, stderr)
	}
	_, c := f.Open()
	if len(c.Exceptions) != 2 {
		t.Fatal(c.Exceptions)
	}
	for _, id := range []string{"read", "display"} {
		if n, out, stderr := run(f, "exception", "explain", id, "--json"); n != 0 || !strings.Contains(out, `"status": "valid"`) {
			t.Fatal(n, out, stderr)
		}
	}
	if n, out, stderr := run(f, "inspect", "crates/core/lib.rs", "--json"); n != 0 || !strings.Contains(out, `"unexplained_change"`) {
		t.Fatal("batch hid unrelated source", n, out, stderr)
	}
	for p, expected := range sources {
		actual, err := os.ReadFile(filepath.Join(f.Root, p))
		if err != nil || string(actual) != expected {
			t.Fatal("batch edited source", p, err)
		}
	}
	if index != f.Git("ls-files", "--stage") {
		t.Fatal("batch edited index")
	}
}
