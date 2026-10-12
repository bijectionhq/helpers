package workflow

import (
	"fmt"
	"sort"
	"strings"

	"concave/internal/environment"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/verification"
)

// CheckInputs shares the evidence owners' complete declared inventories.
func CheckInputs(repo *repository.Repo, config *policy.Config, checks []string) (repository.Inputs, error) {
	inputs := repository.Inputs{}
	for _, name := range checks {
		check, ok := config.Checks[name]
		if !ok {
			return nil, fmt.Errorf("unknown check %s", name)
		}
		captured, err := verification.Inputs(repo, check)
		if err != nil {
			return nil, err
		}
		for _, name := range config.EnvironmentClosure(check.Environment) {
			prepared, err := environment.Inputs(repo, config.Environments[name])
			if err != nil {
				return nil, err
			}
			for path, state := range prepared {
				captured[path] = state
			}
		}
		for path, state := range captured {
			inputs[path] = state
		}
	}
	return inputs, nil
}

// CheckPaths bounds physical source materialization. Source analysis reads the
// complete immutable Git tree; execution must prove its declared inputs exist.
// Selectors with patterns conservatively materialize their literal ancestor.
func CheckPaths(repo *repository.Repo, checks []string) ([]string, error) {
	config, err := policy.Load(repo, "")
	if err != nil {
		return nil, err
	}
	inputs, err := CheckInputs(repo, config, checks)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{policy.File: true, policy.BindingsDirectory + "/": true, ".gitignore": true}
	if config.TransformationManifest != "" {
		paths[config.TransformationManifest] = true
	}
	for path := range inputs {
		paths[path] = true
	}
	// Directories include future source additions on a merged candidate.
	for _, name := range checks {
		for _, environment := range config.EnvironmentClosure(config.Checks[name].Environment) {
			paths[config.Environments[environment].Directory+"/"] = true
		}
		for _, selector := range config.Checks[name].Inputs {
			if i := strings.IndexAny(selector, "*?["); i >= 0 {
				prefix := selector[:i]
				j := strings.LastIndex(prefix, "/")
				if j < 0 {
					return nil, fmt.Errorf("check %s has an unbounded materialization selector %q; declare literal input directories", name, selector)
				}
				selector = prefix[:j+1]
			}
			paths[selector] = true
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	selected := []string{}
	ancestor := ""
	for _, path := range ordered {
		if ancestor != "" && strings.HasPrefix(path, ancestor) {
			continue
		}
		selected = append(selected, path)
		ancestor = ""
		if strings.HasSuffix(path, "/") {
			ancestor = path
		}
	}
	args := []string{}
	for _, path := range selected {
		args = append(args, "--check-path", path)
	}
	return args, nil
}
