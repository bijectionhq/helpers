package recovery_test

import (
	"concave/internal/recovery"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const creation = "diff --git a/packaging/native.ts b/packaging/native.ts\nnew file mode 100644\n--- /dev/null\n+++ b/packaging/native.ts\n@@ -0,0 +1 @@\n+export const value = 1;\n"

func TestExplicitCreationAndStaleDestinationAreAtomic(t *testing.T) {
	x := setup(t)
	patch := append(recovery.SinglePatch("crates/core/lib.rs", []byte("first fork\nfn second() {}\nthird fork\n"), []byte("fn first() {}\nfn second() {}\nthird fork\n")), []byte(creation)...)
	if _, err := recovery.Prepare(x.r, x.c, x.report, x.raw, nil, patch, "move native boundary"); err == nil {
		t.Fatal("implicit creation accepted")
	}
	p, err := recovery.Prepare(x.r, x.c, x.report, x.raw, nil, patch, "move native boundary", "packaging/native.ts")
	if err != nil {
		t.Fatal(err)
	}
	index := x.f.Git("ls-files", "--stage")
	x.f.Write("packaging/native.ts", "peer edit\n")
	if err = recovery.Apply(x.r, x.c, p); err == nil {
		t.Fatal("overwrote peer")
	}
	b, _ := os.ReadFile(filepath.Join(x.f.Root, "crates/core/lib.rs"))
	if !strings.HasPrefix(string(b), "first fork") {
		t.Fatal("partial apply")
	}
	if err = os.Remove(filepath.Join(x.f.Root, "packaging/native.ts")); err != nil {
		t.Fatal(err)
	}
	if err = recovery.Apply(x.r, x.c, p); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(x.f.Root, "packaging/native.ts"))
	if string(b) != "export const value = 1;\n" || x.f.Git("ls-files", "--stage") != index {
		t.Fatal("wrong postimage/index")
	}
}

func TestCreationRefusesIgnoredExecutableUnusedAndSymlink(t *testing.T) {
	for _, kind := range []string{"ignored", "executable", "unused", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			x := setup(t)
			patch := creation
			name := "packaging/native.ts"
			switch kind {
			case "ignored":
				name = "target/native.ts"
				patch = strings.ReplaceAll(patch, "packaging/native.ts", name)
			case "executable":
				patch = strings.ReplaceAll(patch, "100644", "100755")
			case "unused":
				name = "another.ts"
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(x.f.Root, "link")); err != nil {
					t.Fatal(err)
				}
				name = "link/native.ts"
				patch = strings.ReplaceAll(patch, "packaging/native.ts", name)
			}
			if _, err := recovery.CheckedPatch(x.r, []byte(patch), []string{name}, nil); err == nil {
				t.Fatal("unsafe creation accepted")
			}
		})
	}
}
