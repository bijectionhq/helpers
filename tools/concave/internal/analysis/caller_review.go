package analysis

import (
	"fmt"
	"sort"

	"concave/internal/repository"
)

type CallerOrigin struct {
	Entry   string `json:"entry"`
	Module  string `json:"module"`
	Kind    string `json:"kind"`
	Binding string `json:"binding,omitempty"`
}
type CallerLocation struct {
	Location
	Origins     []CallerOrigin `json:"origins"`
	Obligations []string       `json:"obligations"`
}

// Direct import evidence gets a bounded context pass through the same resolver
// as ordinary source inspection. Lexical candidates retain their own provenance.
func (packet *ContractReview) resolveCallers(resolver *contractResolver, caseName string, scope []string, addSource func(string, *repository.State) error) error {
	snapshot := resolver.snapshot
	budget := callerBudget{files: 1024, sourceBytes: 16 << 20, locations: 8192}
	byLocation := map[Location]map[CallerOrigin]bool{}
	add := func(location Location, origin CallerOrigin) {
		if byLocation[location] == nil {
			byLocation[location] = map[CallerOrigin]bool{}
		}
		byLocation[location][origin] = true
	}
	for _, edge := range packet.ImportConsumers.Consumers {
		add(Location{edge.Path, edge.Line, edge.Column}, CallerOrigin{edge.Entry, edge.Module, edge.Kind, ""})
		for _, reference := range edge.References {
			add(Location{edge.Path, reference.Line, reference.Column}, CallerOrigin{edge.Entry, edge.Module, "import_binding_use", reference.Name})
		}
	}
	byPath := map[string][]Location{}
	for location := range byLocation {
		byPath[location.Path] = append(byPath[location.Path], location)
	}
	baseline, err := snapshot.Repo.Tree(packet.Against)
	if err != nil {
		return err
	}
	candidates, err := selectCallers(snapshot, baseline, byPath, scope, budget)
	if err != nil {
		return err
	}
	sources := []contractSource{}
	locations := []Location{}
	for _, candidate := range candidates {
		if len(candidate.locations) == 0 {
			continue
		}
		sources = append(sources, resolver.Source(candidate.path))
		locations = append(locations, candidate.locations...)
	}
	context, findings, err := resolver.Resolve(sources, contractOptions{Context: true, Case: caseName})
	if err != nil {
		return err
	}
	packet.CallerContext, packet.CallerFindings = context, findings
	files := map[string]FileContracts{}
	for _, file := range packet.CallerContext.Files {
		files[file.Path] = file
	}
	packet.CallerCoverage = callerCoverage(candidates, files, scope, budget)
	for _, location := range locations {
		file, ok := files[location.Path]
		if !ok {
			continue
		}

		obligations := map[string]bool{}
		for _, obligation := range file.Applicable(location) {
			obligations[obligation.Reference] = true
		}
		entry := CallerLocation{Location: location, Origins: []CallerOrigin{}, Obligations: []string{}}
		for origin := range byLocation[location] {
			entry.Origins = append(entry.Origins, origin)
		}
		sort.Slice(entry.Origins, func(i, j int) bool {
			a, b := entry.Origins[i], entry.Origins[j]
			if a.Entry != b.Entry {
				return a.Entry < b.Entry
			}
			if a.Module != b.Module {
				return a.Module < b.Module
			}
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return a.Binding < b.Binding
		})
		for reference := range obligations {
			entry.Obligations = append(entry.Obligations, reference)
		}
		sort.Strings(entry.Obligations)
		packet.CallerLocations = append(packet.CallerLocations, entry)
	}
	dependencies := make([]string, 0, len(packet.CallerContext.Inputs))
	for path := range packet.CallerContext.Inputs {
		dependencies = append(dependencies, path)
	}
	sort.Strings(dependencies)
	for _, path := range dependencies {
		state := packet.CallerContext.Inputs[path]
		if err = snapshot.Inputs.Observe(path, state); err != nil {
			return err
		}
		if err = addSource(path, state); err != nil {
			return fmt.Errorf("caller context %s: %w", path, err)
		}
	}
	return nil
}
