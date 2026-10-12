package repository

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourcePair holds an already captured, bounded source comparison. DiffSources
// uses the same Git hunk algorithm as revision inspection without touching the
// checkout, index or object database. All pairs share one Git process.
type SourcePair struct{ Before, After []byte }

func (r *Repo) DiffSources(pairs map[string]SourcePair) (map[string][]Hunk, error) {
	paths := make([]string, 0, len(pairs))
	total := 0
	for path, pair := range pairs {
		if !ValidPath(path) || strings.HasSuffix(path, "/") || !Text(pair.Before) || !Text(pair.After) || len(pair.Before) > MaxBytes || len(pair.After) > MaxBytes {
			return nil, fmt.Errorf("invalid bounded source pair %q", path)
		}
		total += len(pair.Before) + len(pair.After)
		if total > MaxOutput {
			return nil, fmt.Errorf("source comparisons exceed memory bound")
		}
		if !bytes.Equal(pair.Before, pair.After) {
			paths = append(paths, path)
		}
	}
	out := map[string][]Hunk{}
	if len(paths) == 0 {
		return out, nil
	}
	sort.Strings(paths)
	dir, err := os.MkdirTemp("", "concave-source-diff-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, side := range []string{"before", "after"} {
		if err = os.Mkdir(filepath.Join(dir, side), 0700); err != nil {
			return nil, err
		}
	}
	// Opaque names avoid case-folding collisions and special path syntax. Source
	// extensions and repository attributes cannot select an external diff driver.
	owners := map[string]string{}
	for i, path := range paths {
		name := fmt.Sprintf("%08d", i)
		owners["after/"+name] = path
		pair := pairs[path]
		if err = os.WriteFile(filepath.Join(dir, "before", name), pair.Before, 0600); err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(dir, "after", name), pair.After, 0600); err != nil {
			return nil, err
		}
	}
	args := append(sourceDiffArgs(), "--no-index", "--", "before", "after")
	data, err := r.gitAt(dir, nil, true, args...)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseDiff(data)
	if err != nil {
		return nil, err
	}
	for name, hs := range parsed {
		path, ok := owners[name]
		if !ok {
			return nil, fmt.Errorf("unexpected source comparison path %q", name)
		}
		pair := pairs[path]
		// Refuse incomplete/truncated output: its hunks must reconstruct every byte.
		lines := Lines(string(pair.Before))
		var reconstructed strings.Builder
		pos := 0
		for i := range hs {
			h := &hs[i]
			if h.Start < pos || h.End < h.Start || h.End > len(lines) || strings.Join(lines[h.Start:h.End], "") != h.Before {
				return nil, fmt.Errorf("invalid source comparison span for %q", path)
			}
			reconstructed.WriteString(strings.Join(lines[pos:h.Start], ""))
			reconstructed.WriteString(h.After)
			pos = h.End
			h.Path = path
		}
		reconstructed.WriteString(strings.Join(lines[pos:], ""))
		if reconstructed.String() != string(pair.After) {
			return nil, fmt.Errorf("incomplete source comparison for %q", path)
		}
		out[path] = hs
	}
	if len(out) != len(paths) {
		return nil, fmt.Errorf("source comparison omitted changed paths")
	}
	return out, nil
}
