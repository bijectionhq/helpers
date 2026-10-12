package document

import (
	"bytes"
	"concave/internal/repository"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// RuntimeInputs identifies the installed parser. Callers bind these inputs to
// their captured source; parsing never installs dependencies or builds a site.
func RuntimeInputs() []string {
	return []string{".nvmrc", "npm-packages/docs/package.json", lockPath}
}
func RecoveryInputs() []string {
	return append(RuntimeInputs(), "npm-packages/docs/src/snippet.tsx", "npm-packages/docs/recovery/snippets.mjs", "npm-packages/docs/recovery/migrate.mjs", "npm-packages/docs/recovery/content.mjs", "npm-packages/bijection/scripts/facade.mjs", "npm-packages/bijection/scripts/parity-transforms.json")
}
func Native(path string) bool {
	return strings.HasPrefix(path, "npm-packages/docs/docs/") && strings.HasSuffix(path, ".mdx")
}

// siteBase and snippetBase are the directories the @site/ and /snippets/
// aliases join their remainder to.
const siteBase, snippetBase = "npm-packages/docs", "apps/docs/snippets"

func ImportPath(owner, specifier string) (string, bool, error) {
	specifier = strings.TrimPrefix(specifier, "!!raw-loader!")
	var resolved string
	switch {
	case strings.HasPrefix(specifier, "@site/"):
		resolved = path.Join(siteBase, strings.TrimPrefix(specifier, "@site/"))
	case strings.HasPrefix(specifier, "/snippets/"):
		resolved = path.Join(snippetBase, strings.TrimPrefix(specifier, "/snippets/"))
	case strings.HasPrefix(specifier, "./"), strings.HasPrefix(specifier, "../"):
		resolved = path.Join(path.Dir(owner), specifier)
	default:
		return "", false, nil
	}
	if !repository.ValidPath(resolved) {
		return "", false, fmt.Errorf("document import escapes repository: %s", specifier)
	}
	return resolved, true, nil
}

// ValidateRuntime prevents historical inspection from claiming facts under a
// lock or Node pin different from the installed parser's working definitions.
// The lockfile is bound by the parser's declared dependency closure, so a
// revision that changes an unloaded SDK test dependency still names this parser.
// Callers retain the reads in their snapshot and perform their final sweep.
func (e *Engine) ValidateRuntime(read func(string) ([]byte, error)) error {
	for _, name := range RuntimeInputs() {
		captured, err := read(name)
		if err != nil {
			return fmt.Errorf("document parser input %s: %w", name, err)
		}
		installed, err := os.ReadFile(filepath.Join(e.root, name))
		if err != nil {
			return fmt.Errorf("document parser input %s: %w", name, err)
		}
		if !bytes.Equal(captured, installed) && !(name == lockPath && sameParserLock(captured, installed)) {
			return fmt.Errorf("document parser inputs differ from selected source: %s; prepare dependencies for the selected revision", name)
		}
	}
	return nil
}
