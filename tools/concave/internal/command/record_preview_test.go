package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

// Existing explicit-hunk cases use the same preview/write path as file bindings.
func recordReviewed(t *testing.T, f *testrepo.Fixture, args ...string) (int, string, string) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(f.Root, "concave.json"))
	if err != nil {
		t.Fatal(err)
	}
	n, out, stderr := run(f, args...)
	var p exceptions.Proposal
	if n != 0 || json.Unmarshal([]byte(out), &p) != nil || p.Status != "preview" {
		t.Fatal(n, out, stderr)
	}
	after, err := os.ReadFile(filepath.Join(f.Root, "concave.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("record preview edited policy", err)
	}
	return run(f, append(append([]string{}, args...), "--write", "--expect", p.ProposalSHA256)...)
}

func TestCLIFencedRecordPreviewAndStaleReview(t *testing.T) {
	for _, change := range []string{"source", "reference", "policy", "head", "unrelated binding"} {
		t.Run(change, func(t *testing.T) {
			f := testrepo.New(t)
			land, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
			if err != nil {
				t.Fatal(err)
			}
			f.Write("scripts/land.py", string(land))
			f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
			f.Save()
			f.Write("witness.rs", "fn read() {}\n")
			f.Commit()
			path := "crates/core/lib.rs"
			source := "// concave:begin read\n// why: retain read order\n// witness: witness.rs::read\nfn first() { read(); }\n// concave:end read\nfn second() {}\n\nfn unrelated() {}\n"
			f.Write(path, source)
			index := f.Git("ls-files", "--stage")
			preview := func() exceptions.Proposal {
				t.Helper()
				n, out, stderr := run(f, "exception", "record", "read", "--json")
				var p exceptions.Proposal
				if n != 0 || json.Unmarshal([]byte(out), &p) != nil || p.Status != "preview" || len(p.Changes[0].Hunks) != 1 || strings.Contains(p.Changes[0].SourceDiff, "unrelated") {
					t.Fatal(n, out, stderr)
				}
				_, c := f.Open()
				for _, x := range c.Exceptions {
					if x.ID == "read" {
						t.Fatal("preview changed policy")
					}
				}
				return p
			}
			p := preview()
			n, out, stderr := run(f, "exception", "record", "read")
			if n != 0 || !strings.Contains(out, "exception record read --write --expect "+p.ProposalSHA256) {
				t.Fatal(n, out, stderr)
			}
			for _, extra := range [][]string{{"--write"}, {"--expect", p.ProposalSHA256}} {
				if n, _, _ := run(f, append([]string{"exception", "record", "read"}, extra...)...); n != 2 {
					t.Fatal("invalid review flags accepted", n)
				}
			}
			switch change {
			case "source":
				source = strings.Replace(source, "read();", "new_read();", 1)
				f.Write(path, source)
			case "reference":
				f.Write("witness.rs", "fn read() { revised(); }\n")
			case "policy":
				f.Config.Families[0].Title += " (edited)"
				f.Save()
			case "head":
				f.Write("peer.txt", "peer change\n")
				f.Commit()
				index = f.Git("ls-files", "--stage")
			case "unrelated binding":
				// A peer approval of another path writes only that path's file.
				f.Config.Exceptions = []policy.Exception{{ID: "peer", Path: "npm-packages/convex/index.ts", UpstreamSHA256: strings.Repeat("a", 64), ReplacementSHA256: strings.Repeat("b", 64), Start: 0, End: 1, Reason: "peer", Witnesses: []string{"witness.rs"}}}
				f.Save()
			}
			write := func(p exceptions.Proposal) (int, string, string) {
				return run(f, "exception", "record", "read", "--write", "--expect", p.ProposalSHA256, "--json")
			}
			if change == "head" || change == "unrelated binding" {
				// Neither HEAD nor another path's binding is an input of this proposal.
				if again := preview(); again.ProposalSHA256 != p.ProposalSHA256 {
					t.Fatal("unrelated change altered the proposal fingerprint")
				}
			} else {
				if n, _, _ := write(p); n != 3 {
					t.Fatal("stale fenced review accepted", n)
				}
				p = preview()
			}
			f.Write(".git/land/sync-fixture/state.json", `{"phase":"applying"}`)
			if n, _, _ := write(p); n == 0 {
				t.Fatal("writer guard bypassed")
			}
			if err := os.Remove(filepath.Join(f.Root, ".git/land/sync-fixture/state.json")); err != nil {
				t.Fatal(err)
			}
			if n, out, stderr := write(p); n != 0 || !strings.Contains(out, `"recorded"`) {
				t.Fatal(n, out, stderr)
			}
			n, out, stderr = run(f, "exception", "explain", "read", "--json")
			if n != 0 || !strings.Contains(out, `"status": "valid"`) || !strings.Contains(out, `"file_classification": "unexplained"`) {
				t.Fatal(n, out, stderr)
			}
			actual, err := os.ReadFile(filepath.Join(f.Root, path))
			if err != nil || string(actual) != source || f.Git("ls-files", "--stage") != index {
				t.Fatal("record changed source or index", err)
			}
		})
	}
}
