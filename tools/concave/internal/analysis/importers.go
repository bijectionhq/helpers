package analysis

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"concave/internal/document"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
)

// maxImporterDocuments bounds one importer search, like a document traversal.
const maxImporterDocuments = 4096

// DocumentImporters names the documents outside a scoped selection whose own
// dependency identity includes a selected path, so a scoped check of a partial
// or snippet re-measures every page that imports it. Each round reads the
// policy's documents and parses, with the document adapter's own resolution,
// only those its Reach admits for a file selected or already found to import
// one; the adapter's transitive inputs decide. Rounds repeat until no document is
// added, so the result is deterministic and bounded by the document count.
func DocumentImporters(repo *repository.Repo, config *policy.Config, revision string, selectors []string) ([]string, error) {
	return documentImporters(repo, config, revision, selectors, policy.NewSession(repo, config))
}

func documentImporters(repo *repository.Repo, config *policy.Config, revision string, selectors []string, session *policy.Session) ([]string, error) {
	defer process.Phase(repo.Context, "importers", "")()
	if config.Documents == nil || len(selectors) == 0 {
		return []string{}, nil
	}
	var inventory []string
	var tree map[string]repository.Entry
	if revision != "" {
		var err error
		if tree, err = repo.Tree(revision); err != nil {
			return nil, err
		}
		for p := range tree {
			inventory = append(inventory, p)
		}
		slices.Sort(inventory)
	} else {
		var err error
		if inventory, err = repo.Inventory(); err != nil {
			return nil, err
		}
	}
	reader := repo.Reader()
	defer reader.Close()
	read := func(p string) ([]byte, error) {
		if revision != "" {
			entry, ok := tree[p]
			if !ok || entry.Mode != "100644" && entry.Mode != "100755" {
				return nil, fmt.Errorf("missing regular text file")
			}
			return repo.Blob(entry.OID)
		}
		state, data, err := reader.Snapshot(p)
		if err != nil {
			return nil, err
		}
		if state == nil || state.Kind != "file" {
			return nil, fmt.Errorf("missing regular text file")
		}
		return data, nil
	}
	targets := map[string]bool{}
	// A literal selected file is a target even when absent: a probe of a
	// deleted partial is part of its importer's identity.
	for _, selector := range selectors {
		if !strings.HasSuffix(selector, "/") {
			targets[selector] = true
		}
	}
	documents := []string{}
	for _, p := range inventory {
		if repository.Matches(p, selectors) {
			targets[p] = true
		}
		if !targets[p] && document.Supports(p) && repository.Matches(p, config.Documents.Paths) {
			documents = append(documents, p)
		}
	}
	if len(targets) == 0 || len(documents) == 0 {
		return []string{}, nil
	}
	if len(documents) > maxImporterDocuments {
		return nil, fmt.Errorf("importer search exceeds %d documents; check the documents in full", maxImporterDocuments)
	}
	sources := map[string][]byte{}
	for _, p := range documents {
		data, err := read(p)
		if repository.IsStale(err) {
			return nil, err
		}
		if err == nil {
			sources[p] = data
		}
	}
	engine := session.Documents
	frontier := slices.Sorted(maps.Keys(targets))
	parsed := map[string]bool{}
	importers := []string{}
	for len(frontier) > 0 {
		var reach document.Reach
		for _, p := range frontier {
			reach.Add(p)
		}
		roots := []document.Source{}
		for _, p := range documents {
			if !parsed[p] && sources[p] != nil && reach.May(p, sources[p]) {
				roots = append(roots, document.Source{Path: p, Source: string(sources[p])})
				parsed[p] = true
			}
		}
		frontier = nil
		if len(roots) == 0 {
			break
		}
		graph, err := engine.Dependencies(roots, read)
		if err != nil {
			return nil, err
		}
		for _, root := range roots {
			for _, input := range graph.Inputs[root.Path] {
				if input != root.Path && targets[input] {
					importers = append(importers, root.Path)
					frontier = append(frontier, root.Path)
					break
				}
			}
		}
		for _, p := range frontier {
			targets[p] = true
		}
	}
	slices.Sort(importers)
	return importers, nil
}
