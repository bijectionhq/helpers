package testrepo

import (
	"os"
	"path/filepath"
	"runtime"
)

// Documents reuses the installed, pinned docs parser in a disposable Git fixture.
// The dependency symlink is ignored and never committed by Fixture.Commit.
func (f *Fixture) Documents() {
	f.T.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	f.Write(".gitignore", "target/\nnode_modules/\n")
	for _, name := range []string{".nvmrc", "npm-packages/docs/package.json", "npm-packages/pnpm-lock.yaml"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			f.T.Fatal(err)
		}
		f.Write(name, string(data))
	}
	if err := os.Symlink(filepath.Join(root, "npm-packages/docs/node_modules"), filepath.Join(f.Root, "npm-packages/docs/node_modules")); err != nil {
		f.T.Fatal(err)
	}
}
