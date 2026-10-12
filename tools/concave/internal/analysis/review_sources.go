package analysis

import (
	"errors"
	"fmt"
	"sort"

	"concave/internal/policy"
	"concave/internal/repository"
)

type ReviewSource struct {
	Version     string `json:"version"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	OID         string `json:"oid"`
	Unavailable string `json:"unavailable,omitempty"`
}

// reviewSources owns source identity checks, deduplication and the source byte budget.
// A source version is distinct from its content: equal bytes share one blob.
type sourceVersion struct{ version, path string }
type reviewSources struct {
	sources []ReviewSource
	blobs   map[string]string
	seen    map[sourceVersion]bool
	bytes   int
}

func newReviewSources() *reviewSources {
	return &reviewSources{sources: []ReviewSource{}, blobs: map[string]string{}, seen: map[sourceVersion]bool{}}
}
func (collector *reviewSources) add(version, path string, snapshot *repository.Snapshot, expected *repository.State, requireMatch bool) error {
	data, err := snapshot.Read(path)
	state := snapshot.Inputs[path]
	matches := repository.Same(expected, state)
	// Inspection's exact-file fast path stores the Git identity without bytes.
	if expected != nil && state != nil && expected.OID != "" {
		matches = expected.OID == state.OID && expected.Mode == state.Mode
	}
	if requireMatch && !matches {
		return &repository.Stale{Message: "input changed preparing review: " + path}
	}
	if err != nil {
		var unavailable *repository.Unavailable
		if errors.As(err, &unavailable) {
			key := sourceVersion{version, path}
			if collector.seen[key] {
				return nil
			}
			collector.seen[key] = true
			source := ReviewSource{Version: version, Path: path, Unavailable: err.Error()}
			if state != nil {
				source.OID, source.SHA256 = state.OID, state.SHA256
			}
			collector.sources = append(collector.sources, source)
			return nil
		}
		return err
	}
	key := sourceVersion{version, path}
	if collector.seen[key] {
		return nil
	}
	collector.seen[key] = true
	digest := repository.Digest(data)
	if _, ok := collector.blobs[digest]; !ok {
		collector.bytes += len(data)
		if collector.bytes > repository.MaxOutput/2 {
			return fmt.Errorf("review source exceeds 64 MiB; narrow scope")
		}
		collector.blobs[digest] = string(data)
	}
	collector.sources = append(collector.sources, ReviewSource{Version: version, Path: path, SHA256: digest, OID: state.OID})
	return nil
}
func (collector *reviewSources) collect(version string, snapshot *repository.Snapshot, report *Report, config *policy.Config, comparisonPaths []string) error {
	paths := map[string]*repository.State{}
	for _, path := range comparisonPaths {
		paths[path] = nil
	}
	for _, row := range report.Files {
		paths[row.Path] = row.Selected
	}
	if report.Contracts != nil {
		for path, state := range report.Contracts.Inputs {
			paths[path] = state
		}
	}
	for path, state := range report.DocumentInputs {
		paths[path] = state
	}
	// Policy, manifest and the scope's bindings are explicit inputs, even
	// outside source scopes.
	rows := make([]string, 0, len(report.Files))
	for _, row := range report.Files {
		rows = append(rows, row.Path)
	}
	inputs := map[string]bool{}
	for _, path := range config.Inputs(rows) {
		paths[path], inputs[path] = nil, true
	}
	names := make([]string, 0, len(paths))
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	for _, path := range names {
		requireMatch := !inputs[path]
		if err := collector.add(version, path, snapshot, paths[path], requireMatch); err != nil {
			return err
		}
	}
	return nil
}
