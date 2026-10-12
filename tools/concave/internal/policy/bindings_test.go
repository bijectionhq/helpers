package policy_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func binding(id, path string) policy.Exception {
	return policy.Exception{ID: id, Path: path, Start: 0, End: 1, UpstreamSHA256: strings.Repeat("a", 64), ReplacementSHA256: strings.Repeat("b", 64), Reason: "reason", Witnesses: []string{"crates/core/lib.rs"}}
}

func TestBindingFilesAreScopedPolicyIdentity(t *testing.T) {
	f := testrepo.New(t)
	r, empty := f.Open()
	if len(empty.Exceptions) != 0 || len(empty.Bindings) != 0 {
		t.Fatal("no binding directory means no bindings", empty.Exceptions)
	}
	f.Config.Exceptions = []policy.Exception{binding("core", "crates/core/lib.rs")}
	f.Save()
	data, err := os.ReadFile(filepath.Join(f.Root, "concave.exceptions/crates/core/lib.rs.json"))
	if err != nil || !strings.HasPrefix(string(data), "{\n  \"format\": 1,\n  \"exceptions\": [\n") {
		t.Fatalf("binding file is not at its mirrored path: %v %q", err, data)
	}
	c, err := policy.Load(r, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Exceptions) != 1 || c.SHA256 == empty.SHA256 {
		t.Fatal("binding file not read or not part of policy identity")
	}
	core, other := []string{"crates/core/lib.rs"}, []string{"npm-packages/convex/index.ts"}
	if c.ScopedSHA256(core) == empty.ScopedSHA256(core) || c.ScopedSHA256(other) != empty.ScopedSHA256(other) {
		t.Fatal("scoped identity must follow exactly its own paths' binding files")
	}
	commit := f.Commit()
	at, err := policy.Load(r, commit)
	if err != nil || at.SHA256 != c.SHA256 || len(at.Exceptions) != 1 {
		t.Fatal("revision and working reads disagree", err)
	}
	// A tracked deletion is absence, never a stale read of the index.
	if err := os.RemoveAll(filepath.Join(f.Root, policy.BindingsDirectory)); err != nil {
		t.Fatal(err)
	}
	if gone, err := policy.Load(r, ""); err != nil || len(gone.Exceptions) != 0 || gone.SHA256 != empty.SHA256 {
		t.Fatal("deleted binding file still read", err)
	}
}

func TestBindingFilesRefuseAmbiguity(t *testing.T) {
	file := "concave.exceptions/crates/core/lib.rs.json"
	valid := func(f *testrepo.Fixture) {
		f.Config.Exceptions = []policy.Exception{binding("core", "crates/core/lib.rs")}
		f.Save()
	}
	for name, mutate := range map[string]func(*testrepo.Fixture){
		"unknown field": func(f *testrepo.Fixture) {
			f.Write(file, `{"format":1,"exceptions":[],"extra":true}`)
		},
		"empty": func(f *testrepo.Fixture) { f.Write(file, `{"format":1,"exceptions":[]}`) },
		"format": func(f *testrepo.Fixture) {
			valid(f)
			data, _ := os.ReadFile(filepath.Join(f.Root, file))
			f.Write(file, strings.Replace(string(data), `"format": 1`, `"format": 2`, 1))
		},
		"other path": func(f *testrepo.Fixture) {
			valid(f)
			data, _ := os.ReadFile(filepath.Join(f.Root, file))
			f.Write("concave.exceptions/crates/core/other.rs.json", string(data))
			os.Remove(filepath.Join(f.Root, file))
		},
		"stray file": func(f *testrepo.Fixture) { f.Write("concave.exceptions/README.md", "notes\n") },
		"duplicate ID": func(f *testrepo.Fixture) {
			f.Config.Exceptions = []policy.Exception{binding("core", "crates/core/lib.rs"), binding("core", "npm-packages/convex/index.ts")}
			f.Save()
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := testrepo.New(t)
			mutate(f)
			r, err := repository.Open(context.Background(), f.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			_, err = policy.Load(r, "")
			if err == nil {
				t.Fatal("ambiguous binding file accepted")
			}
			if name == "duplicate ID" && !strings.Contains(err.Error(), "concave.exceptions/crates/core/lib.rs.json and concave.exceptions/npm-packages/convex/index.ts.json") {
				t.Fatal("duplicate ID must name both binding files:", err)
			}
		})
	}
}
