package environment

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func fixture(t *testing.T) *testrepo.Fixture {
	t.Helper()
	f := testrepo.New(t)
	node, err := exec.Command("node", "-p", "process.versions.node").Output()
	if err != nil {
		t.Fatal(err)
	}
	f.Write(".gitignore", "target/\nnode_modules/\n")
	f.Write(".nvmrc", string(node))
	f.Write("scripts/package.json", `{"devDependencies":{"pnpm":"11.15.1"}}`)
	f.Write("scripts/package-lock.json", "{}")
	f.Write("scripts/npm_toolchain.json", "{}")
	f.Write("npm-packages/package.json", `{"private":true}`)
	f.Write("npm-packages/pnpm-lock.yaml", "lockfileVersion: 9")
	f.Write("npm-packages/pnpm-workspace.yaml", "packages: [convex]")
	f.Write("npm-packages/convex/package.json", `{"name":"convex"}`)
	f.Config.Environments = map[string]policy.Environment{"sdk": {Directory: "npm-packages", Packages: []string{"convex"}, TimeoutSeconds: 10}}
	f.Save()
	f.Commit()
	return f
}

func TestEnvironmentBindsMembershipPinsAndPatches(t *testing.T) {
	for _, path := range []string{".nvmrc", "scripts/package-lock.json", "npm-packages/.npmrc", "npm-packages/patches/changed.patch", "npm-packages/new/package.json"} {
		t.Run(path, func(t *testing.T) {
			f := fixture(t)
			repo, config := f.Open()
			state, err := Capture(repo, "sdk", config.Environments["sdk"])
			if err != nil {
				t.Fatal(err)
			}
			state.SHA256, err = state.digest()
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(repo, config, state); err != nil {
				t.Fatal(err)
			}
			f.Write(path, "changed\n")
			if err := Validate(repo, config, state); !repository.IsStale(err) {
				t.Fatal("change not detected", err)
			}
		})
	}
}

func TestCandidatePreparesOnceWithFrozenIsolatedDependencies(t *testing.T) {
	f := fixture(t)
	// Exercise the real orchestration with deterministic local installer programs.
	// The actual pinned package manager is exercised by the SDK dogfood run.
	pnpm := `import fs from 'node:fs';
if(process.argv.includes('--version')) {console.log('11.15.1');}
else {
 if(!process.argv.includes('--frozen-lockfile') || !process.argv.includes('--ignore-scripts')) process.exit(9);
 fs.mkdirSync('node_modules',{recursive:true});
 fs.mkdirSync('convex/node_modules',{recursive:true});
 fs.appendFileSync('../target/installs','one\n');
}`
	encoded, _ := json.Marshal(pnpm)
	f.Write("scripts/fake-npm.mjs", "import fs from 'node:fs'; fs.mkdirSync('scripts/node_modules/pnpm/bin',{recursive:true}); fs.writeFileSync('scripts/node_modules/pnpm/bin/pnpm.mjs',"+string(encoded)+");")
	f.Write("scripts/qualification_toolchain.py", "import json, pathlib\nprint(json.dumps({'executables':{'npm':{'path':str(pathlib.Path('scripts/fake-npm.mjs').resolve())}}}))\n")
	f.Commit()
	t.Setenv("LAND_CANDIDATE_CHECK", "1")
	repo, config := f.Open()
	originalContext := repo.Context
	session := New(repo, config, filepath.Join(f.Root, "target/preparation"))
	first, err := session.Prepare("sdk")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Context != originalContext || first.Context == originalContext {
		t.Fatal("preparation changed repository context or omitted runtime settings")
	}
	second, err := session.Prepare("sdk")
	if err != nil || first.State.SHA256 != second.State.SHA256 {
		t.Fatal(first, second, err)
	}
	if err := ValidateRuntime(repo, first.State); err != nil {
		t.Fatal(err)
	}
	copy := *first.State
	copy.NodeSHA256 = strings.Repeat("0", 64)
	if err := ValidateRuntime(repo, &copy); !repository.IsStale(err) {
		t.Fatal("runtime identity was not admitted", err)
	}
	data, err := os.ReadFile(filepath.Join(f.Root, "target/installs"))
	if err != nil || string(data) != "one\n" {
		t.Fatal(string(data), err)
	}
	if dirty := f.Git("diff", "--name-only"); dirty != "" {
		t.Fatal("tracked source changed", dirty)
	}
	f.Write("npm-packages/new/package.json", `{"name":"new"}`)
	if _, err := session.Prepare("sdk"); !repository.IsStale(err) {
		t.Fatal("cached environment survived membership change", err)
	}
}

func TestWorkspaceLinksRejectOtherCheckouts(t *testing.T) {
	f := fixture(t)
	f.Write("npm-packages/node_modules/package/index.js", "value")
	f.Write("npm-packages/convex/node_modules/.keep", "")
	foreign := t.TempDir()
	link := filepath.Join(f.Root, "npm-packages/convex/node_modules/foreign")
	if err := os.Symlink(foreign, link); err != nil {
		t.Fatal(err)
	}
	if err := workspaceLinks(f.Root, f.Config.Environments["sdk"]); err == nil || !strings.Contains(err.Error(), "escapes candidate") {
		t.Fatal(err)
	}
}
