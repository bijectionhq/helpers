package analysis

import (
	"bytes"
	"reflect"
	"sort"

	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
)

// ChangedPaths compares raw working bytes, never Git clean filters. A policy
// change conservatively expands to the whole audit, including the old scope.
func ChangedPaths(repo *repository.Repo, config *policy.Config, against, revision string) ([]string, bool, error) {
	paths, full, _, _, err := changedScope(repo, config, against, revision)
	return paths, full, err
}

// changedScope is ChangedPaths with the resolved base and the policy loaded
// from it, which a check reuses for its reviewed-reference comparison.
func changedScope(repo *repository.Repo, config *policy.Config, against, revision string) ([]string, bool, string, *policy.Config, error) {
	defer process.Phase(repo.Context, "changes", "")()
	if against == "" {
		against = "HEAD"
	}
	base, err := repo.Resolve(against)
	if err != nil {
		return nil, false, "", nil, err
	}
	target := revision
	if target == "" {
		target = "HEAD"
	}
	current, err := repo.Resolve(target)
	if err != nil {
		return nil, false, "", nil, err
	}
	if err := repo.Ancestor(base, current); err != nil {
		return nil, false, "", nil, err
	}
	previous, err := policy.Load(repo, base)
	if err != nil {
		return nil, false, "", nil, err
	}
	paths, full, err := changedPaths(repo, previous, config, base, current, revision == "")
	return paths, full, base, previous, err
}

// changedPaths compares two loaded policies and their revisions; working also
// compares the working files of the current revision.
func changedPaths(repo *repository.Repo, previous, config *policy.Config, base, current string, working bool) ([]string, bool, error) {
	bt, err := repo.Tree(base)
	if err != nil {
		return nil, false, err
	}
	ct, err := repo.Tree(current)
	if err != nil {
		return nil, false, err
	}
	all := map[string]bool{}
	for p := range bt {
		all[p] = true
	}
	for p := range ct {
		all[p] = true
	}
	if working {
		paths, err := repo.Inventory()
		if err != nil {
			return nil, false, err
		}
		for _, p := range paths {
			all[p] = true
		}
	}
	full := !bytes.Equal(previous.Raw, config.Raw) || previous.ManifestSHA256 != config.ManifestSHA256
	approvalPaths := map[string]bool{}
	oldBindings := map[string]policy.Exception{}
	for _, e := range previous.Exceptions {
		oldBindings[e.ID] = e
	}
	for _, e := range config.Exceptions {
		old, ok := oldBindings[e.ID]
		if !ok || !reflect.DeepEqual(old, e) {
			approvalPaths[e.Path] = true
			if ok {
				approvalPaths[old.Path] = true
			}
		}
		delete(oldBindings, e.ID)
	}
	for _, e := range oldBindings {
		approvalPaths[e.Path] = true
	}
	for path := range approvalPaths {
		all[path] = true
	}
	if full {
		for _, upstream := range []string{previous.Upstream, config.Upstream} {
			tree, err := repo.Tree(upstream)
			if err != nil {
				return nil, false, err
			}
			for p := range tree {
				all[p] = true
			}
		}
	}
	paths := []string{}
	compare := []string{}
	for p := range all {
		switch {
		case approvalPaths[p] || full:
			paths = append(paths, p)
		case !working:
			if bt[p] != ct[p] {
				paths = append(paths, p)
			}
		default:
			compare = append(compare, p)
		}
	}
	sort.Strings(compare)
	// Git vouches for the bytes of files its stat check finds clean when it
	// converts nothing; only the rest are read.
	unchanged := map[string]repository.Entry{}
	if len(compare) > 0 {
		if unchanged, err = repo.UnchangedIndex(); err != nil {
			return nil, false, err
		}
	}
	reader := repo.Reader()
	defer reader.Close()
	for _, p := range compare {
		var state *repository.State
		if entry, ok := unchanged[p]; ok {
			state = &repository.State{Mode: entry.Mode, OID: entry.OID}
		} else if state, err = reader.Identity(p); err != nil {
			return nil, false, err
		}
		before := bt[p]
		if state == nil && before.OID != "" || state != nil && (state.OID != before.OID || state.Mode != before.Mode) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, full, nil
}
