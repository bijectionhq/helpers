// Package testrepo constructs small, disposable histories for tool tests.
package testrepo

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/syntax"
)

type Fixture struct {
	T          testing.TB
	Root, Base string
	Config     *policy.Config
}

func New(t testing.TB) *Fixture {
	t.Helper()
	// A fixture is a fresh repository without its own structural engine; it
	// uses the one installed in this checkout unless the caller selected one.
	if os.Getenv("CONCAVE_AST_GREP") == "" {
		_, file, _, _ := runtime.Caller(0)
		t.Setenv("CONCAVE_AST_GREP", filepath.Join(filepath.Dir(file), "../../../../target/concave/engine", syntax.Version, "ast-grep"))
	}
	f := &Fixture{T: t, Root: t.TempDir()}
	f.Git("init", "--initial-branch=main")
	f.Git("config", "core.fileMode", "true")
	f.Write(".gitignore", "target/\n")
	f.Write("crates/core/lib.rs", "fn first() {}\nfn second() {}\n")
	f.Write("npm-packages/convex/index.ts", "export const value = 1;\n")
	f.Write("npm-packages/client/protocol.ts", "export type Request = string;\n")
	f.Base = f.Commit()
	f.Config = &policy.Config{Format: 2, Upstream: f.Base, Roots: []string{"crates", "npm-packages"}, Families: []policy.Family{{ID: "engine", Title: "Engine", Paths: []string{"crates/"}}, {ID: "packages", Title: "Packages", Paths: []string{"npm-packages/"}}, {ID: "protocol", Title: "Protocol", Paths: []string{"npm-packages/client/protocol.ts"}}}, PreserveExactly: []string{"npm-packages/client/protocol.ts"}, Cases: map[string]policy.Case{"authentication": {Surfaces: []policy.Surface{{ID: "core", Purpose: "inherited source", Paths: []string{"crates/core/"}}, {ID: "packaging", Purpose: "execution boundaries", Paths: []string{"packaging/"}}}, Probes: []policy.Probe{{ID: "brand", Path: "packaging/facade.ts", Needles: []string{"bijection"}, Meaning: "intentional spelling"}}, ReviewOrder: []string{"review the facade"}, Verification: []string{"run auth tests"}}}}
	f.Write("packaging/facade.ts", "export const audience = 'bijection';\n")
	f.Save()
	f.Commit()
	return f
}
func (f *Fixture) Write(p, s string) {
	f.T.Helper()
	dst := filepath.Join(f.Root, p)
	if e := os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
		f.T.Fatal(e)
	}
	if e := os.WriteFile(dst, []byte(s), 0600); e != nil {
		f.T.Fatal(e)
	}
}
func (f *Fixture) Git(args ...string) string {
	f.T.Helper()
	// Fixture mutations must not leave detached maintenance writing into a
	// TempDir after the command and its test have finished.
	argv := append([]string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)
	c := exec.Command("git", argv...)
	c.Dir = f.Root
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Concave fixture", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Concave fixture", "GIT_COMMITTER_EMAIL=test@example.invalid")
	b, e := c.CombinedOutput()
	if e != nil {
		f.T.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func (f *Fixture) Commit() string {
	f.T.Helper()
	f.Git("add", "--all")
	f.Git("commit", "-m", "fixture")
	return f.Git("rev-parse", "HEAD")
}
func (f *Fixture) Save() {
	b, e := json.MarshalIndent(f.Config, "", "  ")
	if e != nil {
		f.T.Fatal(e)
	}
	f.Write(policy.File, string(b)+"\n")
	if e := os.RemoveAll(filepath.Join(f.Root, policy.BindingsDirectory)); e != nil {
		f.T.Fatal(e)
	}
	byPath := map[string][]policy.Exception{}
	for _, x := range f.Config.Exceptions {
		byPath[x.Path] = append(byPath[x.Path], x)
	}
	for path, xs := range byPath {
		bindings, e := policy.EncodeBindings(path, xs)
		if e != nil {
			f.T.Fatal(e)
		}
		f.Write(policy.BindingPath(path), string(bindings))
	}
}
func (f *Fixture) Open() (*repository.Repo, *policy.Config) {
	f.T.Helper()
	r, e := repository.Open(context.Background(), f.Root)
	if e != nil {
		f.T.Fatal(e)
	}
	f.T.Cleanup(r.Close)
	c, e := policy.Load(r, "")
	if e != nil {
		f.T.Fatal(e)
	}
	return r, c
}
