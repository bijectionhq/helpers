package analysis

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"concave/internal/repository"
)

type CallerOmissionReason string

const (
	callerSourceUnavailable CallerOmissionReason = "source_unavailable"
	callerFileBound         CallerOmissionReason = "file_bound"
	callerByteBound         CallerOmissionReason = "byte_bound"
	callerLocationBound     CallerOmissionReason = "location_bound"
	callerInvalidContract   CallerOmissionReason = "invalid_contract"
)

type CallerActionKind string

const (
	NarrowCallerScope CallerActionKind = "narrow_scope"
	InspectCallerGaps CallerActionKind = "inspect_gaps"
)

type CallerAction struct {
	Kind CallerActionKind `json:"kind"`
	Path string           `json:"path,omitempty"`
}

type CallerOmission struct {
	Path      string               `json:"path"`
	Reason    CallerOmissionReason `json:"reason"`
	Locations int                  `json:"locations"`
	Detail    string               `json:"detail,omitempty"`
}

type CallerCoverage struct {
	NextAction        *CallerAction    `json:"next_action,omitempty"`
	Scope             []string         `json:"scope"`
	TotalFiles        int              `json:"total_files"`
	TotalLocations    int              `json:"total_locations"`
	ExcludedFiles     int              `json:"excluded_files"`
	ExcludedLocations int              `json:"excluded_locations"`
	ResolvedFiles     int              `json:"resolved_files"`
	ResolvedBytes     int              `json:"resolved_bytes"`
	ResolvedLocations int              `json:"resolved_locations"`
	UnresolvedFiles   int              `json:"unresolved_files"`
	OmittedLocations  int              `json:"omitted_locations"`
	Omissions         []CallerOmission `json:"omissions"`
	Limits            []string         `json:"limits"`
}

type callerBudget struct{ files, sourceBytes, locations int }

type callerCandidate struct {
	path           string
	sourceBytes    int
	changed        bool
	locations      []Location
	totalLocations int
	excluded       bool
	reason         CallerOmissionReason
	detail         string
}

func normalizeCallerScope(scope []string) ([]string, error) {
	scope = slices.Clone(scope)
	slices.Sort(scope)
	scope = slices.Compact(scope)
	for _, path := range scope {
		if !repository.ValidPath(path) {
			return nil, fmt.Errorf("unsafe caller path %q", path)
		}
	}
	return scope, nil
}

// Selection owns ordering and budgets, never resolution counts. Candidates keep
// their source metadata and selected locations together until resolution finishes.
func selectCallers(snapshot *repository.Snapshot, baseline map[string]repository.Entry, byPath map[string][]Location, scope []string, budget callerBudget) ([]callerCandidate, error) {
	candidates := make([]callerCandidate, 0, len(byPath))
	matched := map[string]bool{}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		candidate := callerCandidate{path: path, totalLocations: len(byPath[path])}
		for _, selector := range scope {
			if repository.Matches(path, []string{selector}) {
				matched[selector] = true
			}
		}
		candidate.excluded = len(scope) > 0 && !repository.Matches(path, scope)
		if !candidate.excluded {
			data, err := snapshot.Read(path)
			if err != nil {
				var unavailable *repository.Unavailable
				if !errors.As(err, &unavailable) {
					return nil, err
				}
				candidate.reason, candidate.detail = callerSourceUnavailable, err.Error()
			} else {
				candidate.locations = slices.Clone(byPath[path])
				sort.Slice(candidate.locations, func(i, j int) bool {
					a, b := candidate.locations[i], candidate.locations[j]
					if a.Line != b.Line {
						return a.Line < b.Line
					}
					return a.Column < b.Column
				})
			}
			state := snapshot.Inputs[path]
			candidate.changed = state == nil || state.OID != baseline[path].OID || state.Mode != baseline[path].Mode
			candidate.sourceBytes = len(data)
		}
		candidates = append(candidates, candidate)
	}
	for _, selector := range scope {
		if !matched[selector] {
			return nil, fmt.Errorf("caller selector %q matches no captured caller; inspect the artifact's consumers", selector)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.changed != b.changed {
			return a.changed
		}
		return a.path < b.path
	})
	selectedFiles, selectedBytes, selectedLocations := 0, 0, 0
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.excluded || candidate.reason != "" {
			continue
		}
		switch {
		case selectedFiles >= budget.files:
			candidate.reason = callerFileBound
		case candidate.sourceBytes > budget.sourceBytes-selectedBytes:
			candidate.reason = callerByteBound
		case selectedLocations >= budget.locations:
			candidate.reason = callerLocationBound
		}
		if candidate.reason != "" {
			candidate.locations = nil
			continue
		}
		capacity := budget.locations - selectedLocations
		if len(candidate.locations) > capacity {
			candidate.locations = candidate.locations[:capacity]
			candidate.reason = callerLocationBound
		}
		selectedFiles++
		selectedBytes += candidate.sourceBytes
		selectedLocations += len(candidate.locations)
	}
	return candidates, nil
}

// Coverage is derived once from selected candidates and completed resolution.
// This also owns the continuation decision shared by JSON and terminal output.
func callerCoverage(candidates []callerCandidate, resolved map[string]FileContracts, scope []string, budget callerBudget) *CallerCoverage {
	coverage := &CallerCoverage{Scope: scope, Omissions: []CallerOmission{}, Limits: []string{
		fmt.Sprintf("Caller context is bounded to %d files, %d source bytes and %d unique locations. Import edges are source dependencies, not executed calls.", budget.files, budget.sourceBytes, budget.locations),
	}}
	for _, candidate := range candidates {
		coverage.TotalFiles++
		coverage.TotalLocations += candidate.totalLocations
		if candidate.excluded {
			coverage.ExcludedFiles++
			coverage.ExcludedLocations += candidate.totalLocations
			continue
		}
		count := candidate.totalLocations
		reason := candidate.reason
		_, valid := resolved[candidate.path]
		if len(candidate.locations) > 0 && valid {
			coverage.ResolvedFiles++
			coverage.ResolvedBytes += candidate.sourceBytes
			coverage.ResolvedLocations += len(candidate.locations)
			count -= len(candidate.locations)
		} else {
			coverage.UnresolvedFiles++
			if len(candidate.locations) > 0 {
				reason = callerInvalidContract
			}
		}
		if count == 0 {
			continue
		}
		coverage.OmittedLocations += count
		coverage.Omissions = append(coverage.Omissions, CallerOmission{Path: candidate.path, Reason: reason, Locations: count, Detail: candidate.detail})
		narrower := len(scope) != 1 || scope[0] != candidate.path
		fits := candidate.sourceBytes <= budget.sourceBytes && candidate.totalLocations <= budget.locations
		bounded := reason == callerFileBound || reason == callerByteBound || reason == callerLocationBound
		if coverage.NextAction == nil && narrower && fits && bounded {
			coverage.NextAction = &CallerAction{Kind: NarrowCallerScope, Path: candidate.path}
		}
	}
	if len(coverage.Omissions) > 0 && coverage.NextAction == nil {
		coverage.NextAction = &CallerAction{Kind: InspectCallerGaps}
	}
	return coverage
}
