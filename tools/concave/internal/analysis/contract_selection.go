package analysis

import (
	"fmt"
	"sort"

	"concave/internal/policy"
	"concave/internal/repository"
)

// Contract review compares the union selected by both policies. Narrowing a case
// or deleting its last native file must not remove the old obligations from review.
func contractSelection(repo *repository.Repo, before, after *policy.Config, baseline string, options Options) ([]string, error) {
	selected := map[string]bool{}
	known := false
	for _, side := range []struct {
		config   *policy.Config
		revision string
	}{{before, baseline}, {after, options.Rev}} {
		if options.Case != "" {
			if _, ok := side.config.Cases[options.Case]; !ok {
				continue
			}
		}
		if options.Family != "" {
			found := false
			for _, family := range side.config.Families {
				if family.ID == options.Family {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		selectors, err := scopes(side.config, options)
		if err != nil {
			return nil, err
		}
		known = true
		revision := side.revision
		if revision == "" {
			revision = "HEAD"
		}
		tree, err := repo.Tree(revision)
		if err != nil {
			return nil, err
		}
		upstream, err := side.config.UpstreamTree(repo)
		if err != nil {
			return nil, err
		}
		paths := union(tree, upstream)
		if side.revision == "" {
			newPaths, err := repo.Untracked()
			if err != nil {
				return nil, err
			}
			paths = append(paths, newPaths...)
		}
		full := len(options.Paths) == 0 && options.Case == "" && options.Family == ""
		for _, path := range paths {
			_, inherited := upstream[path]
			if !repository.Matches(path, selectors) && !(full && inherited) {
				continue
			}
			if options.Family != "" {
				family, err := side.config.Family(path)
				if err != nil || family != options.Family {
					continue
				}
			}
			selected[path] = true
		}
	}
	if !known {
		return nil, fmt.Errorf("unknown review case or family in both snapshots")
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("empty review scope in both snapshots")
	}
	paths := make([]string, 0, len(selected))
	for path := range selected {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
