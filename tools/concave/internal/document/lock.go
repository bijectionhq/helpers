package document

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"concave/internal/repository"
)

// Bind the installed parser's declared dependency path: the docs importer
// selects @docusaurus/core, and the adapter resolves every parser package
// through that package's dependency closure. Other docs dependencies, including
// workspace SDK development tools, are never loaded by the parser.
const (
	lockPath         = "npm-packages/pnpm-lock.yaml"
	lockImporter     = "docs"
	parserDependency = "@docusaurus/core"
)

type lockEntries map[string][]string

// pnpmLock holds the sections of a version 9 lockfile that describe an
// installation. Resolution inputs (overrides, patches, extension checksums)
// are left out: their effect on a project is the entries it resolves to.
type pnpmLock struct {
	preamble                       []string
	importers, packages, snapshots lockEntries
}

// parseLock reads the line-oriented subset of YAML pnpm writes. Any other
// syntax is an error, so the caller compares whole files instead.
func parseLock(data []byte) (*pnpmLock, error) {
	lock := &pnpmLock{importers: lockEntries{}, packages: lockEntries{}, snapshots: lockEntries{}}
	sections := map[string]lockEntries{"importers": lock.importers, "packages": lock.packages, "snapshots": lock.snapshots}
	var section, key string
	var entries lockEntries
	for number, line := range strings.Split(string(data), "\n") {
		fail := func(why string) (*pnpmLock, error) {
			return nil, fmt.Errorf("unsupported lockfile line %d: %s", number+1, why)
		}
		switch {
		case strings.TrimSpace(line) == "":
		case strings.ContainsAny(line, "\t\r"):
			return fail("tab or carriage return")
		case line[0] == '#':
		case line[0] != ' ':
			name, rest, err := splitKey(line)
			if err != nil {
				return fail(err.Error())
			}
			section, key, entries = name, "", sections[name]
			if entries != nil && rest != "" {
				return fail("section is not a mapping")
			}
			if name == "lockfileVersion" || name == "settings" {
				lock.preamble = append(lock.preamble, line)
			}
		case entries == nil:
			if section == "" {
				return fail("indented line before any section")
			}
			if section == "settings" {
				lock.preamble = append(lock.preamble, line)
			}
		case strings.HasPrefix(line, "    "):
			if key == "" {
				return fail("entry body without an entry")
			}
			entries[key] = append(entries[key], line)
		case strings.HasPrefix(line, "  ") && line[2] != ' ':
			name, rest, err := splitKey(line[2:])
			if err != nil {
				return fail(err.Error())
			}
			if rest != "" && rest != "{}" {
				return fail("entry is not a mapping")
			}
			if _, ok := entries[name]; ok {
				return fail("duplicate entry " + name)
			}
			key, entries[name] = name, []string{}
		default:
			return fail("unexpected indentation")
		}
	}
	if len(lock.preamble) == 0 || !strings.HasPrefix(lock.preamble[0], "lockfileVersion:") {
		return nil, fmt.Errorf("unsupported lockfile: it does not start with lockfileVersion")
	}
	return lock, nil
}

// splitKey splits `key: rest`, where pnpm writes the key plain or in single
// quotes.
func splitKey(text string) (key, rest string, err error) {
	switch {
	case strings.HasPrefix(text, "'"):
		var b strings.Builder
		i := 1
		for ; ; i++ {
			if i >= len(text) {
				return "", "", fmt.Errorf("unterminated key")
			}
			if text[i] != '\'' {
				b.WriteByte(text[i])
			} else if i+1 < len(text) && text[i+1] == '\'' {
				b.WriteByte('\'')
				i++
			} else {
				break
			}
		}
		key, rest = b.String(), text[i+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("key without a value")
		}
		rest = rest[1:]
		if rest != "" && !strings.HasPrefix(rest, " ") {
			return "", "", fmt.Errorf("key without a value")
		}
		rest = strings.TrimPrefix(rest, " ")
	case strings.HasPrefix(text, `"`):
		return "", "", fmt.Errorf("double-quoted key")
	default:
		if i := strings.Index(text, ": "); i >= 0 {
			key, rest = text[:i], text[i+2:]
		} else if strings.HasSuffix(text, ":") {
			key = text[:len(text)-1]
		} else {
			return "", "", fmt.Errorf("key without a value")
		}
	}
	if key == "" {
		return "", "", fmt.Errorf("empty key")
	}
	return key, rest, nil
}

func unquote(value string) (string, error) {
	switch {
	case strings.HasPrefix(value, `"`):
		return "", fmt.Errorf("double-quoted value")
	case strings.HasPrefix(value, "'"):
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", fmt.Errorf("unterminated value")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	case value == "":
		return "", fmt.Errorf("empty value")
	}
	return value, nil
}

type lockDependency struct{ name, reference string }

// dependencies lists what an entry installs. An importer nests each
// dependency's resolved version under its name; a snapshot maps the name to it.
func dependencies(body []string, importer bool) ([]lockDependency, error) {
	var found []lockDependency
	var group, name string
	installed := func() bool {
		return group == "dependencies" || group == "optionalDependencies" || importer && group == "devDependencies"
	}
	pending := func() error {
		if name != "" {
			return fmt.Errorf("dependency %s has no version", name)
		}
		return nil
	}
	for _, line := range body {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 4 || indent == 6 && installed() {
			if err := pending(); err != nil {
				return nil, err
			}
		}
		switch {
		case indent == 4:
			key, _, err := splitKey(line[4:])
			if err != nil {
				return nil, err
			}
			group = key
		case !installed():
		case indent == 6:
			key, rest, err := splitKey(line[6:])
			if err != nil {
				return nil, err
			}
			if importer {
				if rest != "" {
					return nil, fmt.Errorf("dependency %s is not a mapping", key)
				}
				name = key
				continue
			}
			reference, err := unquote(rest)
			if err != nil {
				return nil, err
			}
			found = append(found, lockDependency{key, reference})
		case indent == 8 && importer && name != "":
			key, rest, err := splitKey(line[8:])
			if err != nil {
				return nil, err
			}
			if key != "version" {
				continue
			}
			reference, err := unquote(rest)
			if err != nil {
				return nil, err
			}
			found, name = append(found, lockDependency{name, reference}), ""
		}
	}
	return found, pending()
}

// snapshotKey is pnpm's rule for the entry a dependency reference names: a
// reference that is itself `name@version` is an alias, any other is a version
// of the named package.
func snapshotKey(name, reference string) string {
	at := strings.Index(reference, "@")
	if at == 0 {
		return reference
	}
	colon, bracket := strings.Index(reference, ":"), strings.Index(reference, "(")
	if at > 0 && (colon < 0 || at < colon) && (bracket < 0 || at < bracket) {
		return reference
	}
	return name + "@" + reference
}

func parserLock(data []byte) (string, error) {
	lock, err := parseLock(data)
	if err != nil {
		return "", err
	}
	importers, snapshots, packages := map[string]bool{}, map[string]bool{}, map[string]bool{}
	type visit struct {
		importer bool
		key      string
	}
	pending := []visit{{true, lockImporter}}
	for len(pending) > 0 {
		next := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		seen, entries := snapshots, lock.snapshots
		if next.importer {
			seen, entries = importers, lock.importers
		}
		if seen[next.key] {
			continue
		}
		body, ok := entries[next.key]
		if !ok {
			return "", fmt.Errorf("lockfile has no entry for %s", next.key)
		}
		seen[next.key] = true
		if !next.importer {
			// The package entry holds the content identity; its key drops the
			// peer and patch suffix.
			name, _, _ := strings.Cut(next.key, "(")
			if _, ok := lock.packages[name]; !ok {
				return "", fmt.Errorf("lockfile has no package %s", name)
			}
			packages[name] = true
		}
		found, err := dependencies(body, next.importer)
		if err != nil {
			return "", fmt.Errorf("lockfile entry %s: %w", next.key, err)
		}
		if next.importer && next.key == lockImporter {
			selected := []lockDependency{}
			for _, dependency := range found {
				if dependency.name == parserDependency {
					selected = append(selected, dependency)
				}
			}
			if len(selected) != 1 {
				return "", fmt.Errorf("docs must declare exactly one %s parser dependency", parserDependency)
			}
			found = selected
		}
		for _, dependency := range found {
			target, linked := strings.CutPrefix(dependency.reference, "link:")
			switch {
			case !linked:
				pending = append(pending, visit{false, snapshotKey(dependency.name, dependency.reference)})
			case next.importer:
				// An importer's link is relative to it; a snapshot's to the lockfile.
				pending = append(pending, visit{true, path.Join(next.key, target)})
			default:
				pending = append(pending, visit{true, path.Clean(target)})
			}
		}
	}
	var b strings.Builder
	for _, line := range lock.preamble {
		b.WriteString(line + "\n")
	}
	for _, part := range []struct {
		name     string
		selected map[string]bool
		entries  lockEntries
	}{{"importers", importers, lock.importers}, {"packages", packages, lock.packages}, {"snapshots", snapshots, lock.snapshots}} {
		keys := make([]string, 0, len(part.selected))
		for key := range part.selected {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "%s\x00%s\n%s\n", part.name, key, strings.Join(part.entries[key], "\n"))
		}
	}
	return repository.Digest([]byte(b.String())), nil
}

// sameParserLock reports whether two lockfiles install the same parser. A
// lockfile this cannot read never matches.
func sameParserLock(a, b []byte) bool {
	first, err := parserLock(a)
	if err != nil {
		return false
	}
	second, err := parserLock(b)
	return err == nil && first == second
}
