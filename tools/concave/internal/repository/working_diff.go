package repository

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// workingPatch renders diagnostic patches from raw snapshots. Running ordinary
// git diff on working files would execute clean filters and normalize line ends,
// so those patches could disagree with the bytes inspected and approved.
func (r *Repo) workingPatch(base string, paths []string) ([]byte, error) {
	tree, err := r.Tree(base)
	if err != nil {
		return nil, err
	}
	type input struct {
		before, after *State
	}
	inputs := map[string]input{}
	pairs := map[string]SourcePair{}
	total := 0
	for _, path := range paths {
		if !ValidPath(path) || strings.HasSuffix(path, "/") {
			return nil, fmt.Errorf("patch needs explicit file paths: %q", path)
		}
		if _, seen := inputs[path]; seen {
			continue
		}
		var before *State
		a := []byte{}
		if entry, ok := tree[path]; ok {
			if entry.Kind == "blob" {
				a, err = r.Blob(entry.OID)
				if err != nil {
					return nil, err
				}
			}
			if before, err = r.BlobState(entry, a); err != nil {
				return nil, err
			}
		}
		after, b, err := r.Snapshot(path)
		if err != nil {
			return nil, err
		}
		if before == nil && after == nil {
			continue
		}
		if after == nil {
			b = []byte{}
		}
		if before != nil && after != nil && before.OID == after.OID && before.Mode == after.Mode {
			continue
		}
		total += len(a) + len(b)
		if total > MaxOutput {
			return nil, fmt.Errorf("working patch exceeds source memory bound")
		}
		inputs[path] = input{before, after}
		regular := func(s *State) bool { return s == nil || s.Kind == "file" && (s.Mode == "100644" || s.Mode == "100755") }
		if regular(before) && regular(after) && Text(a) && Text(b) {
			pairs[path] = SourcePair{Before: a, After: b}
		}
	}
	hunks, err := r.DiffSources(pairs)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(inputs))
	for path := range inputs {
		names = append(names, path)
	}
	sort.Strings(names)
	var patch bytes.Buffer
	for _, path := range names {
		in := inputs[path]
		old, next := strconv.Quote("a/"+path), strconv.Quote("b/"+path)
		fmt.Fprintf(&patch, "diff --git %s %s\n", old, next)
		switch {
		case in.before == nil:
			fmt.Fprintf(&patch, "new file mode %s\n", in.after.Mode)
			old = "/dev/null"
		case in.after == nil:
			fmt.Fprintf(&patch, "deleted file mode %s\n", in.before.Mode)
			next = "/dev/null"
		case in.before.Mode != in.after.Mode:
			fmt.Fprintf(&patch, "old mode %s\nnew mode %s\n", in.before.Mode, in.after.Mode)
		}
		if _, text := pairs[path]; !text {
			// Source reports carry the exact identities and unsupported-input findings.
			// Diagnostic patches do not print binary or unbounded source bytes.
			fmt.Fprintf(&patch, "Binary files %s and %s differ\n", old, next)
		} else if hs := hunks[path]; len(hs) > 0 {
			fmt.Fprintf(&patch, "--- %s\n+++ %s\n", old, next)
			encoded := Patch(path, hs)
			// Patch owns hunk serialization, including CRLF and missing final newlines.
			body := bytes.SplitN(encoded, []byte("\n"), 4)
			patch.Write(body[3])
		}
		if patch.Len() > MaxOutput {
			return nil, fmt.Errorf("working patch exceeds output bound")
		}
	}
	return patch.Bytes(), nil
}
