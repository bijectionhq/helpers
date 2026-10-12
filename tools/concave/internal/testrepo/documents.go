package testrepo

import (
	"os"
	"path/filepath"
	"runtime"

	"concave/internal/document"
)

// Documents reuses the installed, pinned standalone parser in a disposable Git fixture.
// The dependency symlink is ignored and never committed by Fixture.Commit.
func (f *Fixture) Documents() {
	f.T.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	f.Write(".gitignore", "target/\nnode_modules/\n")
	for _, name := range document.RuntimeInputs() {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			f.T.Fatal(err)
		}
		f.Write(name, string(data))
	}
	if err := os.Symlink(filepath.Join(root, "tools/concave/testdata/mdx/node_modules"), filepath.Join(f.Root, "tools/concave/testdata/mdx/node_modules")); err != nil {
		f.T.Fatal(err)
	}
}
