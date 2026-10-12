package policy

import (
	"sort"

	"concave/internal/repository"
)

// Requirements is a declaration of checks and cases, never execution evidence.
type Requirements struct {
	Checks   []RequiredCheck `json:"checks"`
	Unmapped []string        `json:"unmapped_witnesses"`
}
type RequiredCheck struct {
	Name  string   `json:"name"`
	Cases []string `json:"cases"`
}

// RequiredChecks is shared by inspection and contribution selection. Source paths
// select an owner directly; witness references select their exact declared mapping.
func (config *Config) RequiredChecks(paths, witnesses []string) Requirements {
	names, references := map[string]bool{}, map[string]bool{}
	for _, witness := range witnesses {
		references[witness] = true
	}
	for name, check := range config.Checks {
		for _, path := range paths {
			if repository.Matches(path, check.Paths) {
				names[name] = true
			}
		}
		// Editing the test source itself also requires its mapped check.
		for reference := range check.Witnesses {
			path, _, err := WitnessTarget("", reference)
			if err == nil && repository.Matches(path, paths) {
				references[reference] = true
			}
		}
	}
	// A generator output or any of its declared sources requires the derived
	// entry's freshness check, so changed inputs cannot leave a stale output.
	for _, d := range config.Derived {
		for _, path := range paths {
			if repository.Matches(path, d.Paths) || repository.Matches(path, d.Sources) {
				names[d.Check] = true
			}
		}
	}
	result := Requirements{Checks: []RequiredCheck{}, Unmapped: []string{}}
	for witness := range references {
		mapped := false
		for name, check := range config.Checks {
			if _, ok := check.Witnesses[witness]; ok {
				names[name], mapped = true, true
			}
		}
		if !mapped {
			result.Unmapped = append(result.Unmapped, witness)
		}
	}
	for name := range names {
		cases := map[string]bool{}
		for _, ids := range config.Checks[name].Witnesses {
			for _, id := range ids {
				cases[id] = true
			}
		}
		required := RequiredCheck{Name: name, Cases: []string{}}
		for id := range cases {
			required.Cases = append(required.Cases, id)
		}
		sort.Strings(required.Cases)
		result.Checks = append(result.Checks, required)
	}
	sort.Slice(result.Checks, func(i, j int) bool { return result.Checks[i].Name < result.Checks[j].Name })
	sort.Strings(result.Unmapped)
	return result
}
