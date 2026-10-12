package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/testrepo"
)

func TestCLIExceptionsThroughRealWriter(t *testing.T) {
	f := testrepo.New(t)
	land, e := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
	if e != nil {
		t.Fatal(e)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Save()
	f.Write("contract.md", "# Read\n")
	f.Commit()
	source := "// concave:begin read\n// why: retain read order\n// witness: tests::read\nfn first() { read(); }\n// concave:end read\nfn second() {}\n"
	path := "crates/core/lib.rs"
	f.Write(path, source)
	index := f.Git("ls-files", "--stage")
	n, out, err := run(f, "inspect", path, "--json")
	if n != 0 {
		t.Fatal(n, err)
	}
	var rep analysis.Report
	if e = json.Unmarshal([]byte(out), &rep); e != nil {
		t.Fatal(e)
	}
	args := []string{"exception", "record", "read", "--json"}
	for _, h := range rep.Hunks {
		args = append(args, "--hunk", h.ID)
	}
	n, out, err = recordReviewed(t, f, args...)
	if n != 0 || !strings.Contains(out, `"recorded"`) {
		t.Fatal(n, out, err)
	}
	n, out, err = run(f, "exception", "explain", "read", "--json")
	if n != 0 || !strings.Contains(out, `"valid"`) {
		t.Fatal(n, out, err)
	}
	f.Write(path, strings.Replace(source, "read();", "new_read();", 1))
	preview := func() exceptions.Proposal {
		t.Helper()
		n, out, err := run(f, "exception", "refresh", "read", "--json")
		if n != 0 {
			t.Fatal(n, out, err)
		}
		var p exceptions.Proposal
		if e = json.Unmarshal([]byte(out), &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	p := preview()
	if p.Status != "preview" || p.PolicyPatch == "" {
		t.Fatal(p)
	}
	if n, _, _ = run(f, "exception", "refresh", "read", "--write"); n != 2 {
		t.Fatal("write without review fingerprint", n)
	}
	f.Write(path, strings.Replace(source, "read();", "newer_read();", 1))
	if n, _, _ = run(f, "exception", "refresh", "read", "--write", "--expect", p.ProposalSHA256); n != 3 {
		t.Fatal("stale review accepted", n)
	}
	p = preview()
	f.Write(".git/land/sync-fixture/state.json", `{"phase":"applying"}`)
	if n, _, _ = run(f, "exception", "refresh", "read", "--write", "--expect", p.ProposalSHA256); n == 0 {
		t.Fatal("writer guard bypassed")
	}
	os.Remove(filepath.Join(f.Root, ".git/land/sync-fixture/state.json"))
	n, out, err = run(f, "exception", "refresh", "read", "--write", "--expect", p.ProposalSHA256, "--json")
	if n != 0 {
		t.Fatal(n, out, err)
	}
	if n, _, _ = run(f, "exception", "refresh", "read", "--write", "--expect", p.ProposalSHA256); n != 3 {
		t.Fatal("stale repeat accepted", n)
	}
	f.Write(path, "fn first() {}\nfn second() {}\n")
	p = preview()
	if p.Changes[0].Action != "remove" {
		t.Fatal(p)
	}
	n, out, err = run(f, "exception", "refresh", "read", "--write", "--expect", p.ProposalSHA256, "--json")
	if n != 0 {
		t.Fatal(n, out, err)
	}
	_, cfg := f.Open()
	if len(cfg.Exceptions) != 0 {
		t.Fatal(cfg.Exceptions)
	}
	if index != f.Git("ls-files", "--stage") {
		t.Fatal("shared index changed")
	}
}

func TestCLIFileExceptionsThroughRealWriter(t *testing.T) {
	for _, retired := range []bool{false, true} {
		name := "replacement"
		if retired {
			name = "retirement"
		}
		t.Run(name, func(t *testing.T) {
			f := testrepo.New(t)
			path := "crates/core/logo.png"
			original := "\x00original"
			if retired {
				path = "crates/core/retired.ts"
				original = "export const retired = true;\n"
			}
			f.Write(path, original)
			f.Config.Upstream = f.Commit()
			land, e := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
			if e != nil {
				t.Fatal(e)
			}
			f.Write("scripts/land.py", string(land))
			f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
			f.Write("contract.md", "# Branding\n")
			f.Save()
			f.Commit()
			if retired {
				if e = os.Remove(filepath.Join(f.Root, path)); e != nil {
					t.Fatal(e)
				}
			} else {
				f.Write(path, "\x00Bijection")
			}
			index := f.Git("ls-files", "--stage")
			args := []string{"exception", "record", "brand", "--file", "--path", path, "--why", "owned branding", "--witness", "crates/core/lib.rs"}
			preview := func() exceptions.Proposal {
				t.Helper()
				n, out, err := run(f, append(append([]string{}, args...), "--json")...)
				if n != 0 {
					t.Fatal(n, out, err)
				}
				var p exceptions.Proposal
				if e = json.Unmarshal([]byte(out), &p); e != nil {
					t.Fatal(e)
				}
				return p
			}
			p := preview()
			_, c := f.Open()
			if len(c.Exceptions) != 0 || p.Status != "preview" {
				t.Fatal("preview mutated policy", p, c.Exceptions)
			}
			for _, extra := range [][]string{{"--write"}, {"--expect", p.ProposalSHA256}} {
				if n, _, _ := run(f, append(append([]string{}, args...), extra...)...); n != 2 {
					t.Fatal("invalid review flags accepted", n)
				}
			}
			f.Write("crates/core/lib.rs", "fn revised_witness() {}\n")
			writeArgs := func(p exceptions.Proposal) []string {
				return append(append([]string{}, args...), "--write", "--expect", p.ProposalSHA256, "--json")
			}
			if n, _, _ := run(f, writeArgs(p)...); n != 3 {
				t.Fatal("stale file proposal accepted", n)
			}
			p = preview()
			f.Write(".git/land/sync-fixture/state.json", `{"phase":"applying"}`)
			if n, _, _ := run(f, writeArgs(p)...); n == 0 {
				t.Fatal("writer guard bypassed")
			}
			if e = os.Remove(filepath.Join(f.Root, ".git/land/sync-fixture/state.json")); e != nil {
				t.Fatal(e)
			}
			n, out, err := run(f, writeArgs(p)...)
			if n != 0 || !strings.Contains(out, `"recorded"`) {
				t.Fatal(n, out, err)
			}
			n, out, err = run(f, "exception", "explain", "brand", "--json")
			if n != 0 || !strings.Contains(out, `"valid"`) {
				t.Fatal(n, out, err)
			}
			// Committing the explicitly reviewed policy makes the scoped gate pass.
			f.Commit()
			index = f.Git("ls-files", "--stage")
			n, out, err = run(f, "check", path, "--json")
			if n != 0 {
				t.Fatal(n, out, err)
			}
			f.Write(path, original)
			n, out, err = run(f, "exception", "refresh", "brand", "--json")
			if n != 0 {
				t.Fatal(n, out, err)
			}
			if e = json.Unmarshal([]byte(out), &p); e != nil || p.Changes[0].Action != "remove" {
				t.Fatal(p, e)
			}
			_, c = f.Open()
			if len(c.Exceptions) != 1 {
				t.Fatal("refresh preview mutated policy")
			}
			n, out, err = run(f, "exception", "refresh", "brand", "--write", "--expect", p.ProposalSHA256, "--json")
			if n != 0 {
				t.Fatal(n, out, err)
			}
			if f.Git("ls-files", "--stage") != index {
				t.Fatal("shared index changed")
			}
		})
	}
}

func TestCLITOMLExceptionsThroughRealWriter(t *testing.T) {
	f := testrepo.New(t)
	path := "crates/core/Cargo.toml"
	original := "[package]\nname = \"core\"\nversion = \"1.0.0\"\n\n[dev-dependencies]\n"
	f.Write(path, original)
	f.Config.Upstream = f.Commit()
	land, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
	if err != nil {
		t.Fatal(err)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Write("contract.md", "# Tests\n")
	f.Save()
	f.Commit()
	source := original + "# concave:begin own-tests\n# why: unify the development feature graph\n#\n#\n# witness: crates/core/lib.rs\ncore = { path = \".\", features = [\"testing\"] }\n# concave:end own-tests\n"
	f.Write(path, source)
	index := f.Git("ls-files", "--stage")
	n, out, stderr := run(f, "inspect", path, "--json")
	var rep analysis.Report
	if n != 0 || json.Unmarshal([]byte(out), &rep) != nil || len(rep.Hunks) != 1 {
		t.Fatal(n, out, stderr)
	}
	n, out, stderr = recordReviewed(t, f, "exception", "record", "own-tests", "--path", path, "--hunk", rep.Hunks[0].ID, "--json")
	if n != 0 || !strings.Contains(out, `"recorded"`) {
		t.Fatal(n, out, stderr)
	}
	_, c := f.Open()
	if len(c.Exceptions) != 1 || !c.Exceptions[0].Fence || c.Exceptions[0].Reason != "" {
		t.Fatal("TOML explanation was not owned in source", c.Exceptions)
	}
	if f.Git("ls-files", "--stage") != index {
		t.Fatal("shared index changed")
	}
	rev := f.Commit()
	if n, out, stderr = run(f, "check", "--rev", rev, path); n != 0 {
		t.Fatal(n, out, stderr)
	}
	// Extra drift is still reported, even while the bounded exception is valid.
	f.Write(path, strings.Replace(source, "1.0.0", "2.0.0", 1))
	if n, out, stderr = run(f, "check", path, "--json"); n != 1 || !strings.Contains(out, `"unexplained"`) {
		t.Fatal(n, out, stderr)
	}
	if n, out, stderr = run(f, "exception", "explain", "own-tests", "--json"); n != 0 || !strings.Contains(out, `"status": "valid"`) {
		t.Fatal(n, out, stderr)
	}
	// Explanation edits preserve the code binding; committed reads retain their original explanation.
	f.Write(path, strings.Replace(source, "unify the development feature graph", "a changed reason", 1))
	if n, out, stderr = run(f, "check", path, "--json"); n != 0 || strings.Contains(out, `"stale_exception"`) || !strings.Contains(out, "a changed reason") {
		t.Fatal(n, out, stderr)
	}
	if n, out, stderr = run(f, "check", "--rev", rev, path); n != 0 {
		t.Fatal(n, out, stderr)
	}
	n, out, stderr = run(f, "exception", "refresh", "own-tests", "--json")
	var preview exceptions.Proposal
	if n != 0 || json.Unmarshal([]byte(out), &preview) != nil || preview.Status != "unchanged" {
		t.Fatal(n, out, stderr)
	}
	if n, out, stderr = run(f, "exception", "refresh", "own-tests", "--write", "--expect", preview.ProposalSHA256); n != 0 {
		t.Fatal(n, out, stderr)
	}
	f.Write(path, original)
	n, out, stderr = run(f, "exception", "refresh", "own-tests", "--json")
	if n != 0 || json.Unmarshal([]byte(out), &preview) != nil || preview.Changes[0].Action != "remove" {
		t.Fatal(n, out, stderr)
	}
}

func TestCLIProtobufExceptionsThroughRealWriter(t *testing.T) {
	f := testrepo.New(t)
	path := "crates/core/token.proto"
	original := "syntax = \"proto3\";\nmessage Token {\n  uint64 issued_s = 2;\n  optional string component = 3;\n}\n"
	f.Write(path, original)
	f.Config.Upstream = f.Commit()
	land, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
	if err != nil {
		t.Fatal(err)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Write("contract.md", "# Incarnation\n")
	f.Save()
	f.Commit()
	extension := "// concave:begin incarnation\n// why: bind callbacks to the admitting deployment lifetime\n// witness: crates/core/lib.rs\n  string incarnation = 10001;\n// concave:end incarnation\n"
	source := strings.Replace(original, "}\n", extension+"}\n", 1)
	f.Write(path, source)
	index := f.Git("ls-files", "--stage")
	n, out, stderr := run(f, "inspect", path, "--json")
	var report analysis.Report
	if n != 0 || json.Unmarshal([]byte(out), &report) != nil || len(report.Hunks) != 1 {
		t.Fatal(n, out, stderr)
	}
	n, out, stderr = recordReviewed(t, f, "exception", "record", "incarnation", "--path", path, "--hunk", report.Hunks[0].ID, "--json")
	if n != 0 || !strings.Contains(out, `"recorded"`) {
		t.Fatal(n, out, stderr)
	}
	_, config := f.Open()
	if len(config.Exceptions) != 1 || !config.Exceptions[0].Fence || config.Exceptions[0].Reason != "" {
		t.Fatal(config.Exceptions)
	}
	if f.Git("ls-files", "--stage") != index {
		t.Fatal("shared index changed")
	}
	rev := f.Commit()
	if n, out, stderr = run(f, "check", "--rev", rev, path); n != 0 {
		t.Fatal(n, out, stderr)
	}
	f.Write(path, strings.Replace(source, "issued_s = 2", "issued_s = 3", 1))
	if n, out, stderr = run(f, "check", path, "--json"); n != 1 || !strings.Contains(out, `"unexplained_change"`) {
		t.Fatal(n, out, stderr)
	}
	if n, out, stderr = run(f, "exception", "explain", "incarnation", "--json"); n != 0 || !strings.Contains(out, `"status": "valid"`) {
		t.Fatal(n, out, stderr)
	}
	f.Write(path, strings.Replace(source, "incarnation = 10001", "incarnation = 10002", 1))
	if n, out, stderr = run(f, "check", path, "--json"); n != 1 || !strings.Contains(out, `"stale_exception"`) {
		t.Fatal(n, out, stderr)
	}
	if n, out, stderr = run(f, "check", "--rev", rev, path); n != 0 {
		t.Fatal(n, out, stderr)
	}
	n, out, stderr = run(f, "exception", "refresh", "incarnation", "--json")
	var preview exceptions.Proposal
	if n != 0 || json.Unmarshal([]byte(out), &preview) != nil {
		t.Fatal(n, out, stderr)
	}
	if n, out, stderr = run(f, "exception", "refresh", "incarnation", "--write", "--expect", preview.ProposalSHA256); n != 0 {
		t.Fatal(n, out, stderr)
	}
	f.Write(path, original)
	n, out, stderr = run(f, "exception", "refresh", "incarnation", "--json")
	if n != 0 || json.Unmarshal([]byte(out), &preview) != nil || preview.Changes[0].Action != "remove" {
		t.Fatal(n, out, stderr)
	}
	if n, out, stderr = run(f, "exception", "refresh", "incarnation", "--write", "--expect", preview.ProposalSHA256); n != 0 {
		t.Fatal(n, out, stderr)
	}
	_, config = f.Open()
	if len(config.Exceptions) != 0 {
		t.Fatal(config.Exceptions)
	}
}

func TestCLIRecordsNativeExceptionBesideSharedRule(t *testing.T) {
	f := testrepo.New(t)
	path := "crates/core/lib.rs"
	original := "const BRAND: &str = \"convex\";\n\nfn first() {}\n\nfn second() {}\n"
	f.Write(path, original)
	f.Config.Upstream = f.Commit()
	land, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "scripts", "land.py"))
	if err != nil {
		t.Fatal(err)
	}
	f.Write("scripts/land.py", string(land))
	f.Config.Writer = []string{"python3", "scripts/land.py", "--edit", "--"}
	f.Config.Transforms = []policy.Transform{{ID: "public-name", Paths: []string{path}, Replacements: []policy.Replacement{{From: "\"convex\"", To: "\"bijection\""}}, Explanation: policy.Explanation{Why: "Expose the public name.", Witnesses: []string{path}}}}
	f.Save()
	f.Write("contract.md", "# Read\n")
	f.Commit()
	source := strings.Replace(original, "\"convex\"", "\"bijection\"", 1)
	body := "// concave:begin read\n// why: preserve native read ordering\n// witness: tests::read\nfn first() { let owner = \"convex\"; read(); }\n// concave:end read\n"
	f.Write(path, strings.Replace(source, "fn first() {}\n", body, 1))
	if n, out, err := recordReviewed(t, f, "exception", "record", "read", "--path", path, "--json"); n != 0 {
		t.Fatal(n, out, err)
	}
	if n, out, err := run(f, "exception", "explain", "read", "--json"); n != 0 || !strings.Contains(out, `"valid"`) {
		t.Fatal(n, out, err)
	}
	f.Commit()
	if n, out, err := run(f, "check", path, "--rev", "HEAD", "--json"); n != 0 {
		t.Fatal(n, out, err)
	}
	f.Write(path, strings.Replace(source, "fn first() {}\n", strings.Replace(body, "read();", "unreviewed();", 1), 1))
	if n, out, err := run(f, "check", path, "--json"); n != 1 || !strings.Contains(out, "stale_exception") {
		t.Fatal(n, out, err)
	}
}
