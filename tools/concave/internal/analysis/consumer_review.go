package analysis

import (
	"sort"
	"strings"

	"concave/internal/contracts"
	"concave/internal/repository"
	"concave/internal/syntax"
)

func (packet *ContractReview) findConsumers(resolver *contractResolver, addSource func(string) error) error {
	snapshot := resolver.snapshot
	packet.ConsumerDefinitions = map[string]Obligation{}
	targets := invariantMap(packet.Candidate)
	for ref, inv := range invariantMap(packet.Baseline) {
		if current, ok := targets[ref]; !ok {
			targets[ref] = inv
		} else if current.Declaration.Identity != inv.Declaration.Identity {
			targets[ref+"@baseline"] = inv
		}
	}
	packet.Search.Limits = []string{"Lexical reference candidates require reviewer confirmation; no symbol or complete call-graph claim.", "At most 256 files, 8 MiB of source and 64 candidate references per invariant are inspected."}
	if len(targets) == 0 {
		return nil
	}
	paths := []string{}
	available := map[string]bool{}
	selected := map[string]bool{}
	for _, row := range packet.Candidate.Files {
		selected[row.Path] = true
	}
	for _, path := range snapshot.Paths() {
		available[path] = true
	}
	if snapshot.Revision == "" {
		untracked, err := snapshot.Repo.Untracked()
		if err != nil {
			return err
		}
		for _, path := range untracked {
			available[path] = true
		}
	}
	for path := range available {
		if syntax.Language(path) != "" {
			paths = append(paths, path)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		if selected[paths[i]] != selected[paths[j]] {
			return selected[paths[i]]
		}
		return paths[i] < paths[j]
	})
	names := make([]string, 0, len(targets))
	for ref := range targets {
		names = append(names, ref)
	}
	sort.Strings(names)
	counts := map[string]int{}
	sources := []contractSource{}
	for _, path := range paths {
		if packet.Search.ScannedFiles >= 256 || packet.Search.ScannedBytes >= 8<<20 {
			break
		}
		data, err := snapshot.Read(path)
		packet.Search.ScannedFiles++
		if err != nil {
			if repository.IsStale(err) {
				return err
			}
			packet.Search.Limits = append(packet.Search.Limits, err.Error())
			continue
		}
		if len(data) > 8<<20-packet.Search.ScannedBytes {
			packet.Search.ScannedFiles--
			break
		}
		packet.Search.ScannedBytes += len(data)
		lines := repository.Lines(string(data))
		retained := false
		for _, ref := range names {
			inv := targets[ref]
			for line, text := range lines {
				if path == inv.Path && line+1 == inv.Declaration.Range.Start.Line+1 {
					continue
				}
				if !identifierMention(text, inv.Declaration.Name) {
					continue
				}
				if counts[ref] >= 64 {
					packet.Search.OmittedReferences++
					continue
				}
				candidate := ConsumerCandidate{Invariant: inv.Reference(), Path: path, Line: line + 1, Text: strings.TrimSpace(text), Relationship: "unresolved_lexical_candidate", Declaration: inv.Declaration.Identity}
				packet.Consumers = append(packet.Consumers, candidate)
				counts[ref]++
				retained = true
			}
		}
		if retained {
			sources = append(sources, resolver.Source(path))
		}
	}
	context, findings, err := resolver.Resolve(sources, contractOptions{Context: true})
	if err != nil {
		return err
	}
	for _, finding := range findings {
		packet.Search.Limits = append(packet.Search.Limits, finding.Path+": "+finding.Message)
	}
	packet.ConsumerDefinitions = context.Definitions
	files := map[string]FileContracts{}
	for _, file := range context.Files {
		files[file.Path] = file
	}
	for i := range packet.Consumers {
		candidate := &packet.Consumers[i]
		file := files[candidate.Path]
		document := contracts.Document{Invariants: file.Invariants}
		candidate.Obligations = document.Applicable(candidate.Line, 0)
		candidate.Scopes = []Obligation{}
		for _, obligation := range file.Applicable(Location{Path: candidate.Path, Line: candidate.Line}) {
			if obligation.Scope != "declaration" {
				candidate.Scopes = append(candidate.Scopes, obligation)
			}
		}
	}
	dependencies := make([]string, 0, len(context.Inputs))
	for path := range context.Inputs {
		dependencies = append(dependencies, path)
	}
	sort.Strings(dependencies)
	for _, path := range dependencies {
		if err := addSource(path); err != nil {
			return err
		}
	}
	packet.Search.UnscannedFiles = len(paths) - packet.Search.ScannedFiles
	return nil
}
func identifierMention(text, name string) bool {
	if name == "" {
		return false
	}
	word := func(c byte) bool {
		return c >= 65 && c <= 90 || c >= 97 && c <= 122 || c >= 48 && c <= 57 || c == 95 || c == 36 || c >= 128
	}
	offset := 0
	for {
		at := strings.Index(text[offset:], name)
		if at < 0 {
			return false
		}
		at += offset
		end := at + len(name)
		if (at == 0 || !word(text[at-1])) && (end == len(text) || !word(text[end])) {
			return true
		}
		offset = end
		if offset >= len(text) {
			return false
		}
	}
}
