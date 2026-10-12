package document

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The parser owns its MDX, YAML and Markdown dependencies. Unrelated fixture
// and development tools are installed in the same workspace but never loaded.
const sampleLock = `lockfileVersion: '9.0'

settings:
  autoInstallPeers: true

overrides:
  left-pad: 1.3.0

importers:

  mdx:
    dependencies:
      '@mdx-js/mdx':
        specifier: ~1.0.0
        version: 1.0.0(peer@2.0.0)
      gray-matter:
        specifier: 1.0.0
        version: 1.0.0
      js-yaml:
        specifier: 1.0.0
        version: 1.0.0
      remark-gfm:
        specifier: 1.0.0
        version: 1.0.0
      library:
        specifier: workspace:*
        version: link:../library
    devDependencies:
      renamed:
        specifier: npm:actual@^3.0.0
        version: actual@3.0.0

  library:
    dependencies:
      leaf:
        specifier: ^1.0.0
        version: 1.0.0

  other:
    dependencies:
      unrelated:
        specifier: ^1.0.0
        version: 1.0.0

packages:

  '@mdx-js/mdx@1.0.0':
    resolution: {integrity: sha512-core}

  gray-matter@1.0.0:
    resolution: {integrity: sha512-matter}

  js-yaml@1.0.0:
    resolution: {integrity: sha512-yaml}

  remark-gfm@1.0.0:
    resolution: {integrity: sha512-gfm}

  actual@3.0.0:
    resolution: {integrity: sha512-actual}

  leaf@1.0.0:
    resolution: {integrity: sha512-leaf}

  optional@1.0.0:
    resolution: {integrity: sha512-optional}

  peer@2.0.0:
    resolution: {integrity: sha512-peer}

  unrelated@1.0.0:
    resolution: {integrity: sha512-unrelated}

snapshots:

  '@mdx-js/mdx@1.0.0(peer@2.0.0)':
    dependencies:
      peer: 2.0.0
    optionalDependencies:
      optional: 1.0.0
    transitivePeerDependencies:
      - supports-color

  gray-matter@1.0.0: {}

  js-yaml@1.0.0: {}

  remark-gfm@1.0.0: {}

  actual@3.0.0: {}

  leaf@1.0.0: {}

  optional@1.0.0: {}

  peer@2.0.0: {}

  unrelated@1.0.0: {}
`

func edited(t *testing.T, lock, old, new string) string {
	t.Helper()
	if strings.Count(lock, old) != 1 {
		t.Fatalf("fixture has %d of %q", strings.Count(lock, old), old)
	}
	return strings.Replace(lock, old, new, 1)
}

func TestParserLockIgnoresOtherProjects(t *testing.T) {
	want, err := parserLock([]byte(sampleLock))
	if err != nil {
		t.Fatal(err)
	}
	for name, lock := range map[string]string{
		"override":                  edited(t, sampleLock, "left-pad: 1.3.0", "left-pad: 1.3.1"),
		"unrelated package":         edited(t, sampleLock, "sha512-unrelated", "sha512-changed"),
		"unloaded SDK":              edited(t, sampleLock, "sha512-leaf", "sha512-changed"),
		"unloaded development tool": edited(t, sampleLock, "sha512-actual", "sha512-changed"),
		"SDK test dependency":       edited(t, sampleLock, "  library:\n    dependencies:", "  library:\n    devDependencies:\n      test-only:\n        specifier: 1.0.0\n        version: 1.0.0\n    dependencies:"),
		"unrelated importer":        edited(t, sampleLock, "      unrelated:\n        specifier: ^1.0.0", "      unrelated:\n        specifier: ~1.0.0"),
		"added project": edited(t, edited(t, edited(t, sampleLock,
			"\npackages:\n", "\n  added:\n    devDependencies:\n      fresh:\n        specifier: 1.0.0\n        version: 1.0.0\n\npackages:\n"),
			"\nsnapshots:\n", "\n  fresh@1.0.0:\n    resolution: {integrity: sha512-fresh}\n\nsnapshots:\n"),
			"  unrelated@1.0.0: {}\n", "  unrelated@1.0.0: {}\n\n  fresh@1.0.0: {}\n"),
	} {
		if got, err := parserLock([]byte(lock)); err != nil || got != want {
			t.Errorf("%s changed the parser: %v", name, err)
		}
		if !sameParserLock([]byte(sampleLock), []byte(lock)) {
			t.Errorf("%s: lockfiles install different parsers", name)
		}
	}
}

func TestParserLockBindsTheRuntimeClosure(t *testing.T) {
	want, err := parserLock([]byte(sampleLock))
	if err != nil {
		t.Fatal(err)
	}
	for name, lock := range map[string]string{
		"direct package":      edited(t, sampleLock, "sha512-core", "sha512-changed"),
		"peer":                edited(t, sampleLock, "sha512-peer", "sha512-changed"),
		"optional dependency": edited(t, sampleLock, "sha512-optional", "sha512-changed"),
		"parser importer":     edited(t, sampleLock, "specifier: ~1.0.0", "specifier: ^1.0.0"),
		"snapshot edge":       edited(t, sampleLock, "      - supports-color", "      - debug"),
		"settings":            edited(t, sampleLock, "autoInstallPeers: true", "autoInstallPeers: false"),
		"format":              edited(t, sampleLock, "lockfileVersion: '9.0'", "lockfileVersion: '9.1'"),
	} {
		got, err := parserLock([]byte(lock))
		if err != nil || got == want {
			t.Errorf("%s left the parser unchanged: %v", name, err)
		}
		if sameParserLock([]byte(sampleLock), []byte(lock)) {
			t.Errorf("%s: lockfiles install the same parser", name)
		}
	}
}

// A snapshot links a workspace project relative to the lockfile, not to itself.
func TestParserLockFollowsSnapshotLinks(t *testing.T) {
	lock := edited(t, edited(t, sampleLock,
		"      library:\n        specifier: workspace:*\n        version: link:../library\n", ""),
		"      peer: 2.0.0\n", "      peer: 2.0.0\n      library: link:library\n")
	want, err := parserLock([]byte(lock))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := parserLock([]byte(edited(t, lock, "sha512-leaf", "sha512-changed"))); err != nil || got == want {
		t.Fatal("a project linked from a snapshot is outside the closure", err)
	}
}

func TestParserLockRefusesWhatItCannotRead(t *testing.T) {
	for name, lock := range map[string]string{
		"missing snapshot":   edited(t, sampleLock, "  peer@2.0.0: {}\n", ""),
		"missing package":    edited(t, sampleLock, "  optional@1.0.0:\n    resolution: {integrity: sha512-optional}\n", ""),
		"missing runtime":    edited(t, sampleLock, "\n  mdx:\n", "\n  documents:\n"),
		"missing parser":     edited(t, sampleLock, "      '@mdx-js/mdx':", "      '@scope/unloaded':"),
		"missing version":    edited(t, sampleLock, "        version: 1.0.0(peer@2.0.0)\n", ""),
		"double-quoted key":  edited(t, sampleLock, "  actual@3.0.0: {}", `  "actual@3.0.0": {}`),
		"unterminated key":   edited(t, sampleLock, "  '@mdx-js/mdx@1.0.0':", "  '@mdx-js/mdx@1.0.0:"),
		"scalar entry":       edited(t, sampleLock, "  actual@3.0.0: {}", "  actual@3.0.0: text"),
		"duplicate entry":    edited(t, sampleLock, "  leaf@1.0.0: {}", "  leaf@1.0.0: {}\n\n  leaf@1.0.0: {}"),
		"tab":                edited(t, sampleLock, "  leaf@1.0.0: {}", "\tleaf@1.0.0: {}"),
		"odd indentation":    edited(t, sampleLock, "  leaf@1.0.0: {}", "   leaf@1.0.0: {}"),
		"scalar section":     edited(t, sampleLock, "\nsnapshots:\n", "\nsnapshots: none\n"),
		"no lockfileVersion": edited(t, sampleLock, "lockfileVersion: '9.0'\n", ""),
		"not a lockfile":     "- one\n- two\n",
	} {
		if digest, err := parserLock([]byte(lock)); err == nil {
			t.Errorf("%s was read as %s", name, digest)
		}
		if sameParserLock([]byte(lock), []byte(lock)) {
			t.Errorf("%s: an unreadable lockfile matched", name)
		}
	}
}

func TestSnapshotKeysFollowPnpmReferences(t *testing.T) {
	for _, c := range [][3]string{
		{"core", "1.0.0", "core@1.0.0"},
		{"core", "1.0.0(peer@2.0.0)", "core@1.0.0(peer@2.0.0)"},
		{"renamed", "actual@3.0.0", "actual@3.0.0"},
		{"renamed", "@scope/actual@3.0.0(peer@2.0.0)", "@scope/actual@3.0.0(peer@2.0.0)"},
		{"remote", "https://example.test/remote.tgz", "remote@https://example.test/remote.tgz"},
		{"@scope/remote", "@scope/remote@https://example.test/remote.tgz", "@scope/remote@https://example.test/remote.tgz"},
		{"local", "file:../local(peer@2.0.0)", "local@file:../local(peer@2.0.0)"},
	} {
		if got := snapshotKey(c[0], c[1]); got != c[2] {
			t.Errorf("%s %s: %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

// The repository's own lockfile is in the supported subset, a project outside
// the parser's closure can change freely, and a package the adapter loads
// cannot.
func TestInstalledLockfileBindsTheParser(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), lockPath))
	if err != nil {
		t.Fatal(err)
	}
	lock := string(data)
	want, err := parserLock(data)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := edited(t, lock, "\npackages:\n", "\n  concave-test-project:\n    dependencies:\n      gray-matter:\n        specifier: 0.0.0\n        version: 0.0.0\n\npackages:\n")
	unrelated = edited(t, unrelated, "\nsnapshots:\n", "\n  gray-matter@0.0.0:\n    resolution: {integrity: sha512-concave-test}\n\nsnapshots:\n")
	unrelated += "\n  gray-matter@0.0.0: {}\n"
	if got, err := parserLock([]byte(unrelated)); err != nil || got != want {
		t.Fatal("another project's resolution changed the parser", err)
	}
	loaded := regexp.MustCompile(`(?m)^  gray-matter@[^:\n]+:\n    resolution: \{integrity: `)
	if len(loaded.FindAllString(lock, -1)) != 1 {
		t.Fatal("the lockfile does not resolve the adapter's gray-matter exactly once")
	}
	changed := loaded.ReplaceAllString(lock, "${0}concave-test-")
	if got, err := parserLock([]byte(changed)); err != nil || got == want {
		t.Fatal("a package the adapter loads changed without changing the parser", err)
	}
}
