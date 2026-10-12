package document

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneRuntimeNeedsNoApplicationDocumentation(t *testing.T) {
	installed := root(t)
	standalone := t.TempDir()
	for _, name := range RuntimeInputs() {
		data, err := os.ReadFile(filepath.Join(installed, name))
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(standalone, name)
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	modules := "tools/concave/testdata/mdx/node_modules"
	if err := os.Symlink(filepath.Join(installed, modules), filepath.Join(standalone, modules)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(standalone, "npm-packages/docs")); !os.IsNotExist(err) {
		t.Fatal("fixture has application documentation")
	}
	engine := New(context.Background(), standalone)
	read := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(standalone, name)) }
	if err := engine.ValidateRuntime(read); err != nil {
		t.Fatal(err)
	}
	facts, err := engine.Parse("BIJECTION.md", "<!-- concave:begin custody -->\n# Custody\n<!-- concave:end custody -->\n")
	if err != nil || facts.Error != "" || len(facts.Comments) != 2 {
		t.Fatalf("%v %+v", err, facts)
	}
	if err := engine.ValidateRuntime(func(name string) ([]byte, error) {
		data, err := read(name)
		if name == nodePinPath {
			return []byte("0.0.0\n"), nil
		}
		return data, err
	}); err == nil || !strings.Contains(err.Error(), nodePinPath) {
		t.Fatal("different selected Node pin was accepted", err)
	}
}
