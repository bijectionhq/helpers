package surface

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompilerModulesArePrivateAndRemoved(t *testing.T) {
	entry, cleanup, err := materializeAdapter()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	dir := filepath.Dir(entry)
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal(info, err)
	}
	for _, name := range []string{"main.mjs", "inputs.mjs", "compiler.mjs", "signatures.mjs", "runtime.mjs", "exports.mjs", "bindings.mjs", "callers.mjs"} {
		info, err = os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(name, info, err)
		}
	}
	cleanup()
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("compiler directory survives cleanup", err)
	}
}
