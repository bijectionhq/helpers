package process

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInvocationEnvironmentResolvesAndExecutesTheSameTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concave-probe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$CONCAVE_PROBE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithEnvironment(context.Background(), "PATH="+dir, "CONCAVE_PROBE=isolated")
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err := ExecutableIn(ctx, dir, "concave-probe")
	if err != nil || resolved != path {
		t.Fatal(resolved, err)
	}
	result, err := Run(ctx, dir, []string{"concave-probe"}, nil, 4096)
	if err != nil || string(result.Stdout) != "isolated" || !result.Started {
		t.Fatal(result, err)
	}
	if os.Getenv("CONCAVE_PROBE") == "isolated" {
		t.Fatal("changed host environment")
	}
}

// The pinned runtime comes from the declared tool manager before PATH, and
// every candidate must report the exact version.
func TestNodeSelectsTheDeclaredManagersExactInstallation(t *testing.T) {
	t.Setenv("CONCAVE_NODE", "")
	dir := t.TempDir()
	t.Setenv("NVM_DIR", filepath.Join(dir, "nvm"))
	script := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	bin, install := filepath.Join(dir, "bin"), filepath.Join(dir, "installs", "node", "22.22.2")
	script(filepath.Join(bin, "node"), "echo 22.0.0")
	script(filepath.Join(install, "bin", "node"), "echo 22.22.2")
	ctx := WithEnvironment(context.Background(), "PATH="+bin)
	if _, err := Node(ctx, dir, "22.22.2"); err == nil {
		t.Fatal("selected a runtime reporting another version")
	}
	script(filepath.Join(bin, "mise"), `[ "$1 $2" = "where node@22.22.2" ] && echo `+install)
	want, err := filepath.EvalSymlinks(filepath.Join(install, "bin", "node"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Node(ctx, dir, "22.22.2"); err != nil || got != want {
		t.Fatal(got, err)
	}
	if _, err := Node(ctx, dir, "22.22.3"); err == nil {
		t.Fatal("selected a runtime the manager does not install")
	}
}

// Without a tool manager and with another version on PATH, the pinned runtime
// is found where the repository's other tooling keeps an exact version: the
// distribution under target/core-tools, then nvm. Neither substitutes a
// neighbouring version.
func TestNodeFindsTheExactVersionInRepositoryAndNvmInstallations(t *testing.T) {
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	if arch == "" {
		t.Skip("no official Node distribution name for " + runtime.GOARCH)
	}
	t.Setenv("CONCAVE_NODE", "")
	dir := t.TempDir()
	nvm := filepath.Join(dir, "nvm")
	t.Setenv("NVM_DIR", nvm)
	script := func(path, body string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	bin := filepath.Join(dir, "bin")
	script(filepath.Join(bin, "node"), "echo 26.8.2")
	ctx := WithEnvironment(context.Background(), "PATH="+bin)
	inNvm := script(filepath.Join(nvm, "versions", "node", "v22.22.2", "bin", "node"), "echo 22.22.2")
	if got, err := Node(ctx, dir, "22.22.2"); err != nil || got != inNvm {
		t.Fatal(got, err)
	}
	local := script(filepath.Join(dir, "target", "core-tools", "node-v22.22.2-"+runtime.GOOS+"-"+arch, "bin", "node"), "echo 22.22.2")
	if got, err := Node(ctx, dir, "22.22.2"); err != nil || got != local {
		t.Fatal(got, err)
	}
	// A directory named for the version whose runtime reports another is refused.
	script(filepath.Join(dir, "target", "core-tools", "node-v22.22.3-"+runtime.GOOS+"-"+arch, "bin", "node"), "echo 22.22.2")
	if _, err := Node(ctx, dir, "22.22.3"); err == nil {
		t.Fatal("selected a runtime reporting another version")
	}
}
